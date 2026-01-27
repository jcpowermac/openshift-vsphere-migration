package phases

import (
	"context"
	"time"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vsphere-migration-controller/pkg/openshift"
)

// RecreateCPMSPhase recreates the Control Plane Machine Set
type RecreateCPMSPhase struct {
	executor       *PhaseExecutor
	machineManager *openshift.MachineManager
}

// NewRecreateCPMSPhase creates a new recreate CPMS phase
func NewRecreateCPMSPhase(executor *PhaseExecutor) *RecreateCPMSPhase {
	return &RecreateCPMSPhase{
		executor:       executor,
		machineManager: openshift.NewMachineManager(executor.kubeClient),
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

	logger.Info("Recreating Control Plane Machine Set")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Recreating Control Plane Machine Set", string(p.Name()))

	// Get current CPMS as template
	logger.Info("Getting current Control Plane Machine Set")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Getting current Control Plane Machine Set",
		string(p.Name()))

	currentCPMS, err := p.machineManager.GetControlPlaneMachineSet(ctx)
	if err != nil {
		logger.Error(err, "Failed to get current CPMS (may not exist)")
		// CPMS may not exist, continue
	}

	// Delete existing CPMS
	logger.Info("Deleting existing Control Plane Machine Set")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Deleting existing Control Plane Machine Set",
		string(p.Name()))

	if err := p.machineManager.DeleteControlPlaneMachineSet(ctx); err != nil {
		logger.Error(err, "Failed to delete CPMS (may not exist)")
		// Continue - CPMS may not have existed
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Deleted existing CPMS",
		string(p.Name()))

	// Create new CPMS with target vCenter failure domain
	logger.Info("Creating new Control Plane Machine Set",
		"failureDomain", migration.Spec.ControlPlaneMachineSetConfig.FailureDomain)
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Creating new CPMS with target vCenter failure domain",
		string(p.Name()))

	if err := p.machineManager.CreateControlPlaneMachineSet(ctx, migration, currentCPMS); err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to create new CPMS: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Created new CPMS",
		string(p.Name()))

	// Monitor rollout
	logger.Info("Monitoring control plane rollout (this may take 30-60 minutes)")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Monitoring control plane rollout",
		string(p.Name()))

	if err := p.machineManager.WaitForControlPlaneRollout(ctx, 60*time.Minute); err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Control plane rollout failed: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Control plane rollout completed successfully",
		string(p.Name()))

	logger.Info("Successfully recreated Control Plane Machine Set")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully recreated CPMS and rolled out control plane",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *RecreateCPMSPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back RecreateCPMS phase")

	// Get CPMS backup
	backup, err := p.executor.backupManager.GetBackup(migration, "ControlPlaneMachineSet", "cluster", "openshift-machine-api")
	if err != nil {
		logger.Error(err, "Failed to get CPMS backup")
		return err
	}

	// Delete current CPMS
	if err := p.machineManager.DeleteControlPlaneMachineSet(ctx); err != nil {
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
