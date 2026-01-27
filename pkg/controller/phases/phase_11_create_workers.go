package phases

import (
	"context"
	"fmt"

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

	// TODO: Implement MachineSet creation
	// This would involve:
	// 1. Get existing worker MachineSet as template
	// 2. Create new MachineSet with:
	//    - Updated name (e.g., add "-new" suffix)
	//    - Target vCenter failure domain
	//    - Desired replicas
	// 3. Wait for machines to be provisioned
	// 4. Wait for nodes to join cluster
	// 5. Track progress

	// Placeholder implementation showing the flow
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Creating MachineSet for new workers",
		string(p.Name()))

	// Step 1: Create MachineSet
	// machineSetName := fmt.Sprintf("worker-%s-new", migration.Spec.MachineSetConfig.FailureDomain)
	// machineSet := createMachineSet(machineSetName, migration)
	// err := machineClient.Create(ctx, machineSet)

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"MachineSet created, waiting for machines to be provisioned",
		string(p.Name()))

	// Step 2: Monitor machine provisioning
	// This would be an async operation that checks periodically
	// For now, return with requeue to check status later

	// TODO: Check machine status
	// readyMachines, totalMachines := getMachineStatus(ctx, machineSetName)
	// progress = int32((readyMachines * 100) / totalMachines)

	// If not all machines are ready, requeue
	// if readyMachines < totalMachines {
	// 	return &PhaseResult{
	// 		Status:       migrationv1alpha1.PhaseStatusRunning,
	// 		Message:      fmt.Sprintf("Waiting for machines to be ready (%d/%d)", readyMachines, totalMachines),
	// 		Progress:     progress,
	// 		Logs:         logs,
	// 		RequeueAfter: 30 * time.Second,
	// 	}, nil
	// }

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Waiting for nodes to join cluster",
		string(p.Name()))

	// Step 3: Wait for nodes to join
	// TODO: Check node status
	// readyNodes, totalNodes := getNodeStatus(ctx, machineSetName)
	// progress = 50 + int32((readyNodes * 50) / totalNodes)

	// if readyNodes < totalNodes {
	// 	return &PhaseResult{
	// 		Status:       migrationv1alpha1.PhaseStatusRunning,
	// 		Message:      fmt.Sprintf("Waiting for nodes to be ready (%d/%d)", readyNodes, totalNodes),
	// 		Progress:     progress,
	// 		Logs:         logs,
	// 		RequeueAfter: 30 * time.Second,
	// 	}, nil
	// }

	// Placeholder: Assume success for now
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"All worker machines and nodes are ready",
		string(p.Name()))

	logger.Info("Successfully created all worker machines")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully created all worker machines",
		Progress: 100,
		Logs:     logs,
		// Remove RequeueAfter to proceed to next phase
		RequeueAfter: 0,
	}, nil
}

// Rollback reverts the phase changes
func (p *CreateWorkersPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back CreateWorkers phase - deleting new worker MachineSet")

	// TODO: Implement MachineSet deletion
	// 1. Find MachineSet created by this phase
	// 2. Delete MachineSet
	// 3. Wait for machines and nodes to be removed

	// machineSetName := fmt.Sprintf("worker-%s-new", migration.Spec.MachineSetConfig.FailureDomain)
	// err := machineClient.Delete(ctx, machineSetName)
	// if err != nil {
	// 	logger.Error(err, "Failed to delete new worker MachineSet")
	// 	return err
	// }

	// Wait for deletion with timeout
	// timeout := time.After(10 * time.Minute)
	// ticker := time.NewTicker(10 * time.Second)
	// defer ticker.Stop()

	// for {
	// 	select {
	// 	case <-timeout:
	// 		return fmt.Errorf("timeout waiting for MachineSet deletion")
	// 	case <-ticker.C:
	// 		// Check if MachineSet still exists
	// 		_, err := machineClient.Get(ctx, machineSetName)
	// 		if errors.IsNotFound(err) {
	// 			logger.Info("MachineSet successfully deleted")
	// 			return nil
	// 		}
	// 	}
	// }

	logger.Info("Rollback for CreateWorkers phase completed (placeholder)")
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
