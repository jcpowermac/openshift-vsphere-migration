package openshift

import (
	"context"
	"fmt"
	"time"

	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// VolumeAttachmentManager manages VolumeAttachment operations for CSI volume migration
type VolumeAttachmentManager struct {
	kubeClient kubernetes.Interface
}

// NewVolumeAttachmentManager creates a new VolumeAttachment manager
func NewVolumeAttachmentManager(kubeClient kubernetes.Interface) *VolumeAttachmentManager {
	return &VolumeAttachmentManager{
		kubeClient: kubeClient,
	}
}

// GetVolumeAttachmentForPV finds the VolumeAttachment for a specific PV
// Returns nil if no VolumeAttachment exists for the PV
func (m *VolumeAttachmentManager) GetVolumeAttachmentForPV(ctx context.Context, pvName string) (*storagev1.VolumeAttachment, error) {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Looking for VolumeAttachment for PV", "pv", pvName)

	// List all VolumeAttachments and filter by PV name
	// VolumeAttachments don't have a label selector for PV, so we must list and filter
	vaList, err := m.kubeClient.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list VolumeAttachments: %w", err)
	}

	for _, va := range vaList.Items {
		// Check if this VolumeAttachment references the PV
		if va.Spec.Source.PersistentVolumeName != nil && *va.Spec.Source.PersistentVolumeName == pvName {
			logger.V(2).Info("Found VolumeAttachment for PV",
				"pv", pvName,
				"volumeAttachment", va.Name,
				"node", va.Spec.NodeName,
				"attached", va.Status.Attached)
			return &va, nil
		}
	}

	logger.V(2).Info("No VolumeAttachment found for PV", "pv", pvName)
	return nil, nil
}

// IsVolumeAttached checks if a volume is currently attached per VolumeAttachment objects
// Returns: attached bool, nodeName string (if attached), error
func (m *VolumeAttachmentManager) IsVolumeAttached(ctx context.Context, pvName string) (bool, string, error) {
	va, err := m.GetVolumeAttachmentForPV(ctx, pvName)
	if err != nil {
		return false, "", err
	}

	if va == nil {
		return false, "", nil
	}

	// VolumeAttachment exists - volume is attached to this node
	return true, va.Spec.NodeName, nil
}

// WaitForVolumeDetached waits for the VolumeAttachment for a PV to be deleted
// This confirms that the CSI driver has completed the vSphere-level detachment
func (m *VolumeAttachmentManager) WaitForVolumeDetached(ctx context.Context, pvName string, timeout time.Duration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Waiting for VolumeAttachment deletion (confirms vSphere-level detachment)",
		"pv", pvName, "timeout", timeout)

	return wait.PollUntilContextTimeout(ctx, 3*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		va, err := m.GetVolumeAttachmentForPV(ctx, pvName)
		if err != nil {
			// Transient errors should retry
			logger.V(2).Info("Error checking VolumeAttachment, retrying", "pv", pvName, "error", err)
			return false, nil
		}

		if va == nil {
			// VolumeAttachment is gone - CSI driver has completed detachment
			logger.Info("VolumeAttachment deleted - volume detachment confirmed at K8s level", "pv", pvName)
			return true, nil
		}

		// Log progress for visibility
		logger.V(2).Info("VolumeAttachment still exists, waiting for CSI driver to complete detachment",
			"pv", pvName,
			"volumeAttachment", va.Name,
			"node", va.Spec.NodeName,
			"attached", va.Status.Attached)

		return false, nil
	})
}

// ListVolumeAttachments lists all VolumeAttachments in the cluster
func (m *VolumeAttachmentManager) ListVolumeAttachments(ctx context.Context) ([]storagev1.VolumeAttachment, error) {
	vaList, err := m.kubeClient.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list VolumeAttachments: %w", err)
	}
	return vaList.Items, nil
}

// GetVolumeAttachment retrieves a VolumeAttachment by name
func (m *VolumeAttachmentManager) GetVolumeAttachment(ctx context.Context, name string) (*storagev1.VolumeAttachment, error) {
	va, err := m.kubeClient.StorageV1().VolumeAttachments().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get VolumeAttachment %s: %w", name, err)
	}
	return va, nil
}
