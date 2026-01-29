package state

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/controller/phases"
)

// StateMachine manages migration state transitions
type StateMachine struct {
	phaseExecutor *phases.PhaseExecutor
	phaseOrder    []migrationv1alpha1.MigrationPhase
}

// NewStateMachine creates a new state machine
func NewStateMachine(executor *phases.PhaseExecutor) *StateMachine {
	return &StateMachine{
		phaseExecutor: executor,
		phaseOrder: []migrationv1alpha1.MigrationPhase{
			migrationv1alpha1.PhasePreflight,            // 1
			migrationv1alpha1.PhaseBackup,               // 2
			migrationv1alpha1.PhaseDisableCVO,           // 3
			migrationv1alpha1.PhaseUpdateSecrets,        // 4
			migrationv1alpha1.PhaseCreateTags,           // 5
			migrationv1alpha1.PhaseCreateFolder,         // 6
			migrationv1alpha1.PhaseDeleteCPMS,           // 7
			migrationv1alpha1.PhaseUpdateInfrastructure, // 8
			migrationv1alpha1.PhaseUpdateConfig,         // 9
			migrationv1alpha1.PhaseRestartPods,          // 10
			migrationv1alpha1.PhaseMonitorHealth,        // 11
			migrationv1alpha1.PhaseCreateWorkers,        // 12
			migrationv1alpha1.PhaseRecreateCPMS,         // 13
			migrationv1alpha1.PhaseMigrateCSIVolumes,    // 14
			migrationv1alpha1.PhaseScaleOldMachines,     // 15
			migrationv1alpha1.PhaseCleanup,              // 16
			migrationv1alpha1.PhaseVerify,               // 17
		},
	}
}

// GetNextPhase returns the next phase to execute
func (s *StateMachine) GetNextPhase(migration *migrationv1alpha1.VmwareCloudFoundationMigration) (migrationv1alpha1.MigrationPhase, error) {
	currentPhase := migration.Status.Phase

	// If no current phase, start with first phase
	if currentPhase == migrationv1alpha1.PhaseNone || currentPhase == "" {
		return s.phaseOrder[0], nil
	}

	// If completed, no next phase
	if currentPhase == migrationv1alpha1.PhaseCompleted {
		return migrationv1alpha1.PhaseNone, nil
	}

	// Find current phase in order
	for i, phase := range s.phaseOrder {
		if phase == currentPhase {
			// Return next phase if available
			if i+1 < len(s.phaseOrder) {
				return s.phaseOrder[i+1], nil
			}
			// No more phases, mark as completed
			return migrationv1alpha1.PhaseCompleted, nil
		}
	}

	return migrationv1alpha1.PhaseNone, fmt.Errorf("unknown current phase: %s", currentPhase)
}

// ShouldExecutePhase determines if a phase should be executed
func (s *StateMachine) ShouldExecutePhase(migration *migrationv1alpha1.VmwareCloudFoundationMigration, phase migrationv1alpha1.MigrationPhase) bool {
	// Check migration state
	if migration.Spec.State != migrationv1alpha1.MigrationStateRunning {
		return false
	}

	// Check if phase requires approval
	if migration.Spec.ApprovalMode == migrationv1alpha1.ApprovalModeManual {
		// Check if current phase state requires approval
		if migration.Status.CurrentPhaseState != nil &&
			migration.Status.CurrentPhaseState.Name == phase &&
			migration.Status.CurrentPhaseState.RequiresApproval &&
			!migration.Status.CurrentPhaseState.Approved {
			return false
		}
	}

	return true
}

// RecordPhaseCompletion records a completed phase in history
func (s *StateMachine) RecordPhaseCompletion(migration *migrationv1alpha1.VmwareCloudFoundationMigration, phase migrationv1alpha1.MigrationPhase, result *phases.PhaseResult) {
	now := metav1.Now()

	// Find start time from current phase state
	var startTime metav1.Time
	if migration.Status.CurrentPhaseState != nil {
		// Use start time if available, otherwise use now
		startTime = now
		// Try to find from phase history
		for _, entry := range migration.Status.PhaseHistory {
			if entry.Phase == phase && entry.CompletionTime == nil {
				startTime = entry.StartTime
				break
			}
		}
	} else {
		startTime = now
	}

	// Create history entry
	historyEntry := migrationv1alpha1.PhaseHistoryEntry{
		Phase:          phase,
		Status:         result.Status,
		StartTime:      startTime,
		CompletionTime: &now,
		Message:        result.Message,
		Logs:           result.Logs,
	}

	// Update or add to history
	updated := false
	for i := range migration.Status.PhaseHistory {
		if migration.Status.PhaseHistory[i].Phase == phase && migration.Status.PhaseHistory[i].CompletionTime == nil {
			migration.Status.PhaseHistory[i] = historyEntry
			updated = true
			break
		}
	}

	if !updated {
		migration.Status.PhaseHistory = append(migration.Status.PhaseHistory, historyEntry)
	}

	// Clear current phase state
	migration.Status.CurrentPhaseState = nil
}

