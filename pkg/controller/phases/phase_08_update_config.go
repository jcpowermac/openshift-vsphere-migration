package phases

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/openshift"
)

// UpdateConfigPhase updates cloud-provider-config ConfigMap
type UpdateConfigPhase struct {
	executor      *PhaseExecutor
	configManager *openshift.ConfigMapManager
}

// NewUpdateConfigPhase creates a new update config phase
func NewUpdateConfigPhase(executor *PhaseExecutor) *UpdateConfigPhase {
	return &UpdateConfigPhase{
		executor:      executor,
		configManager: openshift.NewConfigMapManager(executor.kubeClient),
	}
}

// Name returns the phase name
func (p *UpdateConfigPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseUpdateConfig
}

// Validate checks if the phase can be executed
func (p *UpdateConfigPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	return nil
}

// Execute runs the phase
func (p *UpdateConfigPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Updating cloud-provider-config ConfigMap")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Updating cloud-provider-config", string(p.Name()))

	// Get current config
	cm, err := p.configManager.GetCloudProviderConfig(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get cloud-provider-config: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Retrieved cloud-provider-config ConfigMap",
		string(p.Name()))

	// Add target vCenter configuration
	_, err = p.configManager.AddTargetVCenterToConfig(ctx, cm, migration)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to add target vCenter to config: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Added target vCenter to cloud-provider-config",
		string(p.Name()))

	// Restart machine-config-operator to force ControllerConfig sync
	if err := p.syncControllerConfig(ctx); err != nil {
		logger.Error(err, "Failed to sync ControllerConfig - continuing")
		logs = AddLog(logs, migrationv1alpha1.LogLevelWarning,
			"Failed to restart machine-config-operator: "+err.Error(),
			string(p.Name()))
	} else {
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			"Restarted machine-config-operator to sync ControllerConfig",
			string(p.Name()))
	}

	logger.Info("Successfully updated cloud-provider-config")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully updated cloud-provider-config",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *UpdateConfigPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back UpdateConfig phase")

	// Restore ConfigMap from backup
	backup, err := p.executor.backupManager.GetBackup(migration, "ConfigMap", "cloud-provider-config", "openshift-config")
	if err != nil {
		logger.Error(err, "Failed to get ConfigMap backup")
		return err
	}

	if err := p.executor.restoreManager.RestoreResource(ctx, backup); err != nil {
		logger.Error(err, "Failed to restore ConfigMap")
		return err
	}

	logger.Info("Successfully restored cloud-provider-config from backup")
	return nil
}

// syncControllerConfig restarts machine-config-operator to force ControllerConfig resync
func (p *UpdateConfigPhase) syncControllerConfig(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	// The ControllerConfig is managed by machine-config-operator
	// Restart the operator pods to force reconciliation with updated cloud-provider-config
	logger.Info("Restarting machine-config-operator to sync ControllerConfig")

	namespace := "openshift-machine-config-operator"
	pods, err := p.executor.kubeClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	deletedCount := 0
	for i := range pods.Items {
		pod := &pods.Items[i]
		if strings.HasPrefix(pod.Name, "machine-config-operator-") {
			if err := p.executor.kubeClient.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
				logger.Error(err, "Failed to delete pod", "pod", pod.Name)
			} else {
				logger.Info("Deleted machine-config-operator pod", "pod", pod.Name)
				deletedCount++
			}
		}
	}

	if deletedCount == 0 {
		logger.Info("No machine-config-operator pods found to restart")
	} else {
		logger.Info("Restarted machine-config-operator pods", "count", deletedCount)
	}

	return nil
}
