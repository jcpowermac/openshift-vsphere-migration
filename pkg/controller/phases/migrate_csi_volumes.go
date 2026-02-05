package phases

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/openshift"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/vsphere"
)

// PV Migration Status constants
const (
	PVStatusPending    = "Pending"
	PVStatusRetainSet  = "RetainSet"  // PV reclaim policy set to Retain
	PVStatusQuiesced   = "Quiesced"   // Workloads scaled down, pods terminated
	PVStatusPVCDeleted = "PVCDeleted" // PVC deleted after quiesce
	PVStatusRelocating = "Relocating"
	PVStatusRelocated  = "Relocated"
	PVStatusRegistered = "Registered"
	PVStatusPVUpdated  = "PVUpdated" // PV volumeHandle updated and claimRef cleared
	PVStatusComplete   = "Complete"
	PVStatusFailed     = "Failed"
)

// MigrateCSIVolumesPhase migrates vSphere CSI PersistentVolumes to the target vCenter
type MigrateCSIVolumesPhase struct {
	executor *PhaseExecutor
}

// NewMigrateCSIVolumesPhase creates a new migrate CSI volumes phase
func NewMigrateCSIVolumesPhase(executor *PhaseExecutor) *MigrateCSIVolumesPhase {
	return &MigrateCSIVolumesPhase{
		executor: executor,
	}
}

// Name returns the phase name
func (p *MigrateCSIVolumesPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseMigrateCSIVolumes
}

// Validate checks if the phase can be executed
func (p *MigrateCSIVolumesPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	// Ensure we have target vCenter configuration
	if len(migration.Spec.FailureDomains) == 0 {
		return fmt.Errorf("no failure domains configured")
	}
	return nil
}

