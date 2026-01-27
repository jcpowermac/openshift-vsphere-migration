package openshift

import (
	"context"
	"encoding/json"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
)

const (
	InfrastructureName = "cluster"
)

// InfrastructureManager manages Infrastructure CRD operations
type InfrastructureManager struct {
	client            configclient.Interface
	kubeClient        kubernetes.Interface
	apiextensionsClient apiextensionsclient.Interface
}

// NewInfrastructureManager creates a new infrastructure manager
func NewInfrastructureManager(client configclient.Interface) *InfrastructureManager {
	return &InfrastructureManager{client: client}
}

// NewInfrastructureManagerWithClients creates a new infrastructure manager with all required clients
func NewInfrastructureManagerWithClients(client configclient.Interface, kubeClient kubernetes.Interface, apiextensionsClient apiextensionsclient.Interface) *InfrastructureManager {
	return &InfrastructureManager{
		client:              client,
		kubeClient:          kubeClient,
		apiextensionsClient: apiextensionsClient,
	}
}

// Get retrieves the cluster infrastructure
func (m *InfrastructureManager) Get(ctx context.Context) (*configv1.Infrastructure, error) {
	return m.client.ConfigV1().Infrastructures().Get(ctx, InfrastructureName, metav1.GetOptions{})
}

// GetSourceVCenter returns the source vCenter from the Infrastructure CRD
// The source vCenter is the first vCenter in the infrastructure spec
func (m *InfrastructureManager) GetSourceVCenter(ctx context.Context) (*configv1.VSpherePlatformVCenterSpec, error) {
	infra, err := m.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get infrastructure: %w", err)
	}

	if infra.Spec.PlatformSpec.VSphere == nil {
		return nil, fmt.Errorf("infrastructure is not vSphere platform")
	}

	if len(infra.Spec.PlatformSpec.VSphere.VCenters) == 0 {
		return nil, fmt.Errorf("no vCenters configured in infrastructure")
	}

	// The first vCenter is the source
	return &infra.Spec.PlatformSpec.VSphere.VCenters[0], nil
}

// AddTargetVCenter adds the target vCenter to the infrastructure spec
func (m *InfrastructureManager) AddTargetVCenter(ctx context.Context, infra *configv1.Infrastructure, migration *migrationv1alpha1.VSphereMigration) (*configv1.Infrastructure, error) {
	logger := klog.FromContext(ctx)

	if infra.Spec.PlatformSpec.VSphere == nil {
		return nil, fmt.Errorf("infrastructure is not vSphere platform")
	}

	if len(migration.Spec.FailureDomains) == 0 {
		return nil, fmt.Errorf("no failure domains specified in migration spec")
	}

	// Extract unique target vCenters and datacenters from failure domains
	vCenterMap := make(map[string]map[string]bool) // server -> datacenter -> true
	for _, fd := range migration.Spec.FailureDomains {
		if _, exists := vCenterMap[fd.Server]; !exists {
			vCenterMap[fd.Server] = make(map[string]bool)
		}
		vCenterMap[fd.Server][fd.Topology.Datacenter] = true
	}

	// Add target vCenters if they don't already exist
	for server, datacenters := range vCenterMap {
		// Check if vCenter already exists
		exists := false
		for _, vc := range infra.Spec.PlatformSpec.VSphere.VCenters {
			if vc.Server == server {
				logger.Info("Target vCenter already exists in infrastructure", "server", server)
				exists = true
				break
			}
		}

		if !exists {
			// Build datacenter list
			var dcList []string
			for dc := range datacenters {
				dcList = append(dcList, dc)
			}

			targetVC := configv1.VSpherePlatformVCenterSpec{
				Server:      server,
				Datacenters: dcList,
			}
			infra.Spec.PlatformSpec.VSphere.VCenters = append(infra.Spec.PlatformSpec.VSphere.VCenters, targetVC)
			logger.Info("Adding target vCenter to infrastructure", "server", server, "datacenters", dcList)
		}
	}

	// Add failure domains
	for _, fd := range migration.Spec.FailureDomains {
		failureDomain := configv1.VSpherePlatformFailureDomainSpec{
			Name:   fd.Name,
			Region: fd.Region,
			Zone:   fd.Zone,
			Server: fd.Server,
			Topology: configv1.VSpherePlatformTopology{
				Datacenter:     fd.Topology.Datacenter,
				ComputeCluster: fd.Topology.ComputeCluster,
				Datastore:      fd.Topology.Datastore,
				Networks:       fd.Topology.Networks,
			},
		}
		infra.Spec.PlatformSpec.VSphere.FailureDomains = append(infra.Spec.PlatformSpec.VSphere.FailureDomains, failureDomain)
	}

	logger.Info("Adding target vCenter configuration to infrastructure",
		"failureDomains", len(migration.Spec.FailureDomains))

	// Update infrastructure
	updated, err := m.client.ConfigV1().Infrastructures().Update(ctx, infra, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update infrastructure: %w", err)
	}

	logger.Info("Successfully updated infrastructure with target vCenter")
	return updated, nil
}

