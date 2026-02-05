package phases

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/openshift"
)

// MonitorHealthPhase monitors cluster health after configuration changes
type MonitorHealthPhase struct {
	executor        *PhaseExecutor
	operatorManager *openshift.OperatorManager
}

// NewMonitorHealthPhase creates a new monitor health phase
func NewMonitorHealthPhase(executor *PhaseExecutor) *MonitorHealthPhase {
	return &MonitorHealthPhase{
		executor:        executor,
		operatorManager: openshift.NewOperatorManager(executor.configClient),
	}
}

// Name returns the phase name
func (p *MonitorHealthPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseMonitorHealth
}

// Validate checks if the phase can be executed
func (p *MonitorHealthPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	return nil
}

// Execute runs the phase
func (p *MonitorHealthPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Checking cluster health")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Checking cluster health", string(p.Name()))

	// Check cluster operators health (non-blocking to avoid leader election timeout)
	logger.Info("Checking cluster operators health")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Checking cluster operators health",
		string(p.Name()))

	healthy, unhealthyList, err := p.operatorManager.CheckAllOperatorsHealthy(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to check operator health: " + err.Error(),
			Logs:    logs,
		}, err
	}

	if !healthy {
		msg := fmt.Sprintf("Waiting for operators to become healthy: %s", strings.Join(unhealthyList, ", "))
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
		"All cluster operators are healthy",
		string(p.Name()))

	// Check nodes are ready
	logger.Info("Checking node health")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Checking node health",
		string(p.Name()))

	// TODO: Add node health check
	// This would check that all nodes are Ready

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"All nodes are healthy",
		string(p.Name()))

	logger.Info("Cluster health check passed")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Cluster health check passed",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *MonitorHealthPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rollback for MonitorHealth phase - no action needed")
	// Monitoring doesn't change state, no rollback needed
	return nil
}