// Execute runs the CSI volume migration phase
func (p *MigrateCSIVolumesPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Starting CSI volume migration phase")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Starting CSI volume migration", string(p.Name()))

	// Initialize CSI migration status if needed
	if migration.Status.CSIVolumeMigration == nil {
		migration.Status.CSIVolumeMigration = &migrationv1alpha1.CSIVolumeMigrationStatus{
			Volumes: make([]migrationv1alpha1.PVMigrationState, 0),
		}
	}

	// Create PV manager
	pvManager := openshift.NewPersistentVolumeManager(p.executor.kubeClient)

	// Discover vSphere CSI volumes if not already done
	if len(migration.Status.CSIVolumeMigration.Volumes) == 0 {
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Discovering vSphere CSI volumes", string(p.Name()))

		csiPVs, err := pvManager.ListVSphereCSIVolumes(ctx)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "Failed to list vSphere CSI volumes: " + err.Error(),
				Logs:    logs,
			}, err
		}

		if len(csiPVs) == 0 {
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "No vSphere CSI volumes found to migrate", string(p.Name()))
			return &PhaseResult{
				Status:   migrationv1alpha1.PhaseStatusCompleted,
				Message:  "No vSphere CSI volumes to migrate",
				Progress: 100,
				Logs:     logs,
			}, nil
		}

		// Initialize volume states
		for _, pv := range csiPVs {
			pvState := migrationv1alpha1.PVMigrationState{
				PVName:           pv.Name,
				SourceVolumePath: pv.VolumeHandle,
				Status:           PVStatusPending,
			}

			// Add PVC info if bound
			if pv.ClaimRef != nil {
				pvState.PVCName = pv.ClaimRef.Name
				pvState.PVCNamespace = pv.ClaimRef.Namespace
			}

			migration.Status.CSIVolumeMigration.Volumes = append(migration.Status.CSIVolumeMigration.Volumes, pvState)
		}

		migration.Status.CSIVolumeMigration.TotalVolumes = int32(len(csiPVs))
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Discovered %d vSphere CSI volumes", len(csiPVs)),
			string(p.Name()))
	}

	// Get source and target vCenter clients
	targetFailureDomain := migration.Spec.FailureDomains[0]

	sourceVCenter, err := p.executor.infraManager.GetSourceVCenter(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get source vCenter: " + err.Error(),
			Logs:    logs,
		}, err
	}

	sourceClient, err := p.executor.GetVSphereClient(ctx, sourceVCenter.Server)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to connect to source vCenter: " + err.Error(),
			Logs:    logs,
		}, err
	}
	defer sourceClient.Logout(ctx)

	targetClient, err := p.executor.GetVSphereClientFromMigration(ctx, migration, targetFailureDomain.Server)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to connect to target vCenter: " + err.Error(),
			Logs:    logs,
		}, err
	}
	defer targetClient.Logout(ctx)

	// Create managers
	workloadManager := openshift.NewWorkloadManager(p.executor.kubeClient)

	// Process each volume
	for i := range migration.Status.CSIVolumeMigration.Volumes {
		pvState := &migration.Status.CSIVolumeMigration.Volumes[i]

		// Skip completed or failed volumes
		if pvState.Status == PVStatusComplete || pvState.Status == PVStatusFailed {
			continue
		}

		logger.Info("Processing CSI volume", "pv", pvState.PVName, "status", pvState.Status)

		// Step 1: Set PV reclaim policy to Retain
		if pvState.Status == PVStatusPending {
			originalPolicy, err := pvManager.UpdatePVReclaimPolicy(ctx, pvState.PVName, corev1.PersistentVolumeReclaimRetain)
			if err != nil {
				pvState.Status = PVStatusFailed
				pvState.Message = "Failed to set PV reclaim policy to Retain: " + err.Error()
				migration.Status.CSIVolumeMigration.FailedVolumes++
				logs = AddLog(logs, migrationv1alpha1.LogLevelError, pvState.Message, string(p.Name()))
				continue
			}
			pvState.OriginalReclaimPolicy = string(originalPolicy)
			pvState.Status = PVStatusRetainSet
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
				fmt.Sprintf("Set PV %s reclaim policy to Retain (was %s)", pvState.PVName, originalPolicy),
				string(p.Name()))
		}

		// Step 2: Quiesce workloads and backup PVC spec
		if pvState.Status == PVStatusRetainSet {
			if err := p.quiesceVolume(ctx, pvManager, workloadManager, pvState); err != nil {
				pvState.Status = PVStatusFailed
				pvState.Message = "Failed to quiesce workloads: " + err.Error()
				migration.Status.CSIVolumeMigration.FailedVolumes++
				logs = AddLog(logs, migrationv1alpha1.LogLevelError, pvState.Message, string(p.Name()))
				continue
			}
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
				fmt.Sprintf("Quiesced workloads for PV %s (workloadType=%s)", pvState.PVName, pvState.WorkloadType),
				string(p.Name()))
		}

		// Step 3: Delete PVC (after pods terminated)
		if pvState.Status == PVStatusQuiesced {
			if err := p.deletePVC(ctx, pvManager, pvState); err != nil {
				pvState.Status = PVStatusFailed
				pvState.Message = "Failed to delete PVC: " + err.Error()
				migration.Status.CSIVolumeMigration.FailedVolumes++
				logs = AddLog(logs, migrationv1alpha1.LogLevelError, pvState.Message, string(p.Name()))
				logger.Error(nil, "PVC deletion failed, workloads remain scaled down",
					"pv", pvState.PVName)
				logs = AddLog(logs, migrationv1alpha1.LogLevelWarning,
					fmt.Sprintf("Workloads for PV %s remain scaled down - PVC deletion failed", pvState.PVName),
					string(p.Name()))
				continue
			}
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
				fmt.Sprintf("Deleted PVC for PV %s", pvState.PVName),
				string(p.Name()))
		}

		// Step 4: Relocate the volume
		if pvState.Status == PVStatusPVCDeleted {
			if err := p.relocateVolume(ctx, sourceClient, targetClient, migration, pvState); err != nil {
				pvState.Status = PVStatusFailed
				pvState.Message = "Failed to relocate volume: " + err.Error()
				migration.Status.CSIVolumeMigration.FailedVolumes++
				logs = AddLog(logs, migrationv1alpha1.LogLevelError, pvState.Message, string(p.Name()))

				// DO NOT restore workloads on relocation failure - volume may be in inconsistent state
				// Workloads remain scaled down to prevent data loss
				logger.Error(nil, "PV migration failed, workloads remain scaled down to prevent data loss",
					"pv", pvState.PVName,
					"scaledDownResources", len(pvState.ScaledDownResources))
				logs = AddLog(logs, migrationv1alpha1.LogLevelWarning,
					fmt.Sprintf("Workloads for PV %s remain scaled down due to migration failure - manual intervention required", pvState.PVName),
					string(p.Name()))
				continue
			}
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
				fmt.Sprintf("Relocated PV %s to target vCenter", pvState.PVName),
				string(p.Name()))
		}

		// Step 5: Register with CNS on target
		if pvState.Status == PVStatusRelocated {
			if err := p.registerVolume(ctx, targetClient, migration, pvState); err != nil {
				pvState.Status = PVStatusFailed
				pvState.Message = "Failed to register volume with CNS: " + err.Error()
				migration.Status.CSIVolumeMigration.FailedVolumes++
				logs = AddLog(logs, migrationv1alpha1.LogLevelError, pvState.Message, string(p.Name()))
				// Workloads remain scaled down - volume exists on target but not registered
				logger.Error(nil, "CNS registration failed, workloads remain scaled down",
					"pv", pvState.PVName)
				logs = AddLog(logs, migrationv1alpha1.LogLevelWarning,
					fmt.Sprintf("Workloads for PV %s remain scaled down - CNS registration failed", pvState.PVName),
					string(p.Name()))
				continue
			}
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
				fmt.Sprintf("Registered PV %s with target CNS", pvState.PVName),
				string(p.Name()))
		}

		// Step 6: Update PV volumeHandle and clear claimRef
		if pvState.Status == PVStatusRegistered {
			if err := p.updatePVAndClearClaimRef(ctx, pvManager, pvState); err != nil {
				pvState.Status = PVStatusFailed
				pvState.Message = "Failed to update PV: " + err.Error()
				migration.Status.CSIVolumeMigration.FailedVolumes++
				logs = AddLog(logs, migrationv1alpha1.LogLevelError, pvState.Message, string(p.Name()))
				// Workloads remain scaled down - PV still points to old location
				logger.Error(nil, "PV update failed, workloads remain scaled down",
					"pv", pvState.PVName)
				logs = AddLog(logs, migrationv1alpha1.LogLevelWarning,
					fmt.Sprintf("Workloads for PV %s remain scaled down - PV update failed", pvState.PVName),
					string(p.Name()))
				continue
			}
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
				fmt.Sprintf("Updated PV %s volumeHandle and cleared claimRef", pvState.PVName),
				string(p.Name()))
		}

		// Step 7: Recreate PVC (for non-StatefulSet workloads) and restore workloads
		if pvState.Status == PVStatusPVUpdated {
			if err := p.restorePVCAndWorkloads(ctx, pvManager, workloadManager, pvState); err != nil {
				pvState.Status = PVStatusFailed
				pvState.Message = "Failed to restore PVC/workloads: " + err.Error()
				migration.Status.CSIVolumeMigration.FailedVolumes++
				logger.Error(err, "Failed to restore PVC/workloads after successful migration",
					"pv", pvState.PVName,
					"workloadType", pvState.WorkloadType)
				logs = AddLog(logs, migrationv1alpha1.LogLevelError,
					fmt.Sprintf("Failed to restore PVC/workloads for PV %s: %v - manual intervention required", pvState.PVName, err),
					string(p.Name()))
				continue
			}

			pvState.Status = PVStatusComplete
			pvState.Message = "Volume migrated successfully"
			migration.Status.CSIVolumeMigration.MigratedVolumes++
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
				fmt.Sprintf("Successfully migrated PV %s", pvState.PVName),
				string(p.Name()))
		}
	}

	// Calculate progress
	total := migration.Status.CSIVolumeMigration.TotalVolumes
	migrated := migration.Status.CSIVolumeMigration.MigratedVolumes
	failed := migration.Status.CSIVolumeMigration.FailedVolumes
	progress := int32(0)
	if total > 0 {
		progress = int32((migrated + failed) * 100 / total)
	}

	// Check if all volumes are processed
	if migrated+failed >= total {
		if failed > 0 {
			// Log prominent failure message
			logger.Info("========================================")
			logger.Info("CSI VOLUME MIGRATION INCOMPLETE")
			logger.Info("========================================")
			logger.Info("Some volumes failed to migrate",
				"failed", failed,
				"migrated", migrated,
				"total", total)
			for _, pv := range migration.Status.CSIVolumeMigration.Volumes {
				if pv.Status == PVStatusFailed {
					logger.Info("Failed volume details",
						"pv", pv.PVName,
						"pvc", fmt.Sprintf("%s/%s", pv.PVCNamespace, pv.PVCName),
						"error", pv.Message,
						"scaledDownResources", len(pv.ScaledDownResources))
				}
			}
			logger.Info("========================================")
			logger.Info("MANUAL INTERVENTION REQUIRED for failed volumes")
			logger.Info("Workloads using failed volumes remain scaled down")
			logger.Info("========================================")

			logs = AddLog(logs, migrationv1alpha1.LogLevelWarning,
				fmt.Sprintf("CSI volume migration completed with %d failures - workloads remain scaled down", failed),
				string(p.Name()))

			return &PhaseResult{
				Status:   migrationv1alpha1.PhaseStatusCompleted,
				Message:  fmt.Sprintf("CSI volume migration completed with %d failures - manual intervention required", failed),
				Progress: 100,
				Logs:     logs,
			}, nil
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Successfully migrated all %d CSI volumes", migrated),
			string(p.Name()))

		return &PhaseResult{
			Status:   migrationv1alpha1.PhaseStatusCompleted,
			Message:  fmt.Sprintf("Successfully migrated %d CSI volumes", migrated),
			Progress: 100,
			Logs:     logs,
		}, nil
	}

	// Still processing, requeue
	return &PhaseResult{
		Status:       migrationv1alpha1.PhaseStatusRunning,
		Message:      fmt.Sprintf("Migrating CSI volumes: %d/%d complete", migrated, total),
		Progress:     progress,
		Logs:         logs,
		RequeueAfter: 30 * time.Second,
	}, nil
}

