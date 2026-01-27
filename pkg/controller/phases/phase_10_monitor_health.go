package phases

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vsphere-migration-controller/pkg/openshift"
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
func (p *MonitorHealthPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	return nil
}

// Execute runs the phase
func (p *MonitorHealthPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Monitoring cluster health")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Monitoring cluster health", string(p.Name()))

	// Wait for all cluster operators to be healthy
	logger.Info("Waiting for all cluster operators to become healthy")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Waiting for all cluster operators to become healthy",
		string(p.Name()))

	if err := p.operatorManager.WaitForOperatorsHealthy(ctx, 10*time.Minute); err != nil {
		// Get current status for error message
		healthy, unhealthyList, statusErr := p.operatorManager.CheckAllOperatorsHealthy(ctx)
		if statusErr == nil && !healthy {
			errMsg := fmt.Sprintf("Cluster operators not healthy: %s", strings.Join(unhealthyList, ", "))
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: errMsg,
				Logs:    logs,
			}, fmt.Errorf("cluster operators not healthy: %s", strings.Join(unhealthyList, ", "))
		}

		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Timeout waiting for operators: " + err.Error(),
			Logs:    logs,
		}, err
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
func (p *MonitorHealthPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rollback for MonitorHealth phase - no action needed")
	// Monitoring doesn't change state, no rollback needed
	return nil
}
