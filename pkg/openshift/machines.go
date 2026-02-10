package openshift

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
)

const (
	MachineAPINamespace = "openshift-machine-api"
)

// cpmsGVR is the GroupVersionResource for ControlPlaneMachineSet
var cpmsGVR = schema.GroupVersionResource{
	Group:    "machine.openshift.io",
	Version:  "v1",
	Resource: "controlplanemachinesets",
}

// MachineManager manages Machine API operations
type MachineManager struct {
	kubeClient    kubernetes.Interface
	machineClient machineclient.Interface
	dynamicClient dynamic.Interface
}

// NewMachineManager creates a new machine manager
func NewMachineManager(client kubernetes.Interface) *MachineManager {
	return &MachineManager{kubeClient: client}
}

// NewMachineManagerWithClients creates a new machine manager with all required clients
func NewMachineManagerWithClients(kubeClient kubernetes.Interface, machineClient machineclient.Interface, dynamicClient dynamic.Interface) *MachineManager {
	return &MachineManager{
		kubeClient:    kubeClient,
		machineClient: machineClient,
		dynamicClient: dynamicClient,
	}
}

// CreateWorkerMachineSet creates a new worker MachineSet in the target vCenter
func (m *MachineManager) CreateWorkerMachineSet(ctx context.Context, name string, migration *migrationv1alpha1.VmwareCloudFoundationMigration, template *machinev1beta1.MachineSet, infraID string) (*machinev1beta1.MachineSet, error) {
	logger := klog.FromContext(ctx)

	if m.machineClient == nil {
		return nil, fmt.Errorf("machine client not initialized")
	}

	// Create new MachineSet based on template
	newMachineSet := template.DeepCopy()
	newMachineSet.Name = name
	newMachineSet.ResourceVersion = ""
	newMachineSet.UID = ""
	newMachineSet.CreationTimestamp = metav1.Time{}

	// Update replicas
	replicas := migration.Spec.MachineSetConfig.Replicas
	newMachineSet.Spec.Replicas = &replicas

	// Update failure domain in annotations
	if newMachineSet.Annotations == nil {
		newMachineSet.Annotations = make(map[string]string)
	}
	newMachineSet.Annotations["machine.openshift.io/failure-domain"] = migration.Spec.MachineSetConfig.FailureDomain

	// Update failure domain in labels
	if newMachineSet.Labels == nil {
		newMachineSet.Labels = make(map[string]string)
	}
	newMachineSet.Labels["machine.openshift.io/failure-domain"] = migration.Spec.MachineSetConfig.FailureDomain

	// Update selector to use new MachineSet name
	if newMachineSet.Spec.Selector.MatchLabels == nil {
		newMachineSet.Spec.Selector.MatchLabels = make(map[string]string)
	}
	newMachineSet.Spec.Selector.MatchLabels["machine.openshift.io/cluster-api-machineset"] = name

	// Update template labels to match selector
	if newMachineSet.Spec.Template.ObjectMeta.Labels == nil {
		newMachineSet.Spec.Template.ObjectMeta.Labels = make(map[string]string)
	}
	newMachineSet.Spec.Template.ObjectMeta.Labels["machine.openshift.io/cluster-api-machineset"] = name

	// Find target failure domain
	var targetFailureDomain *configv1.VSpherePlatformFailureDomainSpec
	for i := range migration.Spec.FailureDomains {
		if migration.Spec.FailureDomains[i].Name == migration.Spec.MachineSetConfig.FailureDomain {
			targetFailureDomain = &migration.Spec.FailureDomains[i]
			break
		}
	}
	if targetFailureDomain == nil {
		return nil, fmt.Errorf("failure domain %s not found", migration.Spec.MachineSetConfig.FailureDomain)
	}

	// Validate template field is set
	if targetFailureDomain.Topology.Template == "" {
		logger.Error(nil, "Template field is empty in failure domain",
			"failureDomain", targetFailureDomain.Name,
			"topology", fmt.Sprintf("%+v", targetFailureDomain.Topology))
		return nil, fmt.Errorf("template not specified in failure domain %s - check VmwareCloudFoundationMigration CR topology.template field",
			targetFailureDomain.Name)
	}

	logger.Info("Using failure domain configuration",
		"name", targetFailureDomain.Name,
		"template", targetFailureDomain.Topology.Template,
		"server", targetFailureDomain.Server,
		"datacenter", targetFailureDomain.Topology.Datacenter)

	// Update providerSpec with target vCenter configuration
	if err := updateMachineSetProviderSpec(newMachineSet, targetFailureDomain, infraID); err != nil {
		return nil, fmt.Errorf("failed to update providerSpec: %w", err)
	}

	logger.Info("Creating new worker MachineSet",
		"name", name,
		"replicas", replicas,
		"failureDomain", migration.Spec.MachineSetConfig.FailureDomain,
		"server", targetFailureDomain.Server,
		"datacenter", targetFailureDomain.Topology.Datacenter,
		"template", targetFailureDomain.Topology.Template)

	// Create MachineSet using OpenShift machine client
	created, err := m.machineClient.MachineV1beta1().MachineSets(MachineAPINamespace).Create(ctx, newMachineSet, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create MachineSet: %w", err)
	}

	logger.Info("Successfully created MachineSet", "name", name)
	return created, nil
}

