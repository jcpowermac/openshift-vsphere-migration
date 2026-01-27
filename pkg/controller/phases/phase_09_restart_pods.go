package phases

import (
	"context"
	"time"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vsphere-migration-controller/pkg/openshift"
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
func (p *RestartPodsPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	return nil
}

// Execute runs the phase
func (p *RestartPodsPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

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

	// Wait for pods to be ready
	logger.Info("Waiting for vSphere pods to be ready")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Waiting for vSphere pods to be ready",
		string(p.Name()))

	if err := p.podManager.WaitForVSpherePodsReady(ctx, 10*time.Minute); err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "vSphere pods did not become ready: " + err.Error(),
			Logs:    logs,
		}, err
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
func (p *RestartPodsPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rollback for RestartPods phase - no action needed")
	// Pods will automatically restart if needed after configuration rollback
	return nil
}
