package phases

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
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
func (p *CreateWorkersPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	if migration.Spec.MachineSetConfig.Replicas <= 0 {
		return fmt.Errorf("worker replicas must be greater than 0")
	}
	if migration.Spec.MachineSetConfig.FailureDomain == "" {
		return fmt.Errorf("worker failure domain is empty")
	}
	return nil
}

// Execute runs the phase
func (p *CreateWorkersPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) (*PhaseResult, error) {
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
			Message: fmt.Sprintf("failure domain %s not found in VSphereMigration CR", targetFD),
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
		logger.Info("MachineSet already exists, verifying readiness",
			"name", newMachineSetName)
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("MachineSet %s already exists (idempotent)", newMachineSetName),
			string(p.Name()))

		// Wait for existing MachineSet to be ready
		readyMachines, totalMachines, err := machineManager.WaitForMachinesReady(ctx, newMachineSetName, 30*time.Minute)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "Existing MachineSet not ready: " + err.Error(),
				Logs:    logs,
			}, err
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

	// Step 3: Wait for machines to be provisioned
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Waiting for machines to be provisioned",
		string(p.Name()))

	timeout := 30 * time.Minute
	readyMachines, totalMachines, err := machineManager.WaitForMachinesReady(ctx, newMachineSet.Name, timeout)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: fmt.Sprintf("Machines not ready: %v", err),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("All %d machines are provisioned", readyMachines),
		string(p.Name()))

	// Step 4: Wait for nodes to join cluster
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Waiting for nodes to join cluster and become Ready",
		string(p.Name()))

	readyNodes, totalNodes, err := machineManager.WaitForNodesReady(ctx, newMachineSet.Name, timeout)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: fmt.Sprintf("Nodes not ready: %v", err),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("All %d nodes are Ready (%d machines, %d nodes)", readyNodes, totalMachines, totalNodes),
		string(p.Name()))

	logger.Info("Successfully created all worker machines")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  fmt.Sprintf("Successfully created MachineSet with %d ready nodes", readyNodes),
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *CreateWorkersPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
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
// func createMachineSet(name string, migration *migrationv1alpha1.VSphereMigration) *machinev1beta1.MachineSet {
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