// updateMachineSetProviderSpec updates the vSphere providerSpec with target vCenter configuration
func updateMachineSetProviderSpec(
	machineSet *machinev1beta1.MachineSet,
	failureDomain *configv1.VSpherePlatformFailureDomainSpec,
	infraID string,
) error {
	// Get providerSpec from MachineSet
	providerSpecValue := machineSet.Spec.Template.Spec.ProviderSpec.Value
	if providerSpecValue == nil || providerSpecValue.Raw == nil {
		return fmt.Errorf("providerSpec.value is nil")
	}

	// Unmarshal to map for manipulation
	var providerSpec map[string]interface{}
	if err := json.Unmarshal(providerSpecValue.Raw, &providerSpec); err != nil {
		return fmt.Errorf("failed to unmarshal providerSpec: %w", err)
	}

	// Update workspace fields
	workspace := map[string]interface{}{
		"server":       failureDomain.Server,
		"datacenter":   failureDomain.Topology.Datacenter,
		"datastore":    failureDomain.Topology.Datastore,
		"folder":       fmt.Sprintf("/%s/vm/%s", failureDomain.Topology.Datacenter, infraID),
		"resourcePool": failureDomain.Topology.ResourcePool,
	}
	providerSpec["workspace"] = workspace

	// Update template
	providerSpec["template"] = failureDomain.Topology.Template

	// Update network devices
	if len(failureDomain.Topology.Networks) > 0 {
		network := map[string]interface{}{
			"devices": []map[string]interface{}{
				{"networkName": failureDomain.Topology.Networks[0]},
			},
		}
		providerSpec["network"] = network
	}

	// Marshal back to RawExtension
	updatedRaw, err := json.Marshal(providerSpec)
	if err != nil {
		return fmt.Errorf("failed to marshal providerSpec: %w", err)
	}

	machineSet.Spec.Template.Spec.ProviderSpec.Value.Raw = updatedRaw
	return nil
}