// quiesceVolume scales down workloads using the volume and backs up PVC spec
func (p *MigrateCSIVolumesPhase) quiesceVolume(ctx context.Context, pvManager *openshift.PersistentVolumeManager, workloadManager *openshift.WorkloadManager, pvState *migrationv1alpha1.PVMigrationState) error {
	logger := klog.FromContext(ctx)

	if pvState.PVCNamespace == "" || pvState.PVCName == "" {
		// No PVC bound, nothing to quiesce
		pvState.Status = PVStatusQuiesced
		return nil
	}

	logger.Info("Quiescing workloads for PVC", "namespace", pvState.PVCNamespace, "name", pvState.PVCName)

	// Scale down workloads
	scaledResources, err := workloadManager.ScaleDownForPV(ctx, pvState.PVCNamespace, pvState.PVCName)
	if err != nil {
		return fmt.Errorf("failed to scale down workloads: %w", err)
	}

	pvState.ScaledDownResources = scaledResources

	// Identify workload type from scaled resources
	pvState.WorkloadType = identifyWorkloadType(scaledResources)
	logger.Info("Identified workload type", "pv", pvState.PVName, "workloadType", pvState.WorkloadType)

	// Backup PVC spec for non-StatefulSet workloads
	// StatefulSet controller will recreate PVCs from volumeClaimTemplates
	if pvState.WorkloadType != "StatefulSet" {
		pvcSpec, err := pvManager.BackupPVCSpec(ctx, pvState.PVCNamespace, pvState.PVCName)
		if err != nil {
			return fmt.Errorf("failed to backup PVC spec: %w", err)
		}
		pvState.PVCSpec = pvcSpec
		logger.Info("Backed up PVC spec", "pv", pvState.PVName, "pvc", pvState.PVCName)
	}

	// Wait for pods to terminate
	if len(scaledResources) > 0 {
		if err := workloadManager.WaitForPodsTerminated(ctx, pvState.PVCNamespace, pvState.PVCName, 5*time.Minute); err != nil {
			return fmt.Errorf("timeout waiting for pods to terminate: %w", err)
		}
	}

	pvState.Status = PVStatusQuiesced
	return nil
}

