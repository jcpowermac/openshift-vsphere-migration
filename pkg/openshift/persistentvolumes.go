package openshift

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	// VSphereCSIDriver is the driver name for vSphere CSI
	VSphereCSIDriver = "csi.vsphere.vmware.com"
)

// PersistentVolumeManager manages PV operations
type PersistentVolumeManager struct {
	kubeClient kubernetes.Interface
}

// VSphereCSIPV represents a vSphere CSI PersistentVolume
type VSphereCSIPV struct {
	Name            string
	VolumeHandle    string
	CapacityBytes   int64
	StorageClass    string
	AccessModes     []corev1.PersistentVolumeAccessMode
	ReclaimPolicy   corev1.PersistentVolumeReclaimPolicy
	ClaimRef        *corev1.ObjectReference
	Attributes      map[string]string
}

// NewPersistentVolumeManager creates a new PV manager
func NewPersistentVolumeManager(kubeClient kubernetes.Interface) *PersistentVolumeManager {
	return &PersistentVolumeManager{
		kubeClient: kubeClient,
	}
}

// ListVSphereCSIVolumes lists all PVs using the vSphere CSI driver
func (m *PersistentVolumeManager) ListVSphereCSIVolumes(ctx context.Context) ([]VSphereCSIPV, error) {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Listing vSphere CSI PersistentVolumes")

	pvList, err := m.kubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list PersistentVolumes: %w", err)
	}

	var csiPVs []VSphereCSIPV
	for _, pv := range pvList.Items {
		// Skip if not a CSI volume
		if pv.Spec.CSI == nil {
			continue
		}

		// Skip if not vSphere CSI driver
		if pv.Spec.CSI.Driver != VSphereCSIDriver {
			continue
		}

		// Skip volumes in terminating state
		if pv.DeletionTimestamp != nil {
			continue
		}

		// Extract capacity
		var capacityBytes int64
		if qty, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
			capacityBytes = qty.Value()
		}

		csiPV := VSphereCSIPV{
			Name:            pv.Name,
			VolumeHandle:    pv.Spec.CSI.VolumeHandle,
			CapacityBytes:   capacityBytes,
			StorageClass:    pv.Spec.StorageClassName,
			AccessModes:     pv.Spec.AccessModes,
			ReclaimPolicy:   pv.Spec.PersistentVolumeReclaimPolicy,
			ClaimRef:        pv.Spec.ClaimRef,
			Attributes:      pv.Spec.CSI.VolumeAttributes,
		}

		csiPVs = append(csiPVs, csiPV)
	}

	logger.Info("Found vSphere CSI PersistentVolumes", "count", len(csiPVs))
	return csiPVs, nil
}

// GetPV retrieves a PersistentVolume by name
func (m *PersistentVolumeManager) GetPV(ctx context.Context, name string) (*corev1.PersistentVolume, error) {
	return m.kubeClient.CoreV1().PersistentVolumes().Get(ctx, name, metav1.GetOptions{})
}

// GetPVC retrieves a PersistentVolumeClaim
func (m *PersistentVolumeManager) GetPVC(ctx context.Context, namespace, name string) (*corev1.PersistentVolumeClaim, error) {
	return m.kubeClient.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
}

// UpdatePVVolumeHandle updates the volumeHandle in a PV's CSI spec
// This is used after migrating the underlying FCD to update the PV to point to the new volume ID
func (m *PersistentVolumeManager) UpdatePVVolumeHandle(ctx context.Context, pvName string, newVolumeHandle string) error {
	logger := klog.FromContext(ctx)
	logger.Info("Updating PV volumeHandle", "pv", pvName, "newVolumeHandle", newVolumeHandle)

	pv, err := m.kubeClient.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get PV %s: %w", pvName, err)
	}

	if pv.Spec.CSI == nil {
		return fmt.Errorf("PV %s is not a CSI volume", pvName)
	}

	// Store old handle for logging
	oldHandle := pv.Spec.CSI.VolumeHandle

	// Update the volume handle
	pv.Spec.CSI.VolumeHandle = newVolumeHandle

	// Update the PV
	_, err = m.kubeClient.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update PV %s: %w", pvName, err)
	}

	logger.Info("Successfully updated PV volumeHandle",
		"pv", pvName,
		"oldHandle", oldHandle,
		"newHandle", newVolumeHandle)
	return nil
}