// updateCPMSProviderSpec updates the CPMS with target vCenter configuration
func updateCPMSProviderSpec(
	cpms *unstructured.Unstructured,
	failureDomain *configv1.VSpherePlatformFailureDomainSpec,
	infraID string,
) error {
	// Deep copy to avoid modifying original
	cpms = cpms.DeepCopy()

	// Update failureDomains.vsphere[].name
	// Path: spec.template.machines_v1beta1_machine_openshift_io.failureDomains.vsphere[0].name
	failureDomains, found, err := unstructured.NestedSlice(cpms.Object,
		"spec", "template", "machines_v1beta1_machine_openshift_io", "failureDomains", "vsphere")
	if err != nil || !found {
		return fmt.Errorf("failed to get CPMS failureDomains: %w", err)
	}

	if len(failureDomains) > 0 {
		if fdMap, ok := failureDomains[0].(map[string]interface{}); ok {
			fdMap["name"] = failureDomain.Name
			failureDomains[0] = fdMap
		}
	}

	if err := unstructured.SetNestedSlice(cpms.Object, failureDomains,
		"spec", "template", "machines_v1beta1_machine_openshift_io", "failureDomains", "vsphere"); err != nil {
		return fmt.Errorf("failed to set CPMS failureDomains: %w", err)
	}

	// Update providerSpec (similar to MachineSet)
	// Path: spec.template.machines_v1beta1_machine_openshift_io.spec.providerSpec.value
	providerSpecValue, found, err := unstructured.NestedMap(cpms.Object,
		"spec", "template", "machines_v1beta1_machine_openshift_io", "spec", "providerSpec", "value")
	if err != nil || !found {
		return fmt.Errorf("failed to get CPMS providerSpec: %w", err)
	}

	// Update workspace
	workspace := map[string]interface{}{
		"server":       failureDomain.Server,
		"datacenter":   failureDomain.Topology.Datacenter,
		"datastore":    failureDomain.Topology.Datastore,
		"folder":       fmt.Sprintf("/%s/vm/%s", failureDomain.Topology.Datacenter, infraID),
		"resourcePool": failureDomain.Topology.ResourcePool,
	}
	providerSpecValue["workspace"] = workspace

	// Update template
	providerSpecValue["template"] = failureDomain.Topology.Template

	// Update network
	if len(failureDomain.Topology.Networks) > 0 {
		network := map[string]interface{}{
			"devices": []map[string]interface{}{
				{"networkName": failureDomain.Topology.Networks[0]},
			},
		}
		providerSpecValue["network"] = network
	}

	if err := unstructured.SetNestedMap(cpms.Object, providerSpecValue,
		"spec", "template", "machines_v1beta1_machine_openshift_io", "spec", "providerSpec", "value"); err != nil {
		return fmt.Errorf("failed to set CPMS providerSpec: %w", err)
	}

	// Set spec.state to "Active" to trigger rollout
	if err := unstructured.SetNestedField(cpms.Object, "Active", "spec", "state"); err != nil {
		return fmt.Errorf("failed to set CPMS state: %w", err)
	}

	return nil
}

// GetMachineSet gets a specific MachineSet by name
func (m *MachineManager) GetMachineSet(ctx context.Context, name string) (*machinev1beta1.MachineSet, error) {
	if m.machineClient == nil {
		return nil, fmt.Errorf("machine client not initialized")
	}

	return m.machineClient.MachineV1beta1().MachineSets(MachineAPINamespace).Get(ctx, name, metav1.GetOptions{})
}

// GetMachineSetsByVCenter returns MachineSets for a specific vCenter.
// If vcenterServer is empty, returns all MachineSets (useful for getting templates).
// If vcenterServer is specified, filters to only MachineSets targeting that vCenter.
func (m *MachineManager) GetMachineSetsByVCenter(ctx context.Context, vcenterServer string) ([]*machinev1beta1.MachineSet, error) {
	logger := klog.FromContext(ctx)

	if m.machineClient == nil {
		return nil, fmt.Errorf("machine client not initialized")
	}

	// List all MachineSets
	machineSetList, err := m.machineClient.MachineV1beta1().MachineSets(MachineAPINamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list MachineSets: %w", err)
	}

	// If no vCenter specified, return all MachineSets (for template purposes)
	if vcenterServer == "" {
		var result []*machinev1beta1.MachineSet
		for i := range machineSetList.Items {
			result = append(result, &machineSetList.Items[i])
		}
		logger.Info("Found all MachineSets", "count", len(result))
		return result, nil
	}

	// Filter by vCenter server in providerSpec
	var result []*machinev1beta1.MachineSet
	for i := range machineSetList.Items {
		ms := &machineSetList.Items[i]

		// Extract vCenter server from providerSpec.workspace.server
		server, err := getVCenterServerFromMachineSet(ms)
		if err != nil {
			logger.V(4).Info("Could not extract vCenter server from MachineSet, skipping",
				"name", ms.Name, "error", err)
			continue
		}

		if server == vcenterServer {
			result = append(result, ms)
		}
	}

	logger.Info("Found MachineSets for vCenter", "server", vcenterServer, "count", len(result))
	return result, nil
}

