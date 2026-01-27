package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vsphere-migration-controller/pkg/controller/phases"
	"github.com/openshift/vsphere-migration-controller/pkg/util"
)

// syncMigration is the main reconciliation loop
func (c *MigrationController) syncMigration(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	logger := klog.FromContext(ctx).WithValues("migration", migration.Name, "namespace", migration.Namespace)
	ctx = klog.NewContext(ctx, logger)

	logger.Info("Reconciling migration", "phase", migration.Status.Phase, "state", migration.Spec.State)

	// Initialize status if needed
	if migration.Status.Phase == migrationv1alpha1.PhaseNone {
		migration.Status.Phase = migrationv1alpha1.PhasePreflight
		migration.Status.PhaseHistory = make([]migrationv1alpha1.PhaseHistoryEntry, 0)
		migration.Status.BackupManifests = make([]migrationv1alpha1.BackupManifest, 0)
		now := metav1.Now()
		migration.Status.StartTime = &now
	}

	// Handle different migration states
	switch migration.Spec.State {
	case migrationv1alpha1.MigrationStatePending:
		logger.Info("Migration is pending, waiting for state to be set to Running")
		util.SetCondition(migration, migrationv1alpha1.ConditionReconciled, metav1.ConditionTrue,
			migrationv1alpha1.ReasonReconcileSucceeded, "Migration is pending")
		return nil

	case migrationv1alpha1.MigrationStatePaused:
		logger.Info("Migration is paused")
		util.SetCondition(migration, migrationv1alpha1.ConditionReconciled, metav1.ConditionTrue,
			migrationv1alpha1.ReasonReconcileSucceeded, "Migration is paused")
		return nil

	case migrationv1alpha1.MigrationStateRollback:
		logger.Info("Initiating rollback")
		if err := c.stateMachine.InitiateRollback(ctx, migration, c.getAllPhases()); err != nil {
			util.SetCondition(migration, migrationv1alpha1.ConditionReconciled, metav1.ConditionFalse,
				migrationv1alpha1.ReasonReconcileFailed, fmt.Sprintf("Rollback failed: %v", err))
			return err
		}
		util.SetCondition(migration, migrationv1alpha1.ConditionReconciled, metav1.ConditionTrue,
			migrationv1alpha1.ReasonReconcileSucceeded, "Rollback completed")
		return nil

	case migrationv1alpha1.MigrationStateRunning:
		// Continue with migration execution
	}

	// Check if migration is already completed
	if migration.Status.Phase == migrationv1alpha1.PhaseCompleted {
		logger.Info("Migration already completed")
		util.SetCondition(migration, migrationv1alpha1.ConditionReconciled, metav1.ConditionTrue,
			migrationv1alpha1.ReasonCompleted, "Migration completed successfully")
		return nil
	}

	// Get current phase
	currentPhase := migration.Status.Phase
	phase := c.getPhaseImplementation(currentPhase)
	if phase == nil {
		return fmt.Errorf("no implementation found for phase %s", currentPhase)
	}

	// Check if phase should be executed
	if !c.stateMachine.ShouldExecutePhase(migration, currentPhase) {
		logger.Info("Phase should not be executed yet", "phase", currentPhase)
		c.stateMachine.MarkPhaseForApproval(migration, currentPhase, "Waiting for approval")
		util.SetCondition(migration, migrationv1alpha1.ConditionReconciled, metav1.ConditionTrue,
			migrationv1alpha1.ReasonReconcileSucceeded, "Waiting for phase approval")
		return nil
	}

	// Execute phase
	logger.Info("Executing phase", "phase", currentPhase)
	util.SetCondition(migration, migrationv1alpha1.ConditionProgressing, metav1.ConditionTrue,
		migrationv1alpha1.ReasonProgressing, fmt.Sprintf("Executing phase %s", currentPhase))

	result, err := c.phaseExecutor.ExecutePhase(ctx, phase, migration)
	if err != nil {
		logger.Error(err, "Phase execution failed", "phase", currentPhase)

		// Record failure
		c.stateMachine.RecordPhaseCompletion(migration, currentPhase, result)
		migration.Status.Phase = migrationv1alpha1.PhaseFailed

		// Check if should rollback automatically
		if migration.Spec.RollbackOnFailure {
			logger.Info("Triggering automatic rollback")
			if rollbackErr := c.stateMachine.InitiateRollback(ctx, migration, c.getAllPhases()); rollbackErr != nil {
				logger.Error(rollbackErr, "Automatic rollback failed")
			}
		}

		util.SetCondition(migration, migrationv1alpha1.ConditionReconciled, metav1.ConditionFalse,
			migrationv1alpha1.ReasonReconcileFailed, fmt.Sprintf("Phase %s failed: %v", currentPhase, err))
		return err
	}

	// Record phase completion
	c.stateMachine.RecordPhaseCompletion(migration, currentPhase, result)

	// Move to next phase
	nextPhase, err := c.stateMachine.GetNextPhase(migration)
	if err != nil {
		return err
	}

	if nextPhase == migrationv1alpha1.PhaseCompleted {
		logger.Info("All phases completed successfully")
		migration.Status.Phase = migrationv1alpha1.PhaseCompleted
		now := metav1.Now()
		migration.Status.CompletionTime = &now
		util.SetCondition(migration, migrationv1alpha1.ConditionReconciled, metav1.ConditionTrue,
			migrationv1alpha1.ReasonCompleted, "Migration completed successfully")
		util.SetCondition(migration, migrationv1alpha1.ConditionProgressing, metav1.ConditionFalse,
			migrationv1alpha1.ReasonCompleted, "Migration completed")
	} else {
		migration.Status.Phase = nextPhase
		logger.Info("Moving to next phase", "phase", nextPhase)
		util.SetCondition(migration, migrationv1alpha1.ConditionReconciled, metav1.ConditionTrue,
			migrationv1alpha1.ReasonReconcileSucceeded, fmt.Sprintf("Moved to phase %s", nextPhase))
	}

	// Check if should requeue
	shouldRequeue, requeueAfter := c.stateMachine.ShouldRequeue(migration, result)
	if shouldRequeue {
		logger.V(2).Info("Requeuing migration", "after", requeueAfter)
		time.Sleep(requeueAfter)
	}

	return nil
}

