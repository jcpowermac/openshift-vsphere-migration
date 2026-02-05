package phases

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/openshift"
)

// RestartPodsPhase restarts vSphere-related pods
type RestartPodsPhase struct {
	executor   *PhaseExecutor
	podManager *openshift.PodManager
}

// NewRestartPodsPhase creates a new restart pods phase
func NewRestartPodsPhase(executor *PhaseExecutor) *RestartPodsPhase {
	return &RestartPodsPhase{
		executor:   executor,
		podManager: openshift.NewPodManager(executor.kubeClient),
	}
}

// Name returns the phase name
func (p *RestartPodsPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseRestartPods
}

// Validate checks if the phase can be executed
func (p *RestartPodsPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	return nil
}

// Execute runs the phase
func (p *RestartPodsPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	// Check if this is a resume (pods already restarted, just polling for readiness)
	isResume := migration.Status.CurrentPhaseState != nil &&
		migration.Status.CurrentPhaseState.Name == p.Name() &&
		migration.Status.CurrentPhaseState.Status == migrationv1alpha1.PhaseStatusRunning

	if !isResume {
		// First execution - restart pods
		logger.Info("Restarting vSphere-related pods")
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Restarting vSphere-related pods", string(p.Name()))

		// Restart vSphere pods
		if err := p.podManager.RestartVSpherePods(ctx); err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "Failed to restart vSphere pods: " + err.Error(),
				Logs:    logs,
			}, err
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			"Triggered restart of vSphere pods",
			string(p.Name()))
	} else {
		logger.Info("Resuming pod readiness check")
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			"Resuming pod readiness check",
			string(p.Name()))
	}

	// Check if pods are ready (non-blocking to avoid leader election timeout)
	logger.Info("Checking vSphere pods readiness")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Checking vSphere pods readiness",
		string(p.Name()))

	status, err := p.podManager.CheckVSpherePodsReady(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to check vSphere pods: " + err.Error(),
			Logs:    logs,
		}, err
	}

	if !status.AllReady {
		msg := fmt.Sprintf("Waiting for vSphere pods: %s", status.NotReadyReason)
		logger.Info(msg)
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, msg, string(p.Name()))

		return &PhaseResult{
			Status:       migrationv1alpha1.PhaseStatusRunning,
			Message:      msg,
			Progress:     50,
			Logs:         logs,
			RequeueAfter: 15 * time.Second,
		}, nil
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"All vSphere pods are ready",
		string(p.Name()))

	logger.Info("Successfully restarted all vSphere pods")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully restarted all vSphere pods",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *RestartPodsPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rollback for RestartPods phase - no action needed")
	// Pods will automatically restart if needed after configuration rollback
	return nil
}