// getVCenterServerFromMachineSet extracts the vCenter server from the MachineSet's providerSpec
func getVCenterServerFromMachineSet(ms *machinev1beta1.MachineSet) (string, error) {
	providerSpecValue := ms.Spec.Template.Spec.ProviderSpec.Value
	if providerSpecValue == nil || providerSpecValue.Raw == nil {
		return "", fmt.Errorf("providerSpec.value is nil")
	}

	var providerSpec map[string]interface{}
	if err := json.Unmarshal(providerSpecValue.Raw, &providerSpec); err != nil {
		return "", fmt.Errorf("failed to unmarshal providerSpec: %w", err)
	}

	workspace, ok := providerSpec["workspace"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("workspace not found in providerSpec")
	}

	server, ok := workspace["server"].(string)
	if !ok {
		return "", fmt.Errorf("server not found in workspace")
	}

	return server, nil
}

// DeleteMachineSet deletes a MachineSet
func (m *MachineManager) DeleteMachineSet(ctx context.Context, name string) error {
	logger := klog.FromContext(ctx)
	logger.Info("Deleting MachineSet", "name", name)

	if m.machineClient == nil {
		return fmt.Errorf("machine client not initialized")
	}

	err := m.machineClient.MachineV1beta1().MachineSets(MachineAPINamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete MachineSet: %w", err)
	}

	logger.Info("Successfully deleted MachineSet", "name", name)
	return nil
}

// WaitForMachinesReady waits for all machines in a MachineSet to be ready
func (m *MachineManager) WaitForMachinesReady(ctx context.Context, machineSetName string, timeout time.Duration) (int32, int32, error) {
	logger := klog.FromContext(ctx)

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return 0, 0, fmt.Errorf("timeout waiting for machines to be ready")
			}

			ready, total, err := m.getMachineStatus(ctx, machineSetName)
			if err != nil {
				logger.V(2).Info("Error getting machine status", "error", err)
				continue
			}

			if ready == total && total > 0 {
				logger.Info("All machines ready", "ready", ready, "total", total)
				return ready, total, nil
			}
			logger.V(2).Info("Waiting for machines", "ready", ready, "total", total)
		}
	}
}

// WaitForNodesReady waits for nodes corresponding to machines to be ready
func (m *MachineManager) WaitForNodesReady(ctx context.Context, machineSetName string, timeout time.Duration) (int32, int32, error) {
	logger := klog.FromContext(ctx)

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return 0, 0, fmt.Errorf("timeout waiting for nodes to be ready")
			}

			// Get nodes for this MachineSet
			ready, total, err := m.getNodeStatus(ctx, machineSetName)
			if err != nil {
				logger.V(2).Info("Error getting node status", "error", err)
				continue
			}

			if ready == total {
				logger.Info("All nodes ready", "ready", ready, "total", total)
				return ready, total, nil
			}

			logger.V(2).Info("Waiting for nodes", "ready", ready, "total", total)
		}
	}
}

// getMachineStatus returns ready and total machine counts for a MachineSet
func (m *MachineManager) getMachineStatus(ctx context.Context, machineSetName string) (int32, int32, error) {
	if m.machineClient == nil {
		return 0, 0, fmt.Errorf("machine client not initialized")
	}

	// List machines for this MachineSet
	machines, err := m.machineClient.MachineV1beta1().Machines(MachineAPINamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			"machine.openshift.io/cluster-api-machineset": machineSetName,
		}).String(),
	})
	if err != nil {
		return 0, 0, err
	}

	total := int32(len(machines.Items))
	ready := int32(0)

	for _, machine := range machines.Items {
		// Check if machine has a node ref and is in Running phase
		if machine.Status.NodeRef != nil && machine.Status.Phase != nil && *machine.Status.Phase == "Running" {
			ready++
		}
	}

	return ready, total, nil
}

