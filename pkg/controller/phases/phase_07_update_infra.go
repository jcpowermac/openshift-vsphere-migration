package phases

import (
	"context"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
)

// UpdateInfrastructurePhase updates the Infrastructure CRD
type UpdateInfrastructurePhase struct {
	executor *PhaseExecutor
}

// NewUpdateInfrastructurePhase creates a new update infrastructure phase
func NewUpdateInfrastructurePhase(executor *PhaseExecutor) *UpdateInfrastructurePhase {
	return &UpdateInfrastructurePhase{executor: executor}
}

// Name returns the phase name
func (p *UpdateInfrastructurePhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseUpdateInfrastructure
}

// Validate checks if the phase can be executed
func (p *UpdateInfrastructurePhase) Validate(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	return nil
}

// Execute runs the phase
func (p *UpdateInfrastructurePhase) Execute(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Updating Infrastructure CRD with target vCenter")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Updating Infrastructure CRD with target vCenter", string(p.Name()))

	// Get current infrastructure
	infra, err := p.executor.infraManager.Get(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get infrastructure: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Retrieved current Infrastructure CRD",
		string(p.Name()))

	// Add target vCenter and failure domains (with CRD modification)
	logger.Info("Adding target vCenter with CRD modification")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Modifying Infrastructure CRD to allow vCenter array modification (CVO will restore later)",
		string(p.Name()))

	updatedInfra, err := p.executor.infraManager.AddTargetVCenterWithCRDModification(ctx, infra, migration)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to add target vCenter to infrastructure: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Infrastructure CRD modified - CVO will restore original schema when re-enabled",
		string(p.Name()))

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Added target vCenter and failure domains to Infrastructure CRD",
		string(p.Name()))

	// Verify update
	if updatedInfra.Spec.PlatformSpec.VSphere == nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Infrastructure vSphere platform spec is nil after update",
			Logs:    logs,
		}, err
	}

	vCenterCount := len(updatedInfra.Spec.PlatformSpec.VSphere.VCenters)
	fdCount := len(updatedInfra.Spec.PlatformSpec.VSphere.FailureDomains)

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Infrastructure updated successfully",
		string(p.Name()))

	logger.Info("Successfully updated Infrastructure CRD",
		"vCenters", vCenterCount,
		"failureDomains", fdCount)

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully updated Infrastructure CRD",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *UpdateInfrastructurePhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back UpdateInfrastructure phase")

	// Restore infrastructure from backup
	backup, err := p.executor.backupManager.GetBackup(migration, "Infrastructure", "cluster", "")
	if err != nil {
		logger.Error(err, "Failed to get infrastructure backup")
		return err
	}

	if err := p.executor.restoreManager.RestoreResource(ctx, backup); err != nil {
		logger.Error(err, "Failed to restore infrastructure")
		return err
	}

	logger.Info("Successfully restored Infrastructure CRD from backup")
	return nil
}