// RemoveSourceVCenter removes the source vCenter from the infrastructure spec
func (m *InfrastructureManager) RemoveSourceVCenter(ctx context.Context, infra *configv1.Infrastructure, sourceServer string) (*configv1.Infrastructure, error) {
	logger := klog.FromContext(ctx)

	if infra.Spec.PlatformSpec.VSphere == nil {
		return nil, fmt.Errorf("infrastructure is not vSphere platform")
	}

	// Remove source vCenter
	var newVCenters []configv1.VSpherePlatformVCenterSpec
	for _, vc := range infra.Spec.PlatformSpec.VSphere.VCenters {
		if vc.Server != sourceServer {
			newVCenters = append(newVCenters, vc)
		}
	}

	infra.Spec.PlatformSpec.VSphere.VCenters = newVCenters

	// Remove source vCenter failure domains
	var newFailureDomains []configv1.VSpherePlatformFailureDomainSpec
	for _, fd := range infra.Spec.PlatformSpec.VSphere.FailureDomains {
		if fd.Server != sourceServer {
			newFailureDomains = append(newFailureDomains, fd)
		}
	}

	infra.Spec.PlatformSpec.VSphere.FailureDomains = newFailureDomains

	logger.Info("Removing source vCenter from infrastructure", "server", sourceServer)

	// Update infrastructure
	updated, err := m.client.ConfigV1().Infrastructures().Update(ctx, infra, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update infrastructure: %w", err)
	}

	logger.Info("Successfully removed source vCenter from infrastructure")
	return updated, nil
}

// GetInfrastructureID returns the infrastructure ID
func (m *InfrastructureManager) GetInfrastructureID(ctx context.Context) (string, error) {
	infra, err := m.Get(ctx)
	if err != nil {
		return "", err
	}

	if infra.Status.InfrastructureName == "" {
		return "", fmt.Errorf("infrastructure ID is empty")
	}

	return infra.Status.InfrastructureName, nil
}

// BackupInfrastructureCRD backs up the Infrastructure CRD definition
func (m *InfrastructureManager) BackupInfrastructureCRD(ctx context.Context) ([]byte, error) {
	if m.apiextensionsClient == nil {
		return nil, fmt.Errorf("apiextensionsClient not set - use NewInfrastructureManagerWithClients")
	}

	logger := klog.FromContext(ctx)
	crdName := "infrastructures.config.openshift.io"

	logger.Info("Backing up Infrastructure CRD", "crd", crdName)

	// Get the CRD
	crd, err := m.apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crdName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get Infrastructure CRD: %w", err)
	}

	// Marshal to JSON for backup
	crdBytes, err := json.Marshal(crd)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal CRD: %w", err)
	}

	logger.Info("Successfully backed up Infrastructure CRD")
	return crdBytes, nil
}