// FindPodsUsingPVC finds all pods that are using a specific PVC
func (m *PersistentVolumeManager) FindPodsUsingPVC(ctx context.Context, pvcNamespace, pvcName string) ([]corev1.Pod, error) {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Finding pods using PVC", "namespace", pvcNamespace, "pvc", pvcName)

	// List all pods in the namespace
	podList, err := m.kubeClient.CoreV1().Pods(pvcNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods in namespace %s: %w", pvcNamespace, err)
	}

	var usingPods []corev1.Pod
	for _, pod := range podList.Items {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == pvcName {
				usingPods = append(usingPods, pod)
				break
			}
		}
	}

	logger.V(2).Info("Found pods using PVC", "namespace", pvcNamespace, "pvc", pvcName, "count", len(usingPods))
	return usingPods, nil
}

// FindAllPodsUsingPVC finds all pods across all namespaces using a specific PVC
func (m *PersistentVolumeManager) FindAllPodsUsingPVC(ctx context.Context, pvcNamespace, pvcName string) ([]corev1.Pod, error) {
	// For PVCs, pods must be in the same namespace as the PVC
	return m.FindPodsUsingPVC(ctx, pvcNamespace, pvcName)
}

// GetVolumeHandleFromPV extracts the volume handle from a PV
func GetVolumeHandleFromPV(pv *corev1.PersistentVolume) (string, error) {
	if pv.Spec.CSI == nil {
		return "", fmt.Errorf("PV %s is not a CSI volume", pv.Name)
	}
	return pv.Spec.CSI.VolumeHandle, nil
}

// ParseVSphereVolumeHandle parses a vSphere CSI volume handle
// Format can be: file://<fcd-id> or just <fcd-id>
func ParseVSphereVolumeHandle(volumeHandle string) (fcdID string, err error) {
	if strings.HasPrefix(volumeHandle, "file://") {
		return strings.TrimPrefix(volumeHandle, "file://"), nil
	}
	return volumeHandle, nil
}

// BuildVSphereVolumeHandle builds a vSphere CSI volume handle from an FCD ID
func BuildVSphereVolumeHandle(fcdID string) string {
	return fmt.Sprintf("file://%s", fcdID)
}

// GetPVsByStorageClass lists all PVs for a specific storage class
func (m *PersistentVolumeManager) GetPVsByStorageClass(ctx context.Context, storageClassName string) ([]corev1.PersistentVolume, error) {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Listing PVs by storage class", "storageClass", storageClassName)

	pvList, err := m.kubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list PersistentVolumes: %w", err)
	}

	var pvs []corev1.PersistentVolume
	for _, pv := range pvList.Items {
		if pv.Spec.StorageClassName == storageClassName {
			pvs = append(pvs, pv)
		}
	}

	logger.V(2).Info("Found PVs for storage class", "storageClass", storageClassName, "count", len(pvs))
	return pvs, nil
}

// IsPVBound checks if a PV is bound to a PVC
func IsPVBound(pv *corev1.PersistentVolume) bool {
	return pv.Status.Phase == corev1.VolumeBound && pv.Spec.ClaimRef != nil
}

// GetPVCFromPV returns the PVC reference from a bound PV
func GetPVCFromPV(pv *corev1.PersistentVolume) (namespace, name string, ok bool) {
	if pv.Spec.ClaimRef == nil {
		return "", "", false
	}
	return pv.Spec.ClaimRef.Namespace, pv.Spec.ClaimRef.Name, true
}

