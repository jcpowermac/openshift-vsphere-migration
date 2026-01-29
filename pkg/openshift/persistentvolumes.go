package openshift

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