// getNodeStatus returns ready and total node counts for a MachineSet
func (m *MachineManager) getNodeStatus(ctx context.Context, machineSetName string) (int32, int32, error) {
	if m.machineClient == nil {
		return 0, 0, fmt.Errorf("machine client not initialized")
	}

	// List machines for this MachineSet
	machines, err := m.machineClient.MachineV1beta1().Machines(MachineAPINamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			"machine.openshift.io/cluster-api-machineset": machineSetName,
		}).String(),
	})
	if err != nil {
		return 0, 0, err
	}

	total := int32(0)
	ready := int32(0)

	for _, machine := range machines.Items {
		// Only count machines that have a node reference
		if machine.Status.NodeRef == nil {
			continue
		}
		total++

		// Get the node and check if it's ready
		node, err := m.kubeClient.CoreV1().Nodes().Get(ctx, machine.Status.NodeRef.Name, metav1.GetOptions{})
		if err != nil {
			continue // Node not found yet
		}

		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				ready++
				break
			}
		}
	}

	return ready, total, nil
}

// ScaleMachineSet scales a MachineSet to the specified number of replicas
func (m *MachineManager) ScaleMachineSet(ctx context.Context, name string, replicas int32) error {
	logger := klog.FromContext(ctx)
	logger.Info("Scaling MachineSet", "name", name, "replicas", replicas)

	if m.machineClient == nil {
		return fmt.Errorf("machine client not initialized")
	}

	// Patch MachineSet with new replica count
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))
	_, err := m.machineClient.MachineV1beta1().MachineSets(MachineAPINamespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to scale MachineSet: %w", err)
	}

	logger.Info("Successfully scaled MachineSet", "name", name, "replicas", replicas)
	return nil
}

// GetControlPlaneMachineSet gets the Control Plane Machine Set as an unstructured object for backup
func (m *MachineManager) GetControlPlaneMachineSet(ctx context.Context) (*unstructured.Unstructured, error) {
	logger := klog.FromContext(ctx)

	if m.dynamicClient == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}

	// Get CPMS using dynamic client
	cpms, err := m.dynamicClient.Resource(cpmsGVR).Namespace(MachineAPINamespace).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		logger.V(2).Info("CPMS not found in openshift-machine-api", "error", err)
		return nil, err
	}

	logger.Info("Successfully retrieved CPMS from openshift-machine-api namespace")
	return cpms, nil
}

// DeleteControlPlaneMachineSet deletes the Control Plane Machine Set
func (m *MachineManager) DeleteControlPlaneMachineSet(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Deleting Control Plane Machine Set from openshift-machine-api")

	if m.dynamicClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	// Delete CPMS using dynamic client
	err := m.dynamicClient.Resource(cpmsGVR).Namespace(MachineAPINamespace).Delete(ctx, "cluster", metav1.DeleteOptions{})
	if err != nil {
		logger.Info("Failed to delete CPMS from openshift-machine-api (may not exist)", "error", err)
		return err
	}

	logger.Info("Successfully deleted CPMS from openshift-machine-api namespace")
	return nil
}

// WaitForCPMSDeletion waits for CPMS to be fully deleted
func (m *MachineManager) WaitForCPMSDeletion(ctx context.Context, timeout time.Duration) error {
	logger := klog.FromContext(ctx)

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for CPMS deletion")
			}

			_, err := m.dynamicClient.Resource(cpmsGVR).Namespace(MachineAPINamespace).Get(ctx, "cluster", metav1.GetOptions{})
			if errors.IsNotFound(err) {
				logger.Info("CPMS successfully deleted")
				return nil
			}
		}
	}
}