// WaitForPVAvailable waits for a PV to become Available
func (m *PersistentVolumeManager) WaitForPVAvailable(ctx context.Context, pvName string) error {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Waiting for PV to become available", "pv", pvName)

	// Simple polling - in production would use informers
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			pv, err := m.GetPV(ctx, pvName)
			if err != nil {
				return err
			}

			if pv.Status.Phase == corev1.VolumeAvailable {
				return nil
			}

			logger.V(2).Info("PV not yet available", "pv", pvName, "phase", pv.Status.Phase)
		}
	}
}

// UpdatePVReclaimPolicy updates the reclaim policy of a PV and returns the original policy
func (m *PersistentVolumeManager) UpdatePVReclaimPolicy(ctx context.Context, pvName string, newPolicy corev1.PersistentVolumeReclaimPolicy) (corev1.PersistentVolumeReclaimPolicy, error) {
	logger := klog.FromContext(ctx)
	logger.Info("Updating PV reclaim policy", "pv", pvName, "newPolicy", newPolicy)

	pv, err := m.kubeClient.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get PV %s: %w", pvName, err)
	}

	originalPolicy := pv.Spec.PersistentVolumeReclaimPolicy

	// Skip if already set to the desired policy
	if originalPolicy == newPolicy {
		logger.Info("PV already has desired reclaim policy", "pv", pvName, "policy", newPolicy)
		return originalPolicy, nil
	}

	// Update the reclaim policy
	pv.Spec.PersistentVolumeReclaimPolicy = newPolicy

	_, err = m.kubeClient.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to update PV %s reclaim policy: %w", pvName, err)
	}

	logger.Info("Successfully updated PV reclaim policy",
		"pv", pvName,
		"originalPolicy", originalPolicy,
		"newPolicy", newPolicy)
	return originalPolicy, nil
}

// DeletePVC deletes a PersistentVolumeClaim
func (m *PersistentVolumeManager) DeletePVC(ctx context.Context, namespace, name string) error {
	logger := klog.FromContext(ctx)
	logger.Info("Deleting PVC", "namespace", namespace, "name", name)

	err := m.kubeClient.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("PVC already deleted", "namespace", namespace, "name", name)
			return nil
		}
		return fmt.Errorf("failed to delete PVC %s/%s: %w", namespace, name, err)
	}

	logger.Info("Successfully deleted PVC", "namespace", namespace, "name", name)
	return nil
}

// WaitForPVCDeleted waits for a PVC to be fully deleted
func (m *PersistentVolumeManager) WaitForPVCDeleted(ctx context.Context, namespace, name string, timeout time.Duration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Waiting for PVC to be deleted", "namespace", namespace, "name", name, "timeout", timeout)

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := m.kubeClient.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				logger.Info("PVC deleted", "namespace", namespace, "name", name)
				return true, nil
			}
			return false, err
		}
		logger.V(2).Info("PVC still exists, waiting...", "namespace", namespace, "name", name)
		return false, nil
	})
}

// ClearPVClaimRef clears the claimRef on a PV to make it Available for rebinding
func (m *PersistentVolumeManager) ClearPVClaimRef(ctx context.Context, pvName string) error {
	logger := klog.FromContext(ctx)
	logger.Info("Clearing PV claimRef", "pv", pvName)

	pv, err := m.kubeClient.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get PV %s: %w", pvName, err)
	}

	if pv.Spec.ClaimRef == nil {
		logger.Info("PV claimRef already cleared", "pv", pvName)
		return nil
	}

	// Clear the claimRef
	pv.Spec.ClaimRef = nil

	_, err = m.kubeClient.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to clear claimRef on PV %s: %w", pvName, err)
	}

	logger.Info("Successfully cleared PV claimRef", "pv", pvName)
	return nil
}

