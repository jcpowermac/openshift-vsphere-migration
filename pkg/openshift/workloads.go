package openshift

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
)

// WorkloadManager manages workload scaling operations for CSI volume migration
type WorkloadManager struct {
	kubeClient kubernetes.Interface
}

// NewWorkloadManager creates a new workload manager
func NewWorkloadManager(kubeClient kubernetes.Interface) *WorkloadManager {
	return &WorkloadManager{
		kubeClient: kubeClient,
	}
}

// ScaleDownForPV scales down all workloads using a specific PVC
// Returns the list of scaled down resources for later restoration
func (m *WorkloadManager) ScaleDownForPV(ctx context.Context, pvcNamespace, pvcName string) ([]migrationv1alpha1.ScaledResource, error) {
	logger := klog.FromContext(ctx)
	logger.Info("Scaling down workloads for PVC", "namespace", pvcNamespace, "pvc", pvcName)

	var scaledResources []migrationv1alpha1.ScaledResource

	// Find and scale down Deployments
	deployments, err := m.findDeploymentsUsingPVC(ctx, pvcNamespace, pvcName)
	if err != nil {
		return nil, fmt.Errorf("failed to find deployments: %w", err)
	}

	for _, deploy := range deployments {
		if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas > 0 {
			originalReplicas := *deploy.Spec.Replicas
			logger.Info("Scaling down Deployment", "name", deploy.Name, "namespace", deploy.Namespace, "replicas", originalReplicas)

			if err := m.scaleDeployment(ctx, deploy.Namespace, deploy.Name, 0); err != nil {
				return scaledResources, fmt.Errorf("failed to scale deployment %s: %w", deploy.Name, err)
			}

			scaledResources = append(scaledResources, migrationv1alpha1.ScaledResource{
				Kind:             "Deployment",
				Name:             deploy.Name,
				Namespace:        deploy.Namespace,
				OriginalReplicas: originalReplicas,
			})
		}
	}

	// Find and scale down StatefulSets
	statefulSets, err := m.findStatefulSetsUsingPVC(ctx, pvcNamespace, pvcName)
	if err != nil {
		return scaledResources, fmt.Errorf("failed to find statefulsets: %w", err)
	}

	for _, sts := range statefulSets {
		if sts.Spec.Replicas != nil && *sts.Spec.Replicas > 0 {
			originalReplicas := *sts.Spec.Replicas
			logger.Info("Scaling down StatefulSet", "name", sts.Name, "namespace", sts.Namespace, "replicas", originalReplicas)

			if err := m.scaleStatefulSet(ctx, sts.Namespace, sts.Name, 0); err != nil {
				return scaledResources, fmt.Errorf("failed to scale statefulset %s: %w", sts.Name, err)
			}

			scaledResources = append(scaledResources, migrationv1alpha1.ScaledResource{
				Kind:             "StatefulSet",
				Name:             sts.Name,
				Namespace:        sts.Namespace,
				OriginalReplicas: originalReplicas,
			})
		}
	}

	// Find and scale down ReplicaSets (standalone, not owned by Deployments)
	replicaSets, err := m.findStandaloneReplicaSetsUsingPVC(ctx, pvcNamespace, pvcName)
	if err != nil {
		return scaledResources, fmt.Errorf("failed to find replicasets: %w", err)
	}

	for _, rs := range replicaSets {
		if rs.Spec.Replicas != nil && *rs.Spec.Replicas > 0 {
			originalReplicas := *rs.Spec.Replicas
			logger.Info("Scaling down ReplicaSet", "name", rs.Name, "namespace", rs.Namespace, "replicas", originalReplicas)

			if err := m.scaleReplicaSet(ctx, rs.Namespace, rs.Name, 0); err != nil {
				return scaledResources, fmt.Errorf("failed to scale replicaset %s: %w", rs.Name, err)
			}

			scaledResources = append(scaledResources, migrationv1alpha1.ScaledResource{
				Kind:             "ReplicaSet",
				Name:             rs.Name,
				Namespace:        rs.Namespace,
				OriginalReplicas: originalReplicas,
			})
		}
	}

	logger.Info("Scaled down workloads", "count", len(scaledResources))
	return scaledResources, nil
}