// WaitForCPMSInactive waits for CPMS to become Inactive state
func (m *MachineManager) WaitForCPMSInactive(ctx context.Context, timeout time.Duration) error {
	logger := klog.FromContext(ctx)

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for CPMS to become Inactive")
			}

			cpms, err := m.dynamicClient.Resource(cpmsGVR).Namespace(MachineAPINamespace).Get(ctx, "cluster", metav1.GetOptions{})
			if err != nil {
				logger.V(2).Info("Error getting CPMS", "error", err)
				continue
			}

			state, found, err := unstructured.NestedString(cpms.Object, "spec", "state")
			if err != nil || !found {
				logger.V(2).Info("CPMS state not found yet")
				continue
			}

			logger.V(2).Info("CPMS state", "state", state)
			if state == "Inactive" {
				logger.Info("CPMS is now Inactive")
				return nil
			}
		}
	}
}

// UpdateCPMSFailureDomain updates an existing CPMS with new failure domain and sets it to Active
func (m *MachineManager) UpdateCPMSFailureDomain(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration, infraID string) error {
	logger := klog.FromContext(ctx)

	if m.dynamicClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	// Get current CPMS
	cpms, err := m.dynamicClient.Resource(cpmsGVR).Namespace(MachineAPINamespace).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get CPMS: %w", err)
	}

	targetFailureDomainName := migration.Spec.ControlPlaneMachineSetConfig.FailureDomain

	logger.Info("Updating CPMS failure domain reference",
		"newFailureDomain", targetFailureDomainName)

	// Update failureDomains.vsphere[].name
	// The CPMS operator will automatically inject workspace/network/template from infrastructure CR
	failureDomains, found, err := unstructured.NestedSlice(cpms.Object,
		"spec", "template", "machines_v1beta1_machine_openshift_io", "failureDomains", "vsphere")
	if err != nil || !found {
		return fmt.Errorf("failed to get CPMS failureDomains: %w", err)
	}

	if len(failureDomains) > 0 {
		if fdMap, ok := failureDomains[0].(map[string]interface{}); ok {
			fdMap["name"] = targetFailureDomainName
			failureDomains[0] = fdMap
		}
	}

	if err := unstructured.SetNestedSlice(cpms.Object, failureDomains,
		"spec", "template", "machines_v1beta1_machine_openshift_io", "failureDomains", "vsphere"); err != nil {
		return fmt.Errorf("failed to set CPMS failureDomains: %w", err)
	}

	// Set state to Active to trigger rollout
	if err := unstructured.SetNestedField(cpms.Object, "Active", "spec", "state"); err != nil {
		return fmt.Errorf("failed to set CPMS state to Active: %w", err)
	}

	// Update CPMS
	_, err = m.dynamicClient.Resource(cpmsGVR).Namespace(MachineAPINamespace).Update(ctx, cpms, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update CPMS: %w", err)
	}

	logger.Info("Successfully updated CPMS failure domain reference and set to Active",
		"failureDomain", targetFailureDomainName)
	return nil
}

// CreateControlPlaneMachineSet creates a new Control Plane Machine Set
func (m *MachineManager) CreateControlPlaneMachineSet(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration, template interface{}, infraID string) error {
	logger := klog.FromContext(ctx)
	logger.Info("Creating Control Plane Machine Set",
		"failureDomain", migration.Spec.ControlPlaneMachineSetConfig.FailureDomain)

	if m.dynamicClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	cpmsTemplate, ok := template.(*unstructured.Unstructured)
	if !ok || cpmsTemplate == nil {
		return fmt.Errorf("invalid CPMS template provided")
	}

	// Deep copy template
	cpmsTemplate = cpmsTemplate.DeepCopy()

	// Find target failure domain
	var targetFailureDomain *configv1.VSpherePlatformFailureDomainSpec
	for i := range migration.Spec.FailureDomains {
		if migration.Spec.FailureDomains[i].Name == migration.Spec.ControlPlaneMachineSetConfig.FailureDomain {
			targetFailureDomain = &migration.Spec.FailureDomains[i]
			break
		}
	}
	if targetFailureDomain == nil {
		return fmt.Errorf("failure domain %s not found", migration.Spec.ControlPlaneMachineSetConfig.FailureDomain)
	}

	// Update providerSpec with target configuration
	if err := updateCPMSProviderSpec(cpmsTemplate, targetFailureDomain, infraID); err != nil {
		return fmt.Errorf("failed to update CPMS providerSpec: %w", err)
	}

	// Update metadata
	cpmsTemplate.SetName("cluster")
	cpmsTemplate.SetNamespace(MachineAPINamespace)
	cpmsTemplate.SetResourceVersion("")
	cpmsTemplate.SetUID("")

	logger.Info("Creating CPMS with updated configuration",
		"failureDomain", targetFailureDomain.Name,
		"server", targetFailureDomain.Server,
		"state", "Active")

	// Create CPMS
	_, err := m.dynamicClient.Resource(cpmsGVR).Namespace(MachineAPINamespace).Create(ctx, cpmsTemplate, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create CPMS: %w", err)
	}

	logger.Info("Successfully created Control Plane Machine Set")
	return nil
}