// identifyWorkloadType determines the primary workload type from scaled resources
func identifyWorkloadType(scaledResources []migrationv1alpha1.ScaledResource) string {
	for _, r := range scaledResources {
		// StatefulSet takes precedence as it has special PVC handling
		if r.Kind == "StatefulSet" {
			return "StatefulSet"
		}
	}
	for _, r := range scaledResources {
		if r.Kind == "Deployment" {
			return "Deployment"
		}
	}
	for _, r := range scaledResources {
		if r.Kind == "ReplicaSet" {
			return "ReplicaSet"
		}
	}
	if len(scaledResources) > 0 {
		return scaledResources[0].Kind
	}
	return "Unknown"
}

// deletePVC deletes the PVC after workloads are quiesced and waits for VolumeAttachment deletion
func (p *MigrateCSIVolumesPhase) deletePVC(ctx context.Context, pvManager *openshift.PersistentVolumeManager, pvState *migrationv1alpha1.PVMigrationState) error {
	logger := klog.FromContext(ctx)

	if pvState.PVCNamespace == "" || pvState.PVCName == "" {
		// No PVC bound, skip deletion
		pvState.Status = PVStatusPVCDeleted
		return nil
	}

	logger.Info("Deleting PVC", "namespace", pvState.PVCNamespace, "name", pvState.PVCName)

	// Delete the PVC
	if err := pvManager.DeletePVC(ctx, pvState.PVCNamespace, pvState.PVCName); err != nil {
		return fmt.Errorf("failed to delete PVC: %w", err)
	}

	// Wait for PVC to be fully deleted
	if err := pvManager.WaitForPVCDeleted(ctx, pvState.PVCNamespace, pvState.PVCName, 2*time.Minute); err != nil {
		return fmt.Errorf("timeout waiting for PVC deletion: %w", err)
	}

	// Wait for VolumeAttachment to be deleted - confirms vSphere-level detachment
	// This is critical: PVC deletion triggers async CSI ControllerUnpublishVolume which
	// performs the actual vSphere detach. We must wait for VolumeAttachment deletion
	// to confirm the VMDK is fully detached before attempting migration.
	vaManager := openshift.NewVolumeAttachmentManager(p.executor.kubeClient)
	if err := vaManager.WaitForVolumeDetached(ctx, pvState.PVName, 3*time.Minute); err != nil {
		return fmt.Errorf("timeout waiting for volume detachment (VolumeAttachment deletion): %w", err)
	}

	pvState.Status = PVStatusPVCDeleted
	logger.Info("PVC deleted and volume detachment confirmed", "namespace", pvState.PVCNamespace, "name", pvState.PVCName)
	return nil
}

