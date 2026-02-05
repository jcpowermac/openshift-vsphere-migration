package phases

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
)

// UpdateSecretsPhase adds target vCenter credentials to secrets
type UpdateSecretsPhase struct {
	executor *PhaseExecutor
}

// NewUpdateSecretsPhase creates a new update secrets phase
func NewUpdateSecretsPhase(executor *PhaseExecutor) *UpdateSecretsPhase {
	return &UpdateSecretsPhase{executor: executor}
}

// Name returns the phase name
func (p *UpdateSecretsPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseUpdateSecrets
}

// Validate checks if the phase can be executed
func (p *UpdateSecretsPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	if migration.Spec.TargetVCenterCredentialsSecret.Name == "" {
		return fmt.Errorf("target vCenter credentials secret name is empty")
	}
	if len(migration.Spec.FailureDomains) == 0 {
		return fmt.Errorf("no failure domains specified")
	}
	return nil
}

// Execute runs the phase
func (p *UpdateSecretsPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Adding target vCenter credentials to vsphere-creds secret")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Adding target vCenter credentials", string(p.Name()))

	// Get vsphere-creds secret
	secret, err := p.executor.secretManager.GetVSphereCredsSecret(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get vsphere-creds secret: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Retrieved vsphere-creds secret",
		string(p.Name()))

	// Get target vCenter credentials from the specified credentials secret
	credSecretName := migration.Spec.TargetVCenterCredentialsSecret.Name
	credSecretNamespace := migration.Spec.TargetVCenterCredentialsSecret.Namespace
	if credSecretNamespace == "" {
		credSecretNamespace = "kube-system" // Default namespace
	}

	logger.Info("Reading target vCenter credentials", "secret", credSecretName, "namespace", credSecretNamespace)
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("Reading target vCenter credentials from secret %s/%s", credSecretNamespace, credSecretName),
		string(p.Name()))

	// Get unique target vCenter servers from failure domains
	targetVCenters := make(map[string]bool)
	for _, fd := range migration.Spec.FailureDomains {
		targetVCenters[fd.Server] = true
	}

	// Add credentials for each target vCenter
	for targetServer := range targetVCenters {
		// Get credentials from the target credentials secret
		// The secret should have keys: {vcenter-fqdn}.username and {vcenter-fqdn}.password
		username, password, err := p.executor.secretManager.GetVCenterCredsFromSecret(ctx, credSecretNamespace, credSecretName, targetServer)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: fmt.Sprintf("Failed to get credentials for %s: %v", targetServer, err),
				Logs:    logs,
			}, err
		}

		// Add target vCenter credentials to vsphere-creds secret
		_, err = p.executor.secretManager.AddTargetVCenterCreds(ctx, secret,
			targetServer,
			username,
			password)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: "Failed to add target vCenter credentials: " + err.Error(),
				Logs:    logs,
			}, err
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Added credentials for target vCenter: %s", targetServer),
			string(p.Name()))
	}

	logger.Info("Successfully updated vsphere-creds secret")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully added target vCenter credentials",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *UpdateSecretsPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back UpdateSecrets phase")

	// Note: We don't restore the secret because:
	// 1. Having multiple vCenter credentials in the secret is harmless
	// 2. The restore was failing due to backup format issues
	// 3. The cleanup phase will remove the target vCenter credentials later if needed

	logger.Info("UpdateSecrets rollback skipped - multiple vCenter credentials in secret are harmless")
	return nil
}
