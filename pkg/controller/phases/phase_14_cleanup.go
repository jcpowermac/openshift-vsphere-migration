package phases

import (
	"context"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vsphere-migration-controller/pkg/openshift"
)

// CleanupPhase removes source vCenter configuration
type CleanupPhase struct {
	executor      *PhaseExecutor
	configManager *openshift.ConfigMapManager
	podManager    *openshift.PodManager
}

// NewCleanupPhase creates a new cleanup phase
func NewCleanupPhase(executor *PhaseExecutor) *CleanupPhase {
	return &CleanupPhase{
		executor:      executor,
		configManager: openshift.NewConfigMapManager(executor.kubeClient),
		podManager:    openshift.NewPodManager(executor.kubeClient),
	}
}

// Name returns the phase name
func (p *CleanupPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseCleanup
}

// Validate checks if the phase can be executed
func (p *CleanupPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	return nil
}

// Execute runs the phase
func (p *CleanupPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Cleaning up source vCenter configuration")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Cleaning up source vCenter configuration", string(p.Name()))

	// Get source vCenter from Infrastructure CRD
	sourceVC, err := p.executor.infraManager.GetSourceVCenter(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get source vCenter from Infrastructure: " + err.Error(),
			Logs:    logs,
		}, err
	}

	// Remove source vCenter from Infrastructure CRD
	logger.Info("Removing source vCenter from Infrastructure CRD", "server", sourceVC.Server)
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Removing source vCenter from Infrastructure CRD",
		string(p.Name()))

	infra, err := p.executor.infraManager.Get(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get Infrastructure: " + err.Error(),
			Logs:    logs,
		}, err
	}

	_, err = p.executor.infraManager.RemoveSourceVCenter(ctx, infra, sourceVC.Server)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to remove source vCenter from Infrastructure: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Removed source vCenter from Infrastructure CRD",
		string(p.Name()))

	// Remove source vCenter from cloud-provider-config
	logger.Info("Removing source vCenter from cloud-provider-config")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Removing source vCenter from cloud-provider-config",
		string(p.Name()))

	cm, err := p.configManager.GetCloudProviderConfig(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get cloud-provider-config: " + err.Error(),
			Logs:    logs,
		}, err
	}

	_, err = p.configManager.RemoveSourceVCenterFromConfig(ctx, cm, sourceVC.Server)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to remove source vCenter from config: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Removed source vCenter from cloud-provider-config",
		string(p.Name()))

	// Remove source vCenter credentials from secret
	logger.Info("Removing source vCenter credentials from secret")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Removing source vCenter credentials from secret",
		string(p.Name()))

	secret, err := p.executor.secretManager.GetVSphereCredsSecret(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get vsphere-creds secret: " + err.Error(),
			Logs:    logs,
		}, err
	}

	_, err = p.executor.secretManager.RemoveSourceVCenterCreds(ctx, secret, sourceVC.Server)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to remove source vCenter credentials: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Removed source vCenter credentials",
		string(p.Name()))

	// Restart vSphere pods to pick up new configuration
	logger.Info("Restarting vSphere pods to apply cleanup")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Restarting vSphere pods to apply cleanup",
		string(p.Name()))

	if err := p.podManager.RestartVSpherePods(ctx); err != nil {
		logger.Error(err, "Failed to restart vSphere pods")
		// Continue - not critical for cleanup
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Cleanup completed successfully",
		string(p.Name()))

	logger.Info("Successfully cleaned up source vCenter configuration")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully cleaned up source vCenter configuration",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *CleanupPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back Cleanup phase - restoring source vCenter configuration")

	// Restore Infrastructure from backup
	infraBackup, err := p.executor.backupManager.GetBackup(migration, "Infrastructure", "cluster", "")
	if err != nil {
		logger.Error(err, "Failed to get Infrastructure backup")
		return err
	}

	if err := p.executor.restoreManager.RestoreResource(ctx, infraBackup); err != nil {
		logger.Error(err, "Failed to restore Infrastructure")
		return err
	}

	// Restore cloud-provider-config from backup
	cmBackup, err := p.executor.backupManager.GetBackup(migration, "ConfigMap", "cloud-provider-config", "openshift-config")
	if err != nil {
		logger.Error(err, "Failed to get ConfigMap backup")
		return err
	}

	if err := p.executor.restoreManager.RestoreResource(ctx, cmBackup); err != nil {
		logger.Error(err, "Failed to restore ConfigMap")
		return err
	}

	// Restore secret from backup
	secretBackup, err := p.executor.backupManager.GetBackup(migration, "Secret", "vsphere-creds", "kube-system")
	if err != nil {
		logger.Error(err, "Failed to get Secret backup")
		return err
	}

	if err := p.executor.restoreManager.RestoreResource(ctx, secretBackup); err != nil {
		logger.Error(err, "Failed to restore Secret")
		return err
	}

	logger.Info("Successfully restored source vCenter configuration")
	return nil
}