// RestoreWorkloads restores previously scaled down workloads to their original replica counts
func (m *WorkloadManager) RestoreWorkloads(ctx context.Context, scaledResources []migrationv1alpha1.ScaledResource) error {
	logger := klog.FromContext(ctx)
	logger.Info("Restoring workloads", "count", len(scaledResources))

	var errs []error
	for _, resource := range scaledResources {
		logger.Info("Restoring workload",
			"kind", resource.Kind,
			"name", resource.Name,
			"namespace", resource.Namespace,
			"replicas", resource.OriginalReplicas)

		var err error
		switch resource.Kind {
		case "Deployment":
			err = m.scaleDeployment(ctx, resource.Namespace, resource.Name, resource.OriginalReplicas)
		case "StatefulSet":
			err = m.scaleStatefulSet(ctx, resource.Namespace, resource.Name, resource.OriginalReplicas)
		case "ReplicaSet":
			err = m.scaleReplicaSet(ctx, resource.Namespace, resource.Name, resource.OriginalReplicas)
		default:
			err = fmt.Errorf("unknown resource kind: %s", resource.Kind)
		}

		if err != nil {
			logger.Error(err, "Failed to restore workload", "kind", resource.Kind, "name", resource.Name)
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to restore %d workloads", len(errs))
	}

	logger.Info("Successfully restored all workloads")
	return nil
}

// WaitForPodsTerminated waits for all pods using a PVC to terminate
func (m *WorkloadManager) WaitForPodsTerminated(ctx context.Context, pvcNamespace, pvcName string, timeout time.Duration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Waiting for pods to terminate", "namespace", pvcNamespace, "pvc", pvcName)

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	pvManager := NewPersistentVolumeManager(m.kubeClient)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for pods to terminate")
			}

			pods, err := pvManager.FindPodsUsingPVC(ctx, pvcNamespace, pvcName)
			if err != nil {
				logger.V(2).Info("Error listing pods", "error", err)
				continue
			}

			// Filter out terminated pods
			activePods := 0
			for _, pod := range pods {
				if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
					activePods++
				}
			}

			if activePods == 0 {
				logger.Info("All pods using PVC have terminated", "namespace", pvcNamespace, "pvc", pvcName)
				return nil
			}

			logger.V(2).Info("Waiting for pods to terminate", "activePods", activePods)
		}
	}
}

// WaitForWorkloadsReady waits for restored workloads to become ready
func (m *WorkloadManager) WaitForWorkloadsReady(ctx context.Context, scaledResources []migrationv1alpha1.ScaledResource, timeout time.Duration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Waiting for workloads to become ready", "count", len(scaledResources))

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for workloads to become ready")
			}

			allReady := true
			for _, resource := range scaledResources {
				ready, err := m.isWorkloadReady(ctx, resource)
				if err != nil {
					logger.V(2).Info("Error checking workload readiness", "error", err)
					allReady = false
					continue
				}
				if !ready {
					allReady = false
				}
			}

			if allReady {
				logger.Info("All workloads are ready")
				return nil
			}
		}
	}
}

// findDeploymentsUsingPVC finds all Deployments using a specific PVC
func (m *WorkloadManager) findDeploymentsUsingPVC(ctx context.Context, namespace, pvcName string) ([]appsv1.Deployment, error) {
	deployList, err := m.kubeClient.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var usingDeployments []appsv1.Deployment
	for _, deploy := range deployList.Items {
		if m.podTemplateUsesPVC(&deploy.Spec.Template, pvcName) {
			usingDeployments = append(usingDeployments, deploy)
		}
	}

	return usingDeployments, nil
}

// findStatefulSetsUsingPVC finds all StatefulSets using a specific PVC
func (m *WorkloadManager) findStatefulSetsUsingPVC(ctx context.Context, namespace, pvcName string) ([]appsv1.StatefulSet, error) {
	stsList, err := m.kubeClient.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var usingSTS []appsv1.StatefulSet
	for _, sts := range stsList.Items {
		if m.podTemplateUsesPVC(&sts.Spec.Template, pvcName) {
			usingSTS = append(usingSTS, sts)
		}
		// Also check volumeClaimTemplates for StatefulSets
		for _, vct := range sts.Spec.VolumeClaimTemplates {
			if vct.Name == pvcName {
				usingSTS = append(usingSTS, sts)
				break
			}
		}
	}

	return usingSTS, nil
}

