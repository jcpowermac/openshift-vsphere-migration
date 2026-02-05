package phases

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
)

// CreateWorkersPhase creates new worker machines in target vCenter
type CreateWorkersPhase struct {
	executor *PhaseExecutor
}

// NewCreateWorkersPhase creates a new create workers phase
func NewCreateWorkersPhase(executor *PhaseExecutor) *CreateWorkersPhase {
	return &CreateWorkersPhase{executor: executor}
}

// Name returns the phase name
func (p *CreateWorkersPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseCreateWorkers
}

// Validate checks if the phase can be executed
func (p *CreateWorkersPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	if migration.Spec.MachineSetConfig.Replicas <= 0 {
		return fmt.Errorf("worker replicas must be greater than 0")
	}
	if migration.Spec.MachineSetConfig.FailureDomain == "" {
		return fmt.Errorf("worker failure domain is empty")
	}
	return nil
}

// Execute runs the phase
func (p *CreateWorkersPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Creating new worker machines in target vCenter",
		"replicas", migration.Spec.MachineSetConfig.Replicas,
		"failureDomain", migration.Spec.MachineSetConfig.FailureDomain)

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("Creating %d worker machines in failure domain %s",
			migration.Spec.MachineSetConfig.Replicas,
			migration.Spec.MachineSetConfig.FailureDomain),
		string(p.Name()))

	// Validate failure domain configuration early
	targetFD := migration.Spec.MachineSetConfig.FailureDomain
	var foundFD *configv1.VSpherePlatformFailureDomainSpec
	for i := range migration.Spec.FailureDomains {
		if migration.Spec.FailureDomains[i].Name == targetFD {
			foundFD = &migration.Spec.FailureDomains[i]
			break
		}
	}

	if foundFD == nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: fmt.Sprintf("failure domain %s not found in VmwareCloudFoundationMigration CR", targetFD),
			Logs:    logs,
		}, fmt.Errorf("failure domain %s not found", targetFD)
	}

	if foundFD.Topology.Template == "" {
		logger.Error(nil, "Template not configured",
			"failureDomain", foundFD.Name,
			"fullSpec", fmt.Sprintf("%+v", foundFD))
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: fmt.Sprintf("template not specified in failure domain %s topology", targetFD),
			Logs:    logs,
		}, fmt.Errorf("template required but not specified")
	}

	logger.Info("Validated failure domain configuration",
		"name", foundFD.Name,
		"template", foundFD.Topology.Template)

	// Get MachineManager
	machineManager := p.executor.GetMachineManager()

	// Get infrastructure ID for naming
	infraID, err := p.executor.infraManager.GetInfrastructureID(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get infrastructure ID: " + err.Error(),
			Logs:    logs,
		}, err
	}

	// Check if MachineSet already exists (idempotency)
	newMachineSetName := fmt.Sprintf("%s-worker-%s", infraID, migration.Spec.MachineSetConfig.FailureDomain)
	existingMS, err := machineManager.GetMachineSet(ctx, newMachineSetName)

	if err == nil && existingMS != nil {
		logger.Info("MachineSet already exists, checking readiness",
			"name", newMachineSetName)
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("MachineSet %s already exists (idempotent)", newMachineSetName),
			string(p.Name()))

		// Check machines ready (non-blocking)
		machinesComplete, readyMachines, totalMachines, err := machineManager.CheckMachinesReady(ctx, newMachineSetName)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "Failed to check machines: " + err.Error(),
				Logs:    logs,
			}, err
		}

		if !machinesComplete {
			msg := fmt.Sprintf("Waiting for machines: %d/%d ready", readyMachines, totalMachines)
			logger.Info(msg)
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, msg, string(p.Name()))

			progress := int32(0)
			if totalMachines > 0 {
				progress = int32(float64(readyMachines) / float64(totalMachines) * 50)
			}

			return &PhaseResult{
				Status:       migrationv1alpha1.PhaseStatusRunning,
				Message:      msg,
				Progress:     progress,
				Logs:         logs,
				RequeueAfter: 30 * time.Second,
			}, nil
		}

		// Check nodes ready (non-blocking)
		nodesComplete, readyNodes, totalNodes, err := machineManager.CheckNodesReady(ctx, newMachineSetName)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "Failed to check nodes: " + err.Error(),
				Logs:    logs,
			}, err
		}

		if !nodesComplete {
			msg := fmt.Sprintf("Waiting for nodes: %d/%d ready", readyNodes, totalNodes)
			logger.Info(msg)
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, msg, string(p.Name()))

			progress := int32(50)
			if totalNodes > 0 {
				progress = 50 + int32(float64(readyNodes)/float64(totalNodes)*50)
			}

			return &PhaseResult{
				Status:       migrationv1alpha1.PhaseStatusRunning,
				Message:      msg,
				Progress:     progress,
				Logs:         logs,
				RequeueAfter: 30 * time.Second,
			}, nil
		}

		// MachineSet already exists and is ready
		return &PhaseResult{
			Status:   migrationv1alpha1.PhaseStatusCompleted,
			Message:  fmt.Sprintf("MachineSet already exists with %d/%d machines ready", readyMachines, totalMachines),
			Progress: 100,
			Logs:     logs,
		}, nil
	}

	// Step 1: Get existing worker MachineSet as template
	existingSets, err := machineManager.GetMachineSetsByVCenter(ctx, "")
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get existing MachineSets: " + err.Error(),
			Logs:    logs,
		}, err
	}

	if len(existingSets) == 0 {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "No existing MachineSets to use as template",
			Logs:    logs,
		}, fmt.Errorf("no existing MachineSets found")
	}

	template := existingSets[0]
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("Using MachineSet %s as template", template.Name),
		string(p.Name()))

	// Step 2: Create new MachineSet
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("Creating new MachineSet %s", newMachineSetName),
		string(p.Name()))

	newMachineSet, err := machineManager.CreateWorkerMachineSet(ctx, newMachineSetName, migration, template, infraID)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to create MachineSet: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("Created MachineSet %s with %d replicas", newMachineSet.Name, migration.Spec.MachineSetConfig.Replicas),
		string(p.Name()))

	// Return running status - next reconcile will check machine/node readiness
	msg := fmt.Sprintf("Created MachineSet %s, waiting for machines to provision", newMachineSet.Name)
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, msg, string(p.Name()))

	return &PhaseResult{
		Status:       migrationv1alpha1.PhaseStatusRunning,
		Message:      msg,
		Progress:     10,
		Logs:         logs,
		RequeueAfter: 30 * time.Second,
	}, nil
}