// CheckControlPlaneRolloutStatus checks if control plane rollout is complete without blocking
func (m *MachineManager) CheckControlPlaneRolloutStatus(ctx context.Context) (complete bool, replicas, updatedReplicas, readyReplicas int32, err error) {
	logger := klog.FromContext(ctx)

	if m.dynamicClient == nil {
		return false, 0, 0, 0, fmt.Errorf("dynamic client not initialized")
	}

	cpms, err := m.dynamicClient.Resource(cpmsGVR).Namespace(MachineAPINamespace).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return false, 0, 0, 0, fmt.Errorf("failed to get CPMS: %w", err)
	}

	status, found, err := unstructured.NestedMap(cpms.Object, "status")
	if err != nil || !found {
		return false, 0, 0, 0, fmt.Errorf("failed to get CPMS status")
	}

	replicasInt, _, _ := unstructured.NestedInt64(status, "replicas")
	updatedReplicasInt, _, _ := unstructured.NestedInt64(status, "updatedReplicas")
	readyReplicasInt, _, _ := unstructured.NestedInt64(status, "readyReplicas")

	replicas = int32(replicasInt)
	updatedReplicas = int32(updatedReplicasInt)
	readyReplicas = int32(readyReplicasInt)

	complete = replicas > 0 && replicas == updatedReplicas && replicas == readyReplicas

	logger.V(2).Info("Control plane rollout status",
		"complete", complete,
		"replicas", replicas,
		"updatedReplicas", updatedReplicas,
		"readyReplicas", readyReplicas)

	return complete, replicas, updatedReplicas, readyReplicas, nil
}

// IsCPMSGenerationObserved checks if the CPMS controller has processed the latest spec update
// by comparing metadata.generation to status.observedGeneration.
func (m *MachineManager) IsCPMSGenerationObserved(ctx context.Context) (bool, error) {
	if m.dynamicClient == nil {
		return false, fmt.Errorf("dynamic client not initialized")
	}

	cpms, err := m.dynamicClient.Resource(cpmsGVR).Namespace(MachineAPINamespace).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get CPMS: %w", err)
	}

	generation := cpms.GetGeneration()

	observedGeneration, found, err := unstructured.NestedInt64(cpms.Object, "status", "observedGeneration")
	if err != nil || !found {
		// If observedGeneration doesn't exist, controller hasn't processed anything yet
		return false, nil
	}

	return observedGeneration >= generation, nil
}

// WaitForControlPlaneRollout waits for the control plane rollout to complete
func (m *MachineManager) WaitForControlPlaneRollout(ctx context.Context, timeout time.Duration) error {
	logger := klog.FromContext(ctx)

	if m.dynamicClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for control plane rollout")
			}

			complete, replicas, updatedReplicas, readyReplicas, err := m.CheckControlPlaneRolloutStatus(ctx)
			if err != nil {
				logger.V(2).Info("Error checking CPMS status", "error", err)
				continue
			}

			if complete {
				logger.Info("Control plane rollout complete",
					"replicas", replicas,
					"updatedReplicas", updatedReplicas,
					"readyReplicas", readyReplicas)
				return nil
			}

			logger.V(2).Info("Waiting for control plane rollout",
				"replicas", replicas,
				"updatedReplicas", updatedReplicas,
				"readyReplicas", readyReplicas)
		}
	}
}

