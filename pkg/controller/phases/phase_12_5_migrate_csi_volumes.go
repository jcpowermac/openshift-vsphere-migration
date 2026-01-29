package phases

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/openshift"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/vsphere"
)

// PV Migration Status constants
const (
	PVStatusPending    = "Pending"
	PVStatusQuiesced   = "Quiesced"
	PVStatusRelocating = "Relocating"
	PVStatusRelocated  = "Relocated"
	PVStatusRegistered = "Registered"
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

		// Step 1: Quiesce workloads
		if pvState.Status == PVStatusPending {
			if err := p.quiesceVolume(ctx, workloadManager, pvState); err != nil {
				pvState.Status = PVStatusFailed
				pvState.Message = "Failed to quiesce workloads: " + err.Error()
				migration.Status.CSIVolumeMigration.FailedVolumes++
				logs = AddLog(logs, migrationv1alpha1.LogLevelError, pvState.Message, string(p.Name()))
				continue
			}
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
				fmt.Sprintf("Quiesced workloads for PV %s", pvState.PVName),
				string(p.Name()))
		}

		// Step 2: Relocate the volume
		if pvState.Status == PVStatusQuiesced {
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

		// Step 3: Register with CNS on target
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

		// Step 4: Update PV volumeHandle
		if pvState.Status == PVStatusRegistered {
			if err := p.updatePVHandle(ctx, pvManager, pvState); err != nil {
				pvState.Status = PVStatusFailed
				pvState.Message = "Failed to update PV volumeHandle: " + err.Error()
				migration.Status.CSIVolumeMigration.FailedVolumes++
				logs = AddLog(logs, migrationv1alpha1.LogLevelError, pvState.Message, string(p.Name()))
				// Workloads remain scaled down - PV still points to old location
				logger.Error(nil, "PV volumeHandle update failed, workloads remain scaled down",
					"pv", pvState.PVName)
				logs = AddLog(logs, migrationv1alpha1.LogLevelWarning,
					fmt.Sprintf("Workloads for PV %s remain scaled down - volumeHandle update failed", pvState.PVName),
					string(p.Name()))
				continue
			}
		}

		// Step 5: Restore workloads - ONLY after successful volumeHandle update
		// At this point, the volume is fully migrated and PV points to new location
		if err := workloadManager.RestoreWorkloads(ctx, pvState.ScaledDownResources); err != nil {
			// Workload restoration failure is critical - mark as failed
			pvState.Status = PVStatusFailed
			pvState.Message = "Failed to restore workloads: " + err.Error()
			migration.Status.CSIVolumeMigration.FailedVolumes++
			logger.Error(err, "Failed to restore workloads after successful migration",
				"pv", pvState.PVName,
				"scaledDownResources", len(pvState.ScaledDownResources))
			logs = AddLog(logs, migrationv1alpha1.LogLevelError,
				fmt.Sprintf("Failed to restore workloads for PV %s: %v - manual intervention required", pvState.PVName, err),
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

// quiesceVolume scales down workloads using the volume
func (p *MigrateCSIVolumesPhase) quiesceVolume(ctx context.Context, workloadManager *openshift.WorkloadManager, pvState *migrationv1alpha1.PVMigrationState) error {
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

	// Wait for pods to terminate
	if len(scaledResources) > 0 {
		if err := workloadManager.WaitForPodsTerminated(ctx, pvState.PVCNamespace, pvState.PVCName, 5*time.Minute); err != nil {
			return fmt.Errorf("timeout waiting for pods to terminate: %w", err)
		}
	}

	pvState.Status = PVStatusQuiesced
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

	// Build relocate config
	relocateConfig := vsphere.RelocateConfig{
		TargetVCenterURL:       targetVCenterURL,
		TargetVCenterUser:      targetUser,
		TargetVCenterPassword:  targetPass,
		TargetVCenterThumbprint: targetThumbprint,
		TargetDatacenter:  targetFD.Topology.Datacenter,
		TargetCluster:     targetFD.Topology.ComputeCluster,
		TargetDatastore:   targetFD.Topology.Datastore,
		TargetFolder:      fmt.Sprintf("/%s/vm/%s", targetFD.Topology.Datacenter, infraID),
		TargetResourcePool: targetFD.Topology.ResourcePool,
	}

	// Perform cross-vCenter vMotion
	if err := relocator.RelocateVM(ctx, dummyVM, relocateConfig); err != nil {
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

// updatePVHandle updates the PV's volumeHandle to point to the new FCD
func (p *MigrateCSIVolumesPhase) updatePVHandle(ctx context.Context, pvManager *openshift.PersistentVolumeManager, pvState *migrationv1alpha1.PVMigrationState) error {
	// Update the PV's volumeHandle
	newHandle := vsphere.BuildCSIVolumeHandle(pvState.TargetVolumeID)
	return pvManager.UpdatePVVolumeHandle(ctx, pvState.PVName, newHandle)
}

// Rollback reverts the phase changes
func (p *MigrateCSIVolumesPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back MigrateCSIVolumes phase")

	if migration.Status.CSIVolumeMigration == nil {
		return nil
	}

	workloadManager := openshift.NewWorkloadManager(p.executor.kubeClient)

	// Restore all scaled down workloads
	for _, pvState := range migration.Status.CSIVolumeMigration.Volumes {
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