// ModifyInfrastructureCRDToAllowVCenterChanges removes the immutability constraint on vcenters field
func (m *InfrastructureManager) ModifyInfrastructureCRDToAllowVCenterChanges(ctx context.Context) error {
	if m.apiextensionsClient == nil {
		return fmt.Errorf("apiextensionsClient not set - use NewInfrastructureManagerWithClients")
	}

	logger := klog.FromContext(ctx)
	crdName := "infrastructures.config.openshift.io"

	logger.Info("Modifying Infrastructure CRD to allow vCenter changes", "crd", crdName)

	// Get current CRD
	crd, err := m.apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crdName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get Infrastructure CRD: %w", err)
	}

	// Find and remove x-kubernetes-validations that prevent vcenters array modification
	// This is in spec.versions[].schema.openAPIV3Schema.properties.spec.properties.platformSpec.properties.vsphere.properties.vcenters
	modified := false
	for i := range crd.Spec.Versions {
		version := &crd.Spec.Versions[i]
		if version.Schema != nil && version.Schema.OpenAPIV3Schema != nil {
			props := version.Schema.OpenAPIV3Schema.Properties
			if spec, ok := props["spec"]; ok {
				if platformSpec, ok := spec.Properties["platformSpec"]; ok {
					if vsphere, ok := platformSpec.Properties["vsphere"]; ok {
						if vcenters, ok := vsphere.Properties["vcenters"]; ok {
							// Remove x-kubernetes-validations
							if len(vcenters.XValidations) > 0 {
								logger.Info("Removing x-kubernetes-validations from vcenters field",
									"validations", len(vcenters.XValidations))
								vcenters.XValidations = nil
								vsphere.Properties["vcenters"] = vcenters
								modified = true
							}
						}
					}
				}
			}
		}
	}

	if !modified {
		logger.Info("No vcenters validation found to remove - may not be needed")
		return nil
	}

	// Update the CRD
	_, err = m.apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, crd, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update Infrastructure CRD: %w", err)
	}

	logger.Info("Successfully modified Infrastructure CRD to allow vCenter changes")
	return nil
}

// RestoreInfrastructureCRD restores the Infrastructure CRD from backup
func (m *InfrastructureManager) RestoreInfrastructureCRD(ctx context.Context, backupBytes []byte) error {
	if m.apiextensionsClient == nil {
		return fmt.Errorf("apiextensionsClient not set - use NewInfrastructureManagerWithClients")
	}

	logger := klog.FromContext(ctx)
	crdName := "infrastructures.config.openshift.io"

	logger.Info("Restoring Infrastructure CRD from backup", "crd", crdName)

	// Unmarshal backup
	var crd apiextensionsv1.CustomResourceDefinition
	if err := json.Unmarshal(backupBytes, &crd); err != nil {
		return fmt.Errorf("failed to unmarshal CRD backup: %w", err)
	}

	// Update to restore
	_, err := m.apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, &crd, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to restore Infrastructure CRD: %w", err)
	}

	logger.Info("Successfully restored Infrastructure CRD")
	return nil
}

// AddTargetVCenterWithCRDModification adds the target vCenter by modifying the CRD
// The CRD is backed up, modified, Infrastructure is updated, then CRD is immediately restored
func (m *InfrastructureManager) AddTargetVCenterWithCRDModification(ctx context.Context, infra *configv1.Infrastructure, migration *migrationv1alpha1.VSphereMigration) (*configv1.Infrastructure, error) {
	logger := klog.FromContext(ctx)

	// Backup CRD first
	crdBackup, err := m.BackupInfrastructureCRD(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to backup CRD: %w", err)
	}

	// Modify CRD to allow vCenter changes
	if err := m.ModifyInfrastructureCRDToAllowVCenterChanges(ctx); err != nil {
		return nil, fmt.Errorf("failed to modify CRD: %w", err)
	}

	logger.Info("Modified Infrastructure CRD temporarily to allow vCenter changes")

	// Now perform the update
	updated, err := m.AddTargetVCenter(ctx, infra, migration)
	if err != nil {
		// Restore CRD on failure
		if restoreErr := m.RestoreInfrastructureCRD(ctx, crdBackup); restoreErr != nil {
			logger.Error(restoreErr, "Failed to restore CRD after Infrastructure update failure")
		}
		return nil, err
	}

	// RESTORE CRD IMMEDIATELY after successful update
	if err := m.RestoreInfrastructureCRD(ctx, crdBackup); err != nil {
		logger.Error(err, "Failed to restore CRD after Infrastructure update - CVO should eventually fix this")
		// Continue - the update succeeded, CVO should restore the CRD
	} else {
		logger.Info("Successfully restored Infrastructure CRD after update")
	}

	return updated, nil
}