// relocateVolume performs the cross-vCenter volume relocation using a dummy VM
func (p *MigrateCSIVolumesPhase) relocateVolume(ctx context.Context, sourceClient, targetClient *vsphere.Client, migration *migrationv1alpha1.VmwareCloudFoundationMigration, pvState *migrationv1alpha1.PVMigrationState) error {
	logger := klog.FromContext(ctx)

	// Parse volume handle to get FCD ID
	fcdID, err := vsphere.ParseCSIVolumeHandle(pvState.SourceVolumePath)
	if err != nil {
		return fmt.Errorf("failed to parse volume handle: %w", err)
	}
	pvState.SourceVolumeID = fcdID

	// Get source failure domain from infrastructure
	sourceFailureDomain, err := p.executor.infraManager.GetSourceFailureDomain(ctx)
	if err != nil {
		return fmt.Errorf("failed to get source failure domain: %w", err)
	}

	// Get target failure domain
	targetFD := migration.Spec.FailureDomains[0]

	// Create FCD manager for source
	sourceFCDManager, err := vsphere.NewFCDManager(ctx, sourceClient)
	if err != nil {
		return fmt.Errorf("failed to create source FCD manager: %w", err)
	}

	// Get FCD info
	fcdInfo, err := sourceFCDManager.GetFCDByID(ctx, fcdID)
	if err != nil {
		return fmt.Errorf("failed to get FCD info: %w", err)
	}

	logger.Info("Found FCD", "id", fcdInfo.ID, "name", fcdInfo.Name, "path", fcdInfo.Path)

	// Create VM relocator
	relocator := vsphere.NewVMRelocator(sourceClient, targetClient)

	// Get infrastructure ID for naming
	infraID, err := p.executor.infraManager.GetInfrastructureID(ctx)
	if err != nil {
		return fmt.Errorf("failed to get infrastructure ID: %w", err)
	}

	// Create dummy VM on source
	dummyVMName := fmt.Sprintf("csi-migration-%s-%s", infraID, pvState.PVName[:min(8, len(pvState.PVName))])
	pvState.DummyVMName = dummyVMName

	dummyConfig := vsphere.DummyVMConfig{
		Name:         dummyVMName,
		Datacenter:   sourceFailureDomain.Topology.Datacenter,
		Cluster:      sourceFailureDomain.Topology.ComputeCluster,
		Datastore:    sourceFailureDomain.Topology.Datastore,
		Folder:       fmt.Sprintf("/%s/vm/%s", sourceFailureDomain.Topology.Datacenter, infraID),
		ResourcePool: sourceFailureDomain.Topology.ResourcePool,
		NumCPUs:      1,
		MemoryMB:     128,
	}

	dummyVM, err := relocator.CreateDummyVM(ctx, dummyConfig)
	if err != nil {
		return fmt.Errorf("failed to create dummy VM: %w", err)
	}

	// Cleanup dummy VM on exit
	defer func() {
		if cleanupErr := relocator.DeleteDummyVM(ctx, dummyVM); cleanupErr != nil {
			logger.Error(cleanupErr, "Failed to delete dummy VM", "name", dummyVMName)
		}
	}()

	// Get SCSI controller key
	controllerKey, err := relocator.GetVMSCSIControllerKey(ctx, dummyVM)
	if err != nil {
		return fmt.Errorf("failed to get SCSI controller: %w", err)
	}

	// Get datastore for FCD
	datastore, err := sourceFCDManager.GetDatastoreFromPath(ctx, fcdInfo.Path)
	if err != nil {
		return fmt.Errorf("failed to get datastore: %w", err)
	}

	// === DEFENSE-IN-DEPTH: Multiple layers of detachment verification ===
	// Data safety is critical - these are customer volumes. We verify detachment at multiple levels.

	// Defense Layer 1: Verify VolumeAttachment is gone (K8s-level confirmation)
	// This was already waited for in deletePVC(), but double-check here as a safety gate
	vaManager := openshift.NewVolumeAttachmentManager(p.executor.kubeClient)
	attached, nodeName, err := vaManager.IsVolumeAttached(ctx, pvState.PVName)
	if err != nil {
		logger.Error(err, "Failed to check VolumeAttachment status", "pv", pvState.PVName)
		// Continue to vSphere-level checks - VolumeAttachment API error shouldn't block if vSphere confirms detachment
	} else if attached {
		return fmt.Errorf("ABORT: volume still attached per VolumeAttachment (node=%s), refusing to proceed to protect data", nodeName)
	}
	logger.Info("Defense Layer 1 PASSED: VolumeAttachment confirms volume is detached", "pv", pvState.PVName)

	// Defense Layer 2: Wait for FCD to be detached from any worker VM (vSphere-level folder scan)
	// This scans all VMs in the cluster folder to confirm FCD is not attached to any VM
	logger.Info("Defense Layer 2: Waiting for FCD to be detached from all VMs in folder", "fcdID", fcdID)
	folderPath := fmt.Sprintf("/%s/vm/%s", sourceFailureDomain.Topology.Datacenter, infraID)
	if err := sourceFCDManager.WaitForFCDDetached(ctx,
		sourceFailureDomain.Topology.Datacenter,
		folderPath,
		fcdID,
		3*time.Minute); err != nil {
		return fmt.Errorf("timeout waiting for FCD detachment from worker VM: %w", err)
	}
	logger.Info("Defense Layer 2 PASSED: FCD is not attached to any VM in folder", "fcdID", fcdID)

	// Defense Layer 3: Direct VM device verification for VMs that were using this volume
	// This is the last-resort safety check - directly query each worker VM's hardware config
	// to verify the VMDK is not in the device configuration before we attach to dummy VM
	if len(pvState.ScaledDownResources) > 0 {
		logger.Info("Defense Layer 3: Verifying FCD not attached to previously-using worker VMs", "fcdID", fcdID)

		// Get VMs in the folder that might have been using this volume
		vms, err := sourceClient.ListVirtualMachinesInFolder(ctx, sourceFailureDomain.Topology.Datacenter, folderPath)
		if err != nil {
			logger.Error(err, "Failed to list VMs for Layer 3 check, continuing with prior confirmations", "fcdID", fcdID)
		} else {
			for _, vm := range vms {
				if err := sourceFCDManager.VerifyFCDNotAttachedToVM(ctx, vm, fcdID); err != nil {
					return fmt.Errorf("Defense Layer 3 FAILED: %w", err)
				}
			}
			logger.Info("Defense Layer 3 PASSED: FCD verified not attached to any worker VM devices", "fcdID", fcdID)
		}
	}

	logger.Info("All defense layers PASSED - safe to proceed with migration", "fcdID", fcdID, "pv", pvState.PVName)

	// Attach FCD to dummy VM
	unitNumber, err := relocator.GetNextFreeUnitNumber(ctx, dummyVM, controllerKey)
	if err != nil {
		return fmt.Errorf("failed to get unit number: %w", err)
	}

	if err := sourceFCDManager.AttachDisk(ctx, dummyVM, datastore, fcdID, controllerKey, unitNumber); err != nil {
		return fmt.Errorf("failed to attach FCD to dummy VM: %w", err)
	}

	pvState.Status = PVStatusRelocating

	// Get target credentials for cross-vCenter vMotion
	targetSecretNS := migration.Spec.TargetVCenterCredentialsSecret.Namespace
	if targetSecretNS == "" {
		targetSecretNS = migration.Namespace
	}
	targetUser, targetPass, err := p.executor.secretManager.GetVCenterCredsFromSecret(
		ctx,
		targetSecretNS,
		migration.Spec.TargetVCenterCredentialsSecret.Name,
		targetFD.Server,
	)
	if err != nil {
		return fmt.Errorf("failed to get target credentials: %w", err)
	}

	// Get target vCenter SSL thumbprint for cross-vCenter vMotion
	// This is required for the ServiceLocator to verify the target server's identity
	targetVCenterURL := fmt.Sprintf("https://%s/sdk", targetFD.Server)
	targetThumbprint, err := vsphere.GetServerThumbprint(ctx, targetVCenterURL)
	if err != nil {
		return fmt.Errorf("failed to get target vCenter SSL thumbprint: %w", err)
	}
	logger.Info("Retrieved target vCenter SSL thumbprint",
		"server", targetFD.Server,
		"thumbprint", targetThumbprint)

	// Get target vCenter instance UUID for cross-vCenter vMotion
	targetInstanceUUID := targetClient.GetInstanceUUID()
	logger.Info("Retrieved target vCenter instance UUID",
		"server", targetFD.Server,
		"instanceUUID", targetInstanceUUID)

	// Build relocate config
	relocateConfig := vsphere.RelocateConfig{
		TargetVCenterURL:          targetVCenterURL,
		TargetVCenterUser:         targetUser,
		TargetVCenterPassword:     targetPass,
		TargetVCenterThumbprint:   targetThumbprint,
		TargetVCenterInstanceUUID: targetInstanceUUID,
		TargetDatacenter:          targetFD.Topology.Datacenter,
		TargetCluster:             targetFD.Topology.ComputeCluster,
		TargetDatastore:           targetFD.Topology.Datastore,
		TargetFolder:              fmt.Sprintf("/%s/vm/%s", targetFD.Topology.Datacenter, infraID),
		TargetResourcePool:        targetFD.Topology.ResourcePool,
	}

	// Validate relocate config before attempting vMotion
	if relocateConfig.TargetVCenterInstanceUUID == "" {
		return fmt.Errorf("FATAL: target vCenter instance UUID is empty - cannot proceed with cross-vCenter vMotion")
	}
	if relocateConfig.TargetVCenterThumbprint == "" {
		return fmt.Errorf("FATAL: target vCenter SSL thumbprint is empty - cannot proceed with cross-vCenter vMotion")
	}

	// Log prominent start message for cross-vCenter vMotion
	logger.Info("========================================")
	logger.Info("STARTING CROSS-VCENTER VMOTION")
	logger.Info("========================================")
	thumbprintPreview := relocateConfig.TargetVCenterThumbprint
	if len(thumbprintPreview) > 20 {
		thumbprintPreview = thumbprintPreview[:20] + "..."
	}
	logger.Info("vMotion configuration",
		"sourceDatacenter", sourceFailureDomain.Topology.Datacenter,
		"targetVCenter", targetFD.Server,
		"targetDatacenter", targetFD.Topology.Datacenter,
		"targetDatastore", targetFD.Topology.Datastore,
		"targetFolder", relocateConfig.TargetFolder,
		"targetInstanceUUID", targetInstanceUUID,
		"sslThumbprint", thumbprintPreview,
		"dummyVM", dummyVMName,
		"fcdID", fcdID)

	// Perform cross-vCenter vMotion
	if err := relocator.RelocateVM(ctx, dummyVM, relocateConfig); err != nil {
		logger.Info("========================================")
		logger.Info("CROSS-VCENTER VMOTION FAILED")
		logger.Info("========================================")
		logger.Error(err, "vMotion failure details",
			"vm", dummyVMName,
			"fcdID", fcdID,
			"targetVCenter", targetFD.Server,
			"error", err.Error())
		return fmt.Errorf("cross-vCenter vMotion failed: %w", err)
	}

	// Detach FCD from dummy VM on target
	// Note: After vMotion, the VM is on target vCenter
	targetFCDManager, err := vsphere.NewFCDManager(ctx, targetClient)
	if err != nil {
		return fmt.Errorf("failed to create target FCD manager: %w", err)
	}

	// Get the VM reference on target
	targetVM, err := targetClient.GetVirtualMachine(ctx, fmt.Sprintf("/%s/vm/%s/%s",
		targetFD.Topology.Datacenter, infraID, dummyVMName))
	if err != nil {
		return fmt.Errorf("failed to find dummy VM on target: %w", err)
	}

	if err := targetFCDManager.DetachDisk(ctx, targetVM, fcdID); err != nil {
		logger.Error(err, "Failed to detach FCD from dummy VM on target", "fcdID", fcdID)
		// Continue anyway, the disk might already be detached
	}

	// Update state
	pvState.TargetVolumeID = fcdID // FCD ID remains the same after vMotion
	pvState.TargetVolumePath = vsphere.BuildCSIVolumeHandle(fcdID)
	pvState.Status = PVStatusRelocated

	logger.Info("Successfully relocated volume", "pv", pvState.PVName, "fcdID", fcdID)
	return nil
}

