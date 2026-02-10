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

// VolumeAttachmentIssue describes a stuck or problematic VolumeAttachment
type VolumeAttachmentIssue struct {
	VAName        string
	PVName        string
	NodeName      string
	DetachError   string
	DeletionStuck bool
	StuckDuration time.Duration
}

// DiagnoseStuckAttachments detects VolumeAttachments stuck in deletion
// Returns list of VolumeAttachments with deletion timestamps older than the timeout
func (m *VolumeAttachmentManager) DiagnoseStuckAttachments(ctx context.Context, timeout time.Duration) ([]VolumeAttachmentIssue, error) {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Diagnosing stuck VolumeAttachments", "timeout", timeout)

	vaList, err := m.kubeClient.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list VolumeAttachments: %w", err)
	}

	var issues []VolumeAttachmentIssue
	now := time.Now()

	for _, va := range vaList.Items {
		// Check if VolumeAttachment has deletion timestamp
		if va.DeletionTimestamp == nil {
			continue
		}

		stuckDuration := now.Sub(va.DeletionTimestamp.Time)
		if stuckDuration > timeout {
			issue := VolumeAttachmentIssue{
				VAName:        va.Name,
				NodeName:      va.Spec.NodeName,
				DeletionStuck: true,
				StuckDuration: stuckDuration,
			}

			// Get PV name if available
			if va.Spec.Source.PersistentVolumeName != nil {
				issue.PVName = *va.Spec.Source.PersistentVolumeName
			}

			// Check for detach errors in status
			if va.Status.DetachError != nil {
				issue.DetachError = va.Status.DetachError.Message
			}

			issues = append(issues, issue)
			logger.Info("Found stuck VolumeAttachment",
				"va", issue.VAName,
				"pv", issue.PVName,
				"node", issue.NodeName,
				"stuckDuration", stuckDuration,
				"detachError", issue.DetachError)
		}
	}

	logger.V(2).Info("Diagnosed stuck VolumeAttachments", "count", len(issues))
	return issues, nil
}

// ForceDetachVolume forces cleanup of a stuck VolumeAttachment by removing finalizers
// ONLY call this after verifying the volume is truly detached at vSphere level
// This is a last-resort safety mechanism for when CSI driver has lost internal state
func (m *VolumeAttachmentManager) ForceDetachVolume(ctx context.Context, pvName string) error {
	logger := klog.FromContext(ctx)

	// Get the VolumeAttachment for this PV
	va, err := m.GetVolumeAttachmentForPV(ctx, pvName)
	if err != nil {
		return fmt.Errorf("failed to get VolumeAttachment: %w", err)
	}

	if va == nil {
		logger.Info("No VolumeAttachment found - already detached", "pv", pvName)
		return nil
	}

	// Log the force-detach action prominently for audit trail
	logger.Info("========================================")
	logger.Info("FORCE DETACHING VOLUME")
	logger.Info("========================================")
	logger.Info("Force-detaching stuck VolumeAttachment after vSphere-level verification",
		"pv", pvName,
		"volumeAttachment", va.Name,
		"node", va.Spec.NodeName,
		"deletionTimestamp", va.DeletionTimestamp)

	// Remove all finalizers to allow immediate deletion
	va.Finalizers = []string{}

	// Update the VolumeAttachment
	_, err = m.kubeClient.StorageV1().VolumeAttachments().Update(ctx, va, metav1.UpdateOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("VolumeAttachment already deleted during force-detach", "pv", pvName)
			return nil
		}
		return fmt.Errorf("failed to remove finalizers from VolumeAttachment: %w", err)
	}

	logger.Info("Removed finalizers from VolumeAttachment, waiting for deletion", "va", va.Name)

	// Wait for deletion to complete (should be immediate after finalizer removal)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		currentVA, err := m.GetVolumeAttachment(ctx, va.Name)
		if err != nil {
			return fmt.Errorf("failed to check VolumeAttachment deletion status: %w", err)
		}
		if currentVA == nil {
			logger.Info("VolumeAttachment successfully deleted after force-detach", "pv", pvName)
			logger.Info("========================================")
			logger.Info("FORCE DETACH COMPLETED")
			logger.Info("========================================")
			return nil
		}

		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("timeout waiting for VolumeAttachment deletion after finalizer removal")
}