// PVCBackup represents a backup of a PVC for restoration
type PVCBackup struct {
	Name             string                           `json:"name"`
	Namespace        string                           `json:"namespace"`
	StorageClassName string                           `json:"storageClassName,omitempty"`
	AccessModes      []corev1.PersistentVolumeAccessMode `json:"accessModes"`
	Resources        corev1.VolumeResourceRequirements   `json:"resources"`
	Labels           map[string]string                `json:"labels,omitempty"`
	Annotations      map[string]string                `json:"annotations,omitempty"`
}

// BackupPVCSpec captures a PVC spec as base64-encoded JSON for later restoration
func (m *PersistentVolumeManager) BackupPVCSpec(ctx context.Context, namespace, name string) (string, error) {
	logger := klog.FromContext(ctx)
	logger.Info("Backing up PVC spec", "namespace", namespace, "name", name)

	pvc, err := m.kubeClient.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get PVC %s/%s: %w", namespace, name, err)
	}

	backup := PVCBackup{
		Name:             pvc.Name,
		Namespace:        pvc.Namespace,
		AccessModes:      pvc.Spec.AccessModes,
		Resources:        pvc.Spec.Resources,
		Labels:           pvc.Labels,
		Annotations:      pvc.Annotations,
	}

	if pvc.Spec.StorageClassName != nil {
		backup.StorageClassName = *pvc.Spec.StorageClassName
	}

	jsonData, err := json.Marshal(backup)
	if err != nil {
		return "", fmt.Errorf("failed to marshal PVC backup: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(jsonData)
	logger.Info("Successfully backed up PVC spec", "namespace", namespace, "name", name)
	return encoded, nil
}

// RestorePVC recreates a PVC from a backup with explicit binding to a specific PV
func (m *PersistentVolumeManager) RestorePVC(ctx context.Context, pvcSpecBase64 string, targetPVName string) error {
	logger := klog.FromContext(ctx)
	logger.Info("Restoring PVC", "targetPV", targetPVName)

	// Decode the backup
	jsonData, err := base64.StdEncoding.DecodeString(pvcSpecBase64)
	if err != nil {
		return fmt.Errorf("failed to decode PVC backup: %w", err)
	}

	var backup PVCBackup
	if err := json.Unmarshal(jsonData, &backup); err != nil {
		return fmt.Errorf("failed to unmarshal PVC backup: %w", err)
	}

	// Create new PVC with explicit binding to the target PV
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        backup.Name,
			Namespace:   backup.Namespace,
			Labels:      backup.Labels,
			Annotations: backup.Annotations,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: backup.AccessModes,
			Resources:   backup.Resources,
			VolumeName:  targetPVName, // Explicit binding to the PV
		},
	}

	if backup.StorageClassName != "" {
		pvc.Spec.StorageClassName = &backup.StorageClassName
	}

	_, err = m.kubeClient.CoreV1().PersistentVolumeClaims(backup.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("PVC already exists", "namespace", backup.Namespace, "name", backup.Name)
			return nil
		}
		return fmt.Errorf("failed to create PVC %s/%s: %w", backup.Namespace, backup.Name, err)
	}

	logger.Info("Successfully restored PVC", "namespace", backup.Namespace, "name", backup.Name, "boundTo", targetPVName)
	return nil
}

// WaitForPVCBound waits for a PVC to become Bound
func (m *PersistentVolumeManager) WaitForPVCBound(ctx context.Context, namespace, name string, timeout time.Duration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Waiting for PVC to become bound", "namespace", namespace, "name", name, "timeout", timeout)

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pvc, err := m.kubeClient.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		if pvc.Status.Phase == corev1.ClaimBound {
			logger.Info("PVC is bound", "namespace", namespace, "name", name, "boundTo", pvc.Spec.VolumeName)
			return true, nil
		}

		logger.V(2).Info("PVC not yet bound", "namespace", namespace, "name", name, "phase", pvc.Status.Phase)
		return false, nil
	})
}