// CheckMachinesReady checks if all machines in a MachineSet are ready without blocking
func (m *MachineManager) CheckMachinesReady(ctx context.Context, machineSetName string) (complete bool, ready, total int32, err error) {
	logger := klog.FromContext(ctx)

	ready, total, err = m.getMachineStatus(ctx, machineSetName)
	if err != nil {
		return false, 0, 0, err
	}

	complete = ready == total && total > 0

	logger.V(2).Info("Machine readiness status",
		"machineSet", machineSetName,
		"complete", complete,
		"ready", ready,
		"total", total)

	return complete, ready, total, nil
}

// CheckNodesReady checks if nodes corresponding to machines are ready without blocking
func (m *MachineManager) CheckNodesReady(ctx context.Context, machineSetName string) (complete bool, ready, total int32, err error) {
	logger := klog.FromContext(ctx)

	ready, total, err = m.getNodeStatus(ctx, machineSetName)
	if err != nil {
		return false, 0, 0, err
	}

	complete = ready == total && total > 0

	logger.V(2).Info("Node readiness status",
		"machineSet", machineSetName,
		"complete", complete,
		"ready", ready,
		"total", total)

	return complete, ready, total, nil
}

// CheckMachinesDeleted checks if all Machine objects for a MachineSet have been deleted
func (m *MachineManager) CheckMachinesDeleted(ctx context.Context, machineSetName string) (allDeleted bool, remaining int32, err error) {
	logger := klog.FromContext(ctx)

	if m.machineClient == nil {
		return false, 0, fmt.Errorf("machine client not initialized")
	}

	machines, err := m.machineClient.MachineV1beta1().Machines(MachineAPINamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			"machine.openshift.io/cluster-api-machineset": machineSetName,
		}).String(),
	})
	if err != nil {
		return false, 0, fmt.Errorf("failed to list machines for MachineSet %s: %w", machineSetName, err)
	}

	remaining = int32(len(machines.Items))
	allDeleted = remaining == 0

	logger.V(2).Info("Machine deletion status",
		"machineSet", machineSetName,
		"allDeleted", allDeleted,
		"remaining", remaining)

	return allDeleted, remaining, nil
}

// CheckNodesDeletedForMachines checks if all Nodes referenced by Machines in a MachineSet have been removed
func (m *MachineManager) CheckNodesDeletedForMachines(ctx context.Context, machineSetName string) (allDeleted bool, remaining int32, err error) {
	logger := klog.FromContext(ctx)

	if m.machineClient == nil {
		return false, 0, fmt.Errorf("machine client not initialized")
	}

	machines, err := m.machineClient.MachineV1beta1().Machines(MachineAPINamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			"machine.openshift.io/cluster-api-machineset": machineSetName,
		}).String(),
	})
	if err != nil {
		return false, 0, fmt.Errorf("failed to list machines for MachineSet %s: %w", machineSetName, err)
	}

	// If no machines exist, all nodes are gone too
	if len(machines.Items) == 0 {
		return true, 0, nil
	}

	remaining = 0
	for _, machine := range machines.Items {
		if machine.Status.NodeRef == nil {
			continue
		}
		_, err := m.kubeClient.CoreV1().Nodes().Get(ctx, machine.Status.NodeRef.Name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				continue // Node is gone
			}
			logger.V(2).Info("Error checking node existence", "node", machine.Status.NodeRef.Name, "error", err)
			continue
		}
		// Node still exists
		remaining++
	}

	allDeleted = remaining == 0

	logger.V(2).Info("Node deletion status for MachineSet",
		"machineSet", machineSetName,
		"allDeleted", allDeleted,
		"remaining", remaining)

	return allDeleted, remaining, nil
}