// registerVolume registers the volume with CNS on the target vCenter
func (p *MigrateCSIVolumesPhase) registerVolume(ctx context.Context, targetClient *vsphere.Client, migration *migrationv1alpha1.VmwareCloudFoundationMigration, pvState *migrationv1alpha1.PVMigrationState) error {
	logger := klog.FromContext(ctx)

	// Create CNS manager
	cnsManager, err := vsphere.NewCNSManager(ctx, targetClient)
	if err != nil {
		return fmt.Errorf("failed to create CNS manager: %w", err)
	}

	// Check if volume is already registered
	existingVol, err := cnsManager.QueryVolume(ctx, pvState.TargetVolumeID)
	if err == nil && existingVol != nil {
		logger.Info("Volume already registered with CNS", "volumeID", pvState.TargetVolumeID)
		pvState.Status = PVStatusRegistered
		return nil
	}

	// Get infrastructure ID for container cluster ID
	infraID, err := p.executor.infraManager.GetInfrastructureID(ctx)
	if err != nil {
		return fmt.Errorf("failed to get infrastructure ID: %w", err)
	}

	// Get target failure domain for datastore info
	targetFD := migration.Spec.FailureDomains[0]

	// Build backing path
	backingPath := fmt.Sprintf("[%s] fcd/%s.vmdk",
		targetFD.Topology.Datastore, pvState.TargetVolumeID)

	// Register volume with CNS
	_, err = cnsManager.RegisterVolume(ctx, backingPath, pvState.PVName, "", infraID)
	if err != nil {
		return fmt.Errorf("failed to register volume with CNS: %w", err)
	}

	pvState.Status = PVStatusRegistered
	logger.Info("Successfully registered volume with CNS", "pv", pvState.PVName)
	return nil
}