// getPhaseImplementation returns the phase implementation for a given phase
func (c *MigrationController) getPhaseImplementation(phase migrationv1alpha1.MigrationPhase) phases.Phase {
	// Map phases to implementations
	switch phase {
	case migrationv1alpha1.PhasePreflight:
		return phases.NewPreflightPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseBackup:
		return phases.NewBackupPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseDisableCVO:
		return phases.NewDisableCVOPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseUpdateSecrets:
		return phases.NewUpdateSecretsPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseCreateTags:
		return phases.NewCreateTagsPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseCreateFolder:
		return phases.NewCreateFolderPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseUpdateInfrastructure:
		return phases.NewUpdateInfrastructurePhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseUpdateConfig:
		return phases.NewUpdateConfigPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseRestartPods:
		return phases.NewRestartPodsPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseMonitorHealth:
		return phases.NewMonitorHealthPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseCreateWorkers:
		return phases.NewCreateWorkersPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseRecreateCPMS:
		return phases.NewRecreateCPMSPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseScaleOldMachines:
		return phases.NewScaleOldMachinesPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseCleanup:
		return phases.NewCleanupPhase(c.phaseExecutor)
	case migrationv1alpha1.PhaseVerify:
		return phases.NewVerifyPhase(c.phaseExecutor)
	default:
		return nil
	}
}

// getAllPhases returns all phase implementations
func (c *MigrationController) getAllPhases() []phases.Phase {
	return []phases.Phase{
		phases.NewPreflightPhase(c.phaseExecutor),
		phases.NewBackupPhase(c.phaseExecutor),
		phases.NewDisableCVOPhase(c.phaseExecutor),
		phases.NewUpdateSecretsPhase(c.phaseExecutor),
		phases.NewCreateTagsPhase(c.phaseExecutor),
		phases.NewCreateFolderPhase(c.phaseExecutor),
		phases.NewUpdateInfrastructurePhase(c.phaseExecutor),
		phases.NewUpdateConfigPhase(c.phaseExecutor),
		phases.NewRestartPodsPhase(c.phaseExecutor),
		phases.NewMonitorHealthPhase(c.phaseExecutor),
		phases.NewCreateWorkersPhase(c.phaseExecutor),
		phases.NewRecreateCPMSPhase(c.phaseExecutor),
		phases.NewScaleOldMachinesPhase(c.phaseExecutor),
		phases.NewCleanupPhase(c.phaseExecutor),
		phases.NewVerifyPhase(c.phaseExecutor),
	}
}
