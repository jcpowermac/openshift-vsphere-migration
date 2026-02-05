package phases

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
)

// RecreateCPMSPhase recreates the Control Plane Machine Set
type RecreateCPMSPhase struct {
	executor *PhaseExecutor
}

// NewRecreateCPMSPhase creates a new recreate CPMS phase
func NewRecreateCPMSPhase(executor *PhaseExecutor) *RecreateCPMSPhase {
	return &RecreateCPMSPhase{
		executor: executor,
	}
}

// Name returns the phase name
func (p *RecreateCPMSPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseRecreateCPMS
}

// Validate checks if the phase can be executed
func (p *RecreateCPMSPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	return nil
}

// Execute runs the phase
func (p *RecreateCPMSPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	// Check if this is a resume (CPMS already updated, just polling for rollout)
	isResume := migration.Status.CurrentPhaseState != nil &&
		migration.Status.CurrentPhaseState.Name == p.Name() &&
		migration.Status.CurrentPhaseState.Status == migrationv1alpha1.PhaseStatusRunning

	machineManager := p.executor.GetMachineManager()

	if !isResume {
		// First execution - update CPMS
		logger.Info("Updating Control Plane Machine Set for new vCenter")
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Updating Control Plane Machine Set", string(p.Name()))

		// CPMS was already deleted in Phase 6 (DeleteCPMS) and should be auto-recreated as Inactive
		// Wait for CPMS to become Inactive (auto-recreated)
		logger.Info("Waiting for CPMS to become Inactive")
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			"Waiting for CPMS to become Inactive",
			string(p.Name()))

		if err := machineManager.WaitForCPMSInactive(ctx, 5*time.Minute); err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "CPMS did not become Inactive: " + err.Error(),
				Logs:    logs,
			}, err
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			"CPMS is now Inactive",
			string(p.Name()))

		// Get infrastructure ID for folder path construction
		infraID, err := p.executor.infraManager.GetInfrastructureID(ctx)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "Failed to get infrastructure ID: " + err.Error(),
				Logs:    logs,
			}, err
		}

		// Update CPMS with new failure domain and set to Active
		logger.Info("Updating CPMS with new failure domain",
			"failureDomain", migration.Spec.ControlPlaneMachineSetConfig.FailureDomain)
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			"Updating CPMS with target vCenter failure domain",
			string(p.Name()))

		if err := machineManager.UpdateCPMSFailureDomain(ctx, migration, infraID); err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "Failed to update CPMS: " + err.Error(),
				Logs:    logs,
			}, err
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			"Updated CPMS and set to Active",
			string(p.Name()))
	} else {
		logger.Info("Resuming control plane rollout check")
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			"Resuming control plane rollout check",
			string(p.Name()))
	}

	// Check rollout status (non-blocking to avoid leader election timeout)
	logger.Info("Checking control plane rollout status")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Checking control plane rollout status",
		string(p.Name()))

	complete, replicas, updatedReplicas, readyReplicas, err := machineManager.CheckControlPlaneRolloutStatus(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to check control plane rollout status: " + err.Error(),
			Logs:    logs,
		}, err
	}

	if !complete {
		msg := fmt.Sprintf("Waiting for control plane rollout: %d/%d updated, %d/%d ready",
			updatedReplicas, replicas, readyReplicas, replicas)
		logger.Info(msg)
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, msg, string(p.Name()))

		progress := int32(0)
		if replicas > 0 {
			progress = int32(float64(readyReplicas) / float64(replicas) * 100)
		}

		return &PhaseResult{
			Status:       migrationv1alpha1.PhaseStatusRunning,
			Message:      msg,
			Progress:     progress,
			Logs:         logs,
			RequeueAfter: 30 * time.Second,
		}, nil
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Control plane rollout completed successfully",
		string(p.Name()))

	logger.Info("Successfully updated Control Plane Machine Set")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully updated CPMS and rolled out control plane",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *RecreateCPMSPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back RecreateCPMS phase")

	// Get MachineManager with all required clients
	machineManager := p.executor.GetMachineManager()

	// Get CPMS backup
	backup, err := p.executor.backupManager.GetBackup(migration, "ControlPlaneMachineSet", "cluster", "openshift-machine-api")
	if err != nil {
		logger.Error(err, "Failed to get CPMS backup")
		return err
	}

	// Delete current CPMS
	if err := machineManager.DeleteControlPlaneMachineSet(ctx); err != nil {
		logger.Error(err, "Failed to delete CPMS during rollback")
	}

	// Restore from backup
	if err := p.executor.restoreManager.RestoreResource(ctx, backup); err != nil {
		logger.Error(err, "Failed to restore CPMS")
		return err
	}

	logger.Info("Successfully restored CPMS from backup")
	return nil
}