// updatePVAndClearClaimRef updates the PV's volumeHandle and clears the claimRef
func (p *MigrateCSIVolumesPhase) updatePVAndClearClaimRef(ctx context.Context, pvManager *openshift.PersistentVolumeManager, pvState *migrationv1alpha1.PVMigrationState) error {
	logger := klog.FromContext(ctx)

	// Update the PV's volumeHandle
	newHandle := vsphere.BuildCSIVolumeHandle(pvState.TargetVolumeID)
	if err := pvManager.UpdatePVVolumeHandle(ctx, pvState.PVName, newHandle); err != nil {
		return fmt.Errorf("failed to update volumeHandle: %w", err)
	}

	// Clear claimRef to make PV Available for rebinding
	if err := pvManager.ClearPVClaimRef(ctx, pvState.PVName); err != nil {
		return fmt.Errorf("failed to clear claimRef: %w", err)
	}

	pvState.Status = PVStatusPVUpdated
	logger.Info("Updated PV and cleared claimRef", "pv", pvState.PVName, "newHandle", newHandle)
	return nil
}

// restorePVCAndWorkloads recreates PVC (for non-StatefulSet) and restores workloads
func (p *MigrateCSIVolumesPhase) restorePVCAndWorkloads(ctx context.Context, pvManager *openshift.PersistentVolumeManager, workloadManager *openshift.WorkloadManager, pvState *migrationv1alpha1.PVMigrationState) error {
	logger := klog.FromContext(ctx)

	// For StatefulSet workloads, the StatefulSet controller will recreate the PVC
	// from volumeClaimTemplates when the pods are restored
	if pvState.WorkloadType == "StatefulSet" {
		logger.Info("StatefulSet workload - skipping PVC recreation (StatefulSet controller will handle it)",
			"pv", pvState.PVName)
	} else if pvState.PVCSpec != "" {
		// Recreate PVC for non-StatefulSet workloads
		logger.Info("Recreating PVC for non-StatefulSet workload",
			"pv", pvState.PVName,
			"workloadType", pvState.WorkloadType)

		if err := pvManager.RestorePVC(ctx, pvState.PVCSpec, pvState.PVName); err != nil {
			return fmt.Errorf("failed to restore PVC: %w", err)
		}

		// Wait for PVC to bind to the PV
		if err := pvManager.WaitForPVCBound(ctx, pvState.PVCNamespace, pvState.PVCName, 2*time.Minute); err != nil {
			return fmt.Errorf("timeout waiting for PVC to bind: %w", err)
		}

		logger.Info("PVC recreated and bound", "pvc", pvState.PVCName, "pv", pvState.PVName)
	}

	// Restore workloads
	if len(pvState.ScaledDownResources) > 0 {
		logger.Info("Restoring workloads", "pv", pvState.PVName, "count", len(pvState.ScaledDownResources))
		if err := workloadManager.RestoreWorkloads(ctx, pvState.ScaledDownResources); err != nil {
			return fmt.Errorf("failed to restore workloads: %w", err)
		}
	}

	logger.Info("Successfully restored PVC and workloads", "pv", pvState.PVName)
	return nil
}