// InitiateRollback initiates a rollback
func (s *StateMachine) InitiateRollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration, phaseList []phases.Phase) error {
	logger := klog.FromContext(ctx)

	// Prominent rollback logging
	logger.Info("========================================")
	logger.Info("ROLLBACK INITIATED")
	logger.Info("========================================")

	// Get failure reason from current phase state if available
	failureReason := "unknown"
	if migration.Status.CurrentPhaseState != nil && migration.Status.CurrentPhaseState.Message != "" {
		failureReason = migration.Status.CurrentPhaseState.Message
	}

	logger.Info("Migration rollback starting",
		"migrationName", migration.Name,
		"currentPhase", migration.Status.Phase,
		"completedPhases", len(migration.Status.PhaseHistory),
		"failureReason", failureReason,
		"failedPhase", migration.Status.Phase)
	logger.Info("Failure details", "error", failureReason)

	// Update phase to rolling back
	migration.Status.Phase = migrationv1alpha1.PhaseRollingBack

	// Iterate through completed phases in reverse order
	for i := len(migration.Status.PhaseHistory) - 1; i >= 0; i-- {
		historyEntry := migration.Status.PhaseHistory[i]

		// Skip failed or skipped phases
		if historyEntry.Status != migrationv1alpha1.PhaseStatusCompleted {
			continue
		}

		// Find phase implementation
		var phaseImpl phases.Phase
		for _, p := range phaseList {
			if p.Name() == historyEntry.Phase {
				phaseImpl = p
				break
			}
		}

		if phaseImpl == nil {
			logger.Info("Phase implementation not found for rollback, skipping",
				"phase", historyEntry.Phase)
			continue
		}

		logger.Info("Rolling back phase", "phase", historyEntry.Phase)

		// Execute rollback
		if err := phaseImpl.Rollback(ctx, migration); err != nil {
			logger.Error(err, "Failed to rollback phase", "phase", historyEntry.Phase)
			// Continue with other rollbacks
		}
	}

	// Re-enable CVO as final step in rollback
	logger.Info("Re-enabling CVO as final rollback step")
	kubeClient := s.phaseExecutor.GetKubeClient()
	deployment, err := kubeClient.AppsV1().
		Deployments("openshift-cluster-version").
		Get(ctx, "cluster-version-operator", metav1.GetOptions{})
	if err != nil {
		logger.Error(err, "Failed to get CVO deployment during rollback")
	} else if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0 {
		replicas := int32(1)
		deployment.Spec.Replicas = &replicas
		_, err = kubeClient.AppsV1().
			Deployments("openshift-cluster-version").
			Update(ctx, deployment, metav1.UpdateOptions{})
		if err != nil {
			logger.Error(err, "Failed to re-enable CVO during rollback")
		} else {
			logger.Info("Successfully re-enabled CVO in rollback")
		}
	}

	// Update phase to rollback completed
	migration.Status.Phase = migrationv1alpha1.PhaseRollbackCompleted
	now := metav1.Now()
	migration.Status.CompletionTime = &now

	logger.Info("========================================")
	logger.Info("ROLLBACK COMPLETED")
	logger.Info("========================================")
	logger.Info("Migration rollback finished",
		"migrationName", migration.Name,
		"finalPhase", migration.Status.Phase,
		"phasesRolledBack", len(migration.Status.PhaseHistory))
	return nil
}

// MarkPhaseForApproval marks a phase as requiring approval
func (s *StateMachine) MarkPhaseForApproval(migration *migrationv1alpha1.VmwareCloudFoundationMigration, phase migrationv1alpha1.MigrationPhase, message string) {
	phaseState := &migrationv1alpha1.PhaseState{
		Name:             phase,
		Status:           migrationv1alpha1.PhaseStatusPending,
		Progress:         0,
		Message:          message,
		RequiresApproval: true,
		Approved:         false,
	}
	migration.Status.CurrentPhaseState = phaseState
}

// ApprovePhase approves a phase for execution
func (s *StateMachine) ApprovePhase(migration *migrationv1alpha1.VmwareCloudFoundationMigration, phase migrationv1alpha1.MigrationPhase) error {
	if migration.Status.CurrentPhaseState == nil {
		return fmt.Errorf("no current phase state")
	}

	if migration.Status.CurrentPhaseState.Name != phase {
		return fmt.Errorf("current phase is %s, not %s", migration.Status.CurrentPhaseState.Name, phase)
	}

	if !migration.Status.CurrentPhaseState.RequiresApproval {
		return fmt.Errorf("phase does not require approval")
	}

	migration.Status.CurrentPhaseState.Approved = true
	return nil
}

// UpdatePhaseProgress updates the progress of the current phase
func (s *StateMachine) UpdatePhaseProgress(migration *migrationv1alpha1.VmwareCloudFoundationMigration, progress int32, message string) {
	if migration.Status.CurrentPhaseState != nil {
		migration.Status.CurrentPhaseState.Progress = progress
		migration.Status.CurrentPhaseState.Message = message
	}
}

// ShouldRequeue determines if the migration should be requeued
func (s *StateMachine) ShouldRequeue(migration *migrationv1alpha1.VmwareCloudFoundationMigration, result *phases.PhaseResult) (bool, time.Duration) {
	// Requeue if phase wants to be requeued
	if result != nil && result.RequeueAfter > 0 {
		return true, result.RequeueAfter
	}

	// Requeue if waiting for approval
	if migration.Status.CurrentPhaseState != nil &&
		migration.Status.CurrentPhaseState.RequiresApproval &&
		!migration.Status.CurrentPhaseState.Approved {
		return true, 30 * time.Second
	}

	// Requeue if migration is running
	if migration.Spec.State == migrationv1alpha1.MigrationStateRunning &&
		migration.Status.Phase != migrationv1alpha1.PhaseCompleted &&
		migration.Status.Phase != migrationv1alpha1.PhaseFailed {
		return true, 10 * time.Second
	}

	return false, 0
}
