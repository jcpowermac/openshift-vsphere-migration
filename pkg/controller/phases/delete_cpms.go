package phases

import (
	"context"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
)

// DeleteCPMSPhase deletes the Control Plane Machine Set before infrastructure update
type DeleteCPMSPhase struct {
	executor *PhaseExecutor
}

// NewDeleteCPMSPhase creates a new delete CPMS phase
func NewDeleteCPMSPhase(executor *PhaseExecutor) *DeleteCPMSPhase {
	return &DeleteCPMSPhase{
		executor: executor,
	}
}

// Name returns the phase name
func (p *DeleteCPMSPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseDeleteCPMS
}

// Validate checks if the phase can be executed
func (p *DeleteCPMSPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	return nil
}

// Execute runs the phase
func (p *DeleteCPMSPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Deleting Control Plane Machine Set before infrastructure update")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Deleting Control Plane Machine Set before infrastructure update",
		string(p.Name()))

	machineManager := p.executor.GetMachineManager()

	// Delete CPMS
	if err := machineManager.DeleteControlPlaneMachineSet(ctx); err != nil {
		logger.Info("Failed to delete CPMS (may not exist)", "error", err)
		logs = AddLog(logs, migrationv1alpha1.LogLevelWarning,
			"CPMS not found or already deleted",
			string(p.Name()))
	} else {
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			"Successfully deleted Control Plane Machine Set",
			string(p.Name()))
	}

	logger.Info("CPMS deletion phase completed")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully deleted CPMS",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *DeleteCPMSPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back DeleteCPMS phase - restoring CPMS")

	// Get CPMS backup
	backup, err := p.executor.backupManager.GetBackup(migration, "ControlPlaneMachineSet", "cluster", "openshift-machine-api")
	if err != nil {
		logger.Info("No CPMS backup found, skipping restore", "error", err)
		return nil
	}

	// Restore from backup
	if err := p.executor.restoreManager.RestoreResource(ctx, backup); err != nil {
		logger.Error(err, "Failed to restore CPMS")
		return err
	}

	logger.Info("Successfully restored CPMS from backup")
	return nil
}