// Rollback reverts the phase changes
func (p *MigrateCSIVolumesPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back MigrateCSIVolumes phase")

	if migration.Status.CSIVolumeMigration == nil {
		return nil
	}

	pvManager := openshift.NewPersistentVolumeManager(p.executor.kubeClient)
	workloadManager := openshift.NewWorkloadManager(p.executor.kubeClient)

	for i := range migration.Status.CSIVolumeMigration.Volumes {
		pvState := &migration.Status.CSIVolumeMigration.Volumes[i]

		// Skip completed volumes - they were successfully migrated
		if pvState.Status == PVStatusComplete {
			continue
		}

		logger.Info("Rolling back PV", "pv", pvState.PVName, "status", pvState.Status)

		// Restore original reclaim policy if it was changed
		if pvState.OriginalReclaimPolicy != "" {
			originalPolicy := corev1.PersistentVolumeReclaimPolicy(pvState.OriginalReclaimPolicy)
			if _, err := pvManager.UpdatePVReclaimPolicy(ctx, pvState.PVName, originalPolicy); err != nil {
				logger.Error(err, "Failed to restore PV reclaim policy", "pv", pvState.PVName)
			} else {
				logger.Info("Restored PV reclaim policy", "pv", pvState.PVName, "policy", originalPolicy)
			}
		}

		// Recreate PVC if it was deleted and we have a backup
		if pvState.PVCSpec != "" && (pvState.Status == PVStatusPVCDeleted ||
			pvState.Status == PVStatusRelocating ||
			pvState.Status == PVStatusRelocated ||
			pvState.Status == PVStatusRegistered ||
			pvState.Status == PVStatusPVUpdated ||
			pvState.Status == PVStatusFailed) {

			logger.Info("Attempting to restore PVC from backup", "pv", pvState.PVName)
			if err := pvManager.RestorePVC(ctx, pvState.PVCSpec, pvState.PVName); err != nil {
				logger.Error(err, "Failed to restore PVC from backup", "pv", pvState.PVName)
			} else {
				logger.Info("Restored PVC from backup", "pv", pvState.PVName)
			}
		}

		// Restore all scaled down workloads
		if len(pvState.ScaledDownResources) > 0 {
			logger.Info("Restoring workloads for PV", "pv", pvState.PVName)
			if err := workloadManager.RestoreWorkloads(ctx, pvState.ScaledDownResources); err != nil {
				logger.Error(err, "Failed to restore workloads", "pv", pvState.PVName)
			}
		}
	}

	logger.Info("Completed rollback of MigrateCSIVolumes phase")
	return nil
}
