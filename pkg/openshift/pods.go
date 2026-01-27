package openshift

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// PodManager manages pod operations
type PodManager struct {
	client kubernetes.Interface
}

// NewPodManager creates a new pod manager
func NewPodManager(client kubernetes.Interface) *PodManager {
	return &PodManager{client: client}
}

// DeletePodsByLabel deletes all pods with matching labels in a namespace
func (m *PodManager) DeletePodsByLabel(ctx context.Context, namespace string, labelSelector map[string]string) (int, error) {
	logger := klog.FromContext(ctx)

	selector := labels.SelectorFromSet(labelSelector).String()

	logger.Info("Deleting pods",
		"namespace", namespace,
		"labelSelector", selector)

	pods, err := m.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to list pods: %w", err)
	}

	count := len(pods.Items)

	for _, pod := range pods.Items {
		logger.V(2).Info("Deleting pod", "pod", pod.Name)
		err := m.client.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
		if err != nil {
			logger.Error(err, "Failed to delete pod", "pod", pod.Name)
			// Continue with other pods
		}
	}

	logger.Info("Deleted pods", "count", count, "namespace", namespace)
	return count, nil
}

// WaitForPodsReady waits for all pods with matching labels to be ready
func (m *PodManager) WaitForPodsReady(ctx context.Context, namespace string, labelSelector map[string]string, timeout time.Duration) error {
	logger := klog.FromContext(ctx)

	selector := labels.SelectorFromSet(labelSelector).String()

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for pods to be ready")
			}

			ready, total, err := m.getPodReadyCount(ctx, namespace, selector)
			if err != nil {
				logger.V(2).Info("Error getting pod status", "error", err)
				continue
			}

			if ready == total && total > 0 {
				logger.Info("All pods are ready",
					"namespace", namespace,
					"ready", ready,
					"total", total)
				return nil
			}

			logger.V(2).Info("Waiting for pods to be ready",
				"namespace", namespace,
				"ready", ready,
				"total", total)
		}
	}
}

// getPodReadyCount returns the number of ready and total pods
func (m *PodManager) getPodReadyCount(ctx context.Context, namespace, selector string) (int, int, error) {
	pods, err := m.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return 0, 0, err
	}

	total := len(pods.Items)
	ready := 0

	for _, pod := range pods.Items {
		if isPodReady(&pod) {
			ready++
		}
	}

	return ready, total, nil
}

// isPodReady checks if a pod is ready
func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

// RestartVSpherePods restarts vSphere-related pods
func (m *PodManager) RestartVSpherePods(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Restarting vSphere-related pods")

	// Delete cloud controller manager pods
	_, err := m.DeletePodsByLabel(ctx, "openshift-cloud-controller-manager", map[string]string{
		"app": "vsphere-cloud-controller-manager",
	})
	if err != nil {
		logger.Error(err, "Failed to delete cloud controller manager pods")
		// Continue with other pods
	}

	// Delete machine API controller pods
	_, err = m.DeletePodsByLabel(ctx, "openshift-machine-api", map[string]string{
		"api": "clusterapi",
	})
	if err != nil {
		logger.Error(err, "Failed to delete machine API controller pods")
	}

	// Delete CSI driver pods
	_, err = m.DeletePodsByLabel(ctx, "openshift-cluster-csi-drivers", map[string]string{
		"app": "vmware-vsphere-csi-driver",
	})
	if err != nil {
		logger.Error(err, "Failed to delete CSI driver pods")
	}

	logger.Info("Successfully triggered restart of vSphere pods")
	return nil
}

// WaitForVSpherePodsReady waits for all vSphere pods to be ready
func (m *PodManager) WaitForVSpherePodsReady(ctx context.Context, timeout time.Duration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Waiting for vSphere pods to be ready")

	// Wait for cloud controller manager
	if err := m.WaitForPodsReady(ctx, "openshift-cloud-controller-manager", map[string]string{
		"app": "vsphere-cloud-controller-manager",
	}, timeout); err != nil {
		return fmt.Errorf("cloud controller manager pods not ready: %w", err)
	}

	// Wait for machine API controllers
	if err := m.WaitForPodsReady(ctx, "openshift-machine-api", map[string]string{
		"api": "clusterapi",
	}, timeout); err != nil {
		return fmt.Errorf("machine API controller pods not ready: %w", err)
	}

	// Wait for CSI driver
	if err := m.WaitForPodsReady(ctx, "openshift-cluster-csi-drivers", map[string]string{
		"app": "vmware-vsphere-csi-driver",
	}, timeout); err != nil {
		return fmt.Errorf("CSI driver pods not ready: %w", err)
	}

	logger.Info("All vSphere pods are ready")
	return nil
}