// Rollback reverts the phase changes
func (p *CreateWorkersPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back CreateWorkers phase - deleting new worker MachineSet")

	// Get infrastructure ID for naming
	infraID, err := p.executor.infraManager.GetInfrastructureID(ctx)
	if err != nil {
		return fmt.Errorf("failed to get infrastructure ID: %w", err)
	}

	machineManager := p.executor.GetMachineManager()
	machineSetName := fmt.Sprintf("%s-worker-%s", infraID, migration.Spec.MachineSetConfig.FailureDomain)

	// Delete MachineSet
	err = machineManager.DeleteMachineSet(ctx, machineSetName)
	if err != nil {
		logger.Error(err, "Failed to delete new worker MachineSet")
		return err
	}

	logger.Info("Successfully deleted new worker MachineSet", "name", machineSetName)
	return nil
}

// Helper functions that would be implemented in pkg/openshift/machines.go

// createMachineSet creates a new MachineSet for the target vCenter
// func createMachineSet(name string, migration *migrationv1alpha1.VmwareCloudFoundationMigration) *machinev1beta1.MachineSet {
// 	// Get template from existing MachineSet
// 	// Modify to use target vCenter failure domain
// 	// Set desired replicas
// 	// Return new MachineSet
// }

// getMachineStatus returns the number of ready and total machines in a MachineSet
// func getMachineStatus(ctx context.Context, machineSetName string) (ready, total int32) {
// 	// List machines with label selector
// 	// Count machines with Phase=Running or Provisioned
// 	// Return counts
// }

// getNodeStatus returns the number of ready and total nodes for a MachineSet
// func getNodeStatus(ctx context.Context, machineSetName string) (ready, total int32) {
// 	// List nodes created by machines in the MachineSet
// 	// Count nodes with Ready condition
// 	// Return counts
// }
