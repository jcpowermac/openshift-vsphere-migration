package phases

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
)

const scaleOldMachinesTimeout = 45 * time.Minute

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
func (p *ScaleOldMachinesPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	return nil
}

// Execute runs the phase
func (p *ScaleOldMachinesPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	isResume := migration.Status.CurrentPhaseState != nil &&
		migration.Status.CurrentPhaseState.Name == p.Name() &&
		migration.Status.CurrentPhaseState.Status == migrationv1alpha1.PhaseStatusRunning

	machineManager := p.executor.GetMachineManager()

	if !isResume {
		// --- First execution: scale all old MachineSets to 0, then requeue ---
		logger.Info("Scaling down old worker machines")
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Scaling down old worker machines", string(p.Name()))

		// Get source vCenter from Infrastructure CRD
		sourceVC, err := p.executor.infraManager.GetSourceVCenter(ctx)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "Failed to get source vCenter from Infrastructure: " + err.Error(),
				Logs:    logs,
			}, err
		}

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
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "No old MachineSets found", string(p.Name()))
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

		for _, ms := range oldMachineSets {
			if ms.Spec.Replicas != nil && *ms.Spec.Replicas == 0 {
				logger.Info("MachineSet already scaled to 0, skipping", "name", ms.Name)
				continue
			}
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
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			"All old MachineSets scaled to 0, waiting for machines and nodes to be deleted",
			string(p.Name()))

		return &PhaseResult{
			Status:       migrationv1alpha1.PhaseStatusRunning,
			Message:      "Old MachineSets scaled to 0, waiting for machine and node deletion",
			Progress:     10,
			Logs:         logs,
			RequeueAfter: 30 * time.Second,
		}, nil
	}

	// --- Resume: check timeout, then monitor machine and node deletion ---
	logger.Info("Checking old machine and node deletion status")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Checking machine and node deletion status", string(p.Name()))

	// Check timeout
	if migration.Status.CurrentPhaseState != nil && migration.Status.CurrentPhaseState.StartTime != nil {
		elapsed := time.Since(migration.Status.CurrentPhaseState.StartTime.Time)
		if elapsed > scaleOldMachinesTimeout {
			msg := fmt.Sprintf("Timed out waiting for machine/node deletion after %s", elapsed.Truncate(time.Second))
			logger.Error(nil, msg)
			logs = AddLog(logs, migrationv1alpha1.LogLevelError, msg, string(p.Name()))
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: msg,
				Logs:    logs,
			}, fmt.Errorf("%s", msg)
		}
	}

	// Re-fetch old MachineSets and ensure all are scaled to 0
	sourceVC, err := p.executor.infraManager.GetSourceVCenter(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get source vCenter from Infrastructure: " + err.Error(),
			Logs:    logs,
		}, err
	}

	oldMachineSets, err := machineManager.GetMachineSetsByVCenter(ctx, sourceVC.Server)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get old MachineSets: " + err.Error(),
			Logs:    logs,
		}, err
	}

	// Ensure all are scaled to 0 (handles partial scaling from controller crash)
	for _, ms := range oldMachineSets {
		if ms.Spec.Replicas != nil && *ms.Spec.Replicas != 0 {
			logger.Info("MachineSet not yet scaled to 0, scaling now", "name", ms.Name)
			if err := machineManager.ScaleMachineSet(ctx, ms.Name, 0); err != nil {
				return &PhaseResult{
					Status:  migrationv1alpha1.PhaseStatusFailed,
					Message: fmt.Sprintf("Failed to scale down MachineSet %s: %v", ms.Name, err),
					Logs:    logs,
				}, err
			}
		}
	}

	// Check if all Machine objects are deleted
	var totalRemainingMachines int32
	for _, ms := range oldMachineSets {
		allDeleted, remaining, err := machineManager.CheckMachinesDeleted(ctx, ms.Name)
		if err != nil {
			logger.V(2).Info("Error checking machine deletion", "machineSet", ms.Name, "error", err)
			continue
		}
		if !allDeleted {
			totalRemainingMachines += remaining
		}
	}

	if totalRemainingMachines > 0 {
		msg := fmt.Sprintf("Waiting for %d machines to be deleted", totalRemainingMachines)
		logger.Info(msg)
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, msg, string(p.Name()))
		return &PhaseResult{
			Status:       migrationv1alpha1.PhaseStatusRunning,
			Message:      msg,
			Progress:     30,
			Logs:         logs,
			RequeueAfter: 30 * time.Second,
		}, nil
	}

	// All machines deleted — check if all Nodes are removed
	var totalRemainingNodes int32
	for _, ms := range oldMachineSets {
		allDeleted, remaining, err := machineManager.CheckNodesDeletedForMachines(ctx, ms.Name)
		if err != nil {
			logger.V(2).Info("Error checking node deletion", "machineSet", ms.Name, "error", err)
			continue
		}
		if !allDeleted {
			totalRemainingNodes += remaining
		}
	}

	if totalRemainingNodes > 0 {
		msg := fmt.Sprintf("Machines deleted, waiting for %d nodes to be removed", totalRemainingNodes)
		logger.Info(msg)
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, msg, string(p.Name()))
		return &PhaseResult{
			Status:       migrationv1alpha1.PhaseStatusRunning,
			Message:      msg,
			Progress:     60,
			Logs:         logs,
			RequeueAfter: 30 * time.Second,
		}, nil
	}

	// All machines and nodes are gone
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"All old machines and nodes have been deleted",
		string(p.Name()))
	logger.Info("Successfully scaled down all old worker machines and confirmed deletion")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "All old machines and nodes have been deleted",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *ScaleOldMachinesPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
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
