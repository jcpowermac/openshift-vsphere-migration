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

	isResume := migration.Status.CurrentPhaseState != nil &&
		migration.Status.CurrentPhaseState.Name == p.Name() &&
		migration.Status.CurrentPhaseState.Status == migrationv1alpha1.PhaseStatusRunning

	machineManager := p.executor.GetMachineManager()

	if !isResume {

		// --- First execution: update CPMS, then requeue ---
		logger.Info("Updating Control Plane Machine Set for new vCenter")
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Updating Control Plane Machine Set", string(p.Name()))

		logger.Info("Waiting for CPMS to become Inactive")
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Waiting for CPMS to become Inactive", string(p.Name()))

		if err := machineManager.WaitForCPMSInactive(ctx, 5*time.Minute); err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "CPMS did not become Inactive: " + err.Error(),
				Logs:    logs,
			}, err
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "CPMS is now Inactive", string(p.Name()))

		infraID, err := p.executor.infraManager.GetInfrastructureID(ctx)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "Failed to get infrastructure ID: " + err.Error(),
				Logs:    logs,
			}, err
		}

		logger.Info("Updating CPMS with new failure domain",
			"failureDomain", migration.Spec.ControlPlaneMachineSetConfig.FailureDomain)
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Updating CPMS with target vCenter failure domain", string(p.Name()))

		if err := machineManager.UpdateCPMSFailureDomain(ctx, migration, infraID); err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "Failed to update CPMS: " + err.Error(),
				Logs:    logs,
			}, err
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Updated CPMS and set to Active, waiting for rollout to begin", string(p.Name()))
	}
	/*
		// Do NOT check rollout status — the CPMS controller needs time to react.
		// Return Running to trigger requeue; StartTime will be set by the reconciler.
		return &PhaseResult{
			Status:       migrationv1alpha1.PhaseStatusRunning,
			Message:      "CPMS updated, waiting for control plane rollout to begin",
			Progress:     0,
			Logs:         logs,
			RequeueAfter: 30 * time.Second,
		}, nil

	*/

	// --- Resume: monitor rollout ---
	logger.Info("Monitoring control plane rollout status")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Checking control plane rollout status", string(p.Name()))

	// Check if CPMS controller has observed the spec update
	observed, err := machineManager.IsCPMSGenerationObserved(ctx)
	if err != nil {
		logger.V(2).Info("Unable to check CPMS generation", "error", err)
		// Non-fatal — fall through to replica check with monitoring period guard
	} else if !observed {
		msg := "CPMS controller has not yet processed the spec update"
		logger.Info(msg)
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, msg, string(p.Name()))
		return &PhaseResult{
			Status:       migrationv1alpha1.PhaseStatusRunning,
			Message:      msg,
			Progress:     0,
			Logs:         logs,
			RequeueAfter: 30 * time.Second,
		}, nil
	}

	complete, replicas, updatedReplicas, readyReplicas, err := machineManager.CheckControlPlaneRolloutStatus(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to check control plane rollout status: " + err.Error(),
			Logs:    logs,
		}, err
	}

	// Enforce minimum monitoring period: even if replicas look healthy,
	// the CPMS controller may not have started the rollout yet.
	const minMonitoringDuration = 5 * time.Minute
	if complete && migration.Status.CurrentPhaseState != nil && migration.Status.CurrentPhaseState.StartTime != nil {
		elapsed := time.Since(migration.Status.CurrentPhaseState.StartTime.Time)
		if elapsed < minMonitoringDuration {
			msg := fmt.Sprintf("Monitoring rollout stability (%s / %s elapsed)",
				elapsed.Truncate(time.Second), minMonitoringDuration)
			logger.Info(msg)
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, msg, string(p.Name()))
			return &PhaseResult{
				Status:       migrationv1alpha1.PhaseStatusRunning,
				Message:      msg,
				Progress:     50,
				Logs:         logs,
				RequeueAfter: 30 * time.Second,
			}, nil
		}
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

	// Rollout complete and monitoring period elapsed
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Control plane rollout completed successfully", string(p.Name()))
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
