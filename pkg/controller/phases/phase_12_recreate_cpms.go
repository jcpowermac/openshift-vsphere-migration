package phases

import (
	"context"
	"time"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
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
func (p *RecreateCPMSPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	return nil
}

// Execute runs the phase
func (p *RecreateCPMSPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Updating Control Plane Machine Set for new vCenter")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Updating Control Plane Machine Set", string(p.Name()))

	machineManager := p.executor.GetMachineManager()

	// Delete existing CPMS (triggers auto-recreation as Inactive)
	logger.Info("Deleting Control Plane Machine Set to trigger recreation as Inactive")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Deleting CPMS to trigger recreation as Inactive",
		string(p.Name()))

	if err := machineManager.DeleteControlPlaneMachineSet(ctx); err != nil {
		logger.Error(err, "Failed to delete CPMS (may not exist)")
		// Continue - CPMS may not have existed
	}

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

	// Monitor rollout
	logger.Info("Monitoring control plane rollout (this may take 30-60 minutes)")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Monitoring control plane rollout",
		string(p.Name()))

	if err := machineManager.WaitForControlPlaneRollout(ctx, 60*time.Minute); err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Control plane rollout failed: " + err.Error(),
			Logs:    logs,
		}, err
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
func (p *RecreateCPMSPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
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
