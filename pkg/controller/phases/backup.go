package phases

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
)

// BackupPhase backs up critical resources
type BackupPhase struct {
	executor *PhaseExecutor
}

// NewBackupPhase creates a new backup phase
func NewBackupPhase(executor *PhaseExecutor) *BackupPhase {
	return &BackupPhase{executor: executor}
}

// Name returns the phase name
func (p *BackupPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseBackup
}

// Validate checks if the phase can be executed
func (p *BackupPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	return nil
}

// Execute runs the phase
func (p *BackupPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Backing up critical resources")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Backing up critical resources", string(p.Name()))

	// Backup Infrastructure CRD
	infra, err := p.executor.infraManager.Get(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get infrastructure: " + err.Error(),
			Logs:    logs,
		}, err
	}

	infraBackup, err := p.executor.backupManager.BackupResource(ctx, client.Object(infra), "Infrastructure")
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to backup infrastructure: " + err.Error(),
			Logs:    logs,
		}, err
	}
	p.executor.backupManager.AddBackupToMigration(migration, infraBackup)

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Backed up Infrastructure CRD", string(p.Name()))

	// Backup vsphere-creds secret
	secret, err := p.executor.secretManager.GetVSphereCredsSecret(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get vsphere-creds secret: " + err.Error(),
			Logs:    logs,
		}, err
	}

	secretBackup, err := p.executor.backupManager.BackupResource(ctx, client.Object(secret), "Secret")
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to backup secret: " + err.Error(),
			Logs:    logs,
		}, err
	}
	p.executor.backupManager.AddBackupToMigration(migration, secretBackup)

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Backed up vsphere-creds secret", string(p.Name()))

	// Backup cloud-provider-config ConfigMap
	cm, err := p.executor.kubeClient.CoreV1().ConfigMaps("openshift-config").Get(ctx, "cloud-provider-config", metav1.GetOptions{})
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get cloud-provider-config: " + err.Error(),
			Logs:    logs,
		}, err
	}

	cmBackup, err := p.executor.backupManager.BackupResource(ctx, client.Object(cm), "ConfigMap")
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to backup ConfigMap: " + err.Error(),
			Logs:    logs,
		}, err
	}
	p.executor.backupManager.AddBackupToMigration(migration, cmBackup)

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Backed up cloud-provider-config", string(p.Name()))

	// Note: CPMS is not backed up as it will be recreated with new configuration
	// after infrastructure update. The new CPMS will use the updated failure domains.

	// TODO: Backup machines

	logger.Info("Successfully backed up all critical resources")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Successfully backed up all critical resources", string(p.Name()))

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully backed up all critical resources",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *BackupPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	// Backup phase doesn't modify resources, no rollback needed
	return nil
}