// findStandaloneReplicaSetsUsingPVC finds ReplicaSets not owned by Deployments
func (m *WorkloadManager) findStandaloneReplicaSetsUsingPVC(ctx context.Context, namespace, pvcName string) ([]appsv1.ReplicaSet, error) {
	rsList, err := m.kubeClient.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var standaloneRS []appsv1.ReplicaSet
	for _, rs := range rsList.Items {
		// Skip if owned by a Deployment
		if hasOwnerOfKind(rs.OwnerReferences, "Deployment") {
			continue
		}

		if m.podTemplateUsesPVC(&rs.Spec.Template, pvcName) {
			standaloneRS = append(standaloneRS, rs)
		}
	}

	return standaloneRS, nil
}

// podTemplateUsesPVC checks if a pod template references a specific PVC
func (m *WorkloadManager) podTemplateUsesPVC(template *corev1.PodTemplateSpec, pvcName string) bool {
	for _, vol := range template.Spec.Volumes {
		if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == pvcName {
			return true
		}
	}
	return false
}

// hasOwnerOfKind checks if any owner reference is of a specific kind
func hasOwnerOfKind(owners []metav1.OwnerReference, kind string) bool {
	for _, owner := range owners {
		if owner.Kind == kind {
			return true
		}
	}
	return false
}

// scaleDeployment scales a deployment to the specified replicas
func (m *WorkloadManager) scaleDeployment(ctx context.Context, namespace, name string, replicas int32) error {
	deploy, err := m.kubeClient.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // Deployment was deleted, nothing to scale
		}
		return err
	}

	deploy.Spec.Replicas = ptr.To(replicas)
	_, err = m.kubeClient.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	return err
}

// scaleStatefulSet scales a statefulset to the specified replicas
func (m *WorkloadManager) scaleStatefulSet(ctx context.Context, namespace, name string, replicas int32) error {
	sts, err := m.kubeClient.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	sts.Spec.Replicas = ptr.To(replicas)
	_, err = m.kubeClient.AppsV1().StatefulSets(namespace).Update(ctx, sts, metav1.UpdateOptions{})
	return err
}

// scaleReplicaSet scales a replicaset to the specified replicas
func (m *WorkloadManager) scaleReplicaSet(ctx context.Context, namespace, name string, replicas int32) error {
	rs, err := m.kubeClient.AppsV1().ReplicaSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	rs.Spec.Replicas = ptr.To(replicas)
	_, err = m.kubeClient.AppsV1().ReplicaSets(namespace).Update(ctx, rs, metav1.UpdateOptions{})
	return err
}

// isWorkloadReady checks if a workload is ready
func (m *WorkloadManager) isWorkloadReady(ctx context.Context, resource migrationv1alpha1.ScaledResource) (bool, error) {
	switch resource.Kind {
	case "Deployment":
		deploy, err := m.kubeClient.AppsV1().Deployments(resource.Namespace).Get(ctx, resource.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return deploy.Status.ReadyReplicas == resource.OriginalReplicas, nil

	case "StatefulSet":
		sts, err := m.kubeClient.AppsV1().StatefulSets(resource.Namespace).Get(ctx, resource.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return sts.Status.ReadyReplicas == resource.OriginalReplicas, nil

	case "ReplicaSet":
		rs, err := m.kubeClient.AppsV1().ReplicaSets(resource.Namespace).Get(ctx, resource.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return rs.Status.ReadyReplicas == resource.OriginalReplicas, nil

	default:
		return false, fmt.Errorf("unknown resource kind: %s", resource.Kind)
	}
}

// DeletePodsUsingPVC deletes all pods using a specific PVC
func (m *WorkloadManager) DeletePodsUsingPVC(ctx context.Context, pvcNamespace, pvcName string) error {
	logger := klog.FromContext(ctx)
	logger.Info("Deleting pods using PVC", "namespace", pvcNamespace, "pvc", pvcName)

	pvManager := NewPersistentVolumeManager(m.kubeClient)
	pods, err := pvManager.FindPodsUsingPVC(ctx, pvcNamespace, pvcName)
	if err != nil {
		return fmt.Errorf("failed to find pods: %w", err)
	}

	for _, pod := range pods {
		logger.Info("Deleting pod", "name", pod.Name, "namespace", pod.Namespace)
		err := m.kubeClient.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete pod %s: %w", pod.Name, err)
		}
	}

	return nil
}

// GetPodsBySelector returns pods matching a label selector
func (m *WorkloadManager) GetPodsBySelector(ctx context.Context, namespace string, selector map[string]string) ([]corev1.Pod, error) {
	podList, err := m.kubeClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(selector).String(),
	})
	if err != nil {
		return nil, err
	}
	return podList.Items, nil
}
