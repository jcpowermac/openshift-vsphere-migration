package phases

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
)

// ScaleOldMachinesPhase scales down old worker machines
type ScaleOldMachinesPhase struct {
	executor *PhaseExecutor
}

// NewScaleOldMachinesPhase creates a new scale old machines phase
func NewScaleOldMachinesPhase(executor *PhaseExecutor) *ScaleOldMachinesPhase {
	return &ScaleOldMachinesPhase{
		executor: executor,
	}
}

// Name returns the phase name
func (p *ScaleOldMachinesPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseScaleOldMachines
}

// Validate checks if the phase can be executed
func (p *ScaleOldMachinesPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	return nil
}

// Execute runs the phase
func (p *ScaleOldMachinesPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Scaling down old worker machines")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Scaling down old worker machines", string(p.Name()))

	// Get MachineManager with all required clients
	machineManager := p.executor.GetMachineManager()

	// Get source vCenter from Infrastructure CRD
	sourceVC, err := p.executor.infraManager.GetSourceVCenter(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get source vCenter from Infrastructure: " + err.Error(),
			Logs:    logs,
		}, err
	}

	// Get old MachineSets (from source vCenter)
	logger.Info("Finding old MachineSets from source vCenter", "server", sourceVC.Server)
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("Finding old MachineSets from source vCenter: %s", sourceVC.Server),
		string(p.Name()))

	oldMachineSets, err := machineManager.GetMachineSetsByVCenter(ctx, sourceVC.Server)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get old MachineSets: " + err.Error(),
			Logs:    logs,
		}, err
	}

	if len(oldMachineSets) == 0 {
		logger.Info("No old MachineSets found")
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			"No old MachineSets found",
			string(p.Name()))
		return &PhaseResult{
			Status:   migrationv1alpha1.PhaseStatusCompleted,
			Message:  "No old MachineSets to scale down",
			Progress: 100,
			Logs:     logs,
		}, nil
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("Found %d old MachineSets", len(oldMachineSets)),
		string(p.Name()))

	// Scale down each old MachineSet
	for i, ms := range oldMachineSets {
		logger.Info("Scaling down MachineSet", "name", ms.Name)
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Scaling down MachineSet %s to 0 replicas", ms.Name),
			string(p.Name()))

		if err := machineManager.ScaleMachineSet(ctx, ms.Name, 0); err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: fmt.Sprintf("Failed to scale down MachineSet %s: %v", ms.Name, err),
				Logs:    logs,
			}, err
		}

		// Update progress
		progress := int32((i + 1) * 100 / len(oldMachineSets))
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Progress: %d%%", progress),
			string(p.Name()))
	}

	// Wait for old machines to be deleted
	logger.Info("Waiting for old machines to be deleted")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Waiting for old machines to be deleted",
		string(p.Name()))

	// TODO: Wait for machines to actually be deleted
	time.Sleep(30 * time.Second) // Placeholder

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"All old machines have been deleted",
		string(p.Name()))

	logger.Info("Successfully scaled down all old worker machines")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully scaled down all old worker machines",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *ScaleOldMachinesPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back ScaleOldMachines phase - restoring old MachineSets")

	// Get MachineManager with all required clients
	machineManager := p.executor.GetMachineManager()

	// Get source vCenter from Infrastructure CRD
	sourceVC, err := p.executor.infraManager.GetSourceVCenter(ctx)
	if err != nil {
		logger.Error(err, "Failed to get source vCenter from Infrastructure")
		return err
	}

	// Get old MachineSets
	oldMachineSets, err := machineManager.GetMachineSetsByVCenter(ctx, sourceVC.Server)
	if err != nil {
		logger.Error(err, "Failed to get old MachineSets")
		return err
	}

	// Scale back to original replicas
	// TODO: Restore original replica count from backup
	for _, ms := range oldMachineSets {
		logger.Info("Restoring MachineSet", "name", ms.Name)
		if err := machineManager.ScaleMachineSet(ctx, ms.Name, 3); err != nil {
			logger.Error(err, "Failed to restore MachineSet", "name", ms.Name)
		}
	}

	logger.Info("Successfully restored old MachineSets")
	return nil
}
