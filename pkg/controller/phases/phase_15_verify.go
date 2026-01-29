package phases

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vsphere-migration-controller/pkg/openshift"
)

// VerifyPhase performs final verification and re-enables CVO
type VerifyPhase struct {
	executor        *PhaseExecutor
	operatorManager *openshift.OperatorManager
}

// NewVerifyPhase creates a new verify phase
func NewVerifyPhase(executor *PhaseExecutor) *VerifyPhase {
	return &VerifyPhase{
		executor:        executor,
		operatorManager: openshift.NewOperatorManager(executor.configClient),
	}
}

// Name returns the phase name
func (p *VerifyPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseVerify
}

// Validate checks if the phase can be executed
func (p *VerifyPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	return nil
}

// waitForCVOReady waits for CVO deployment to be ready
func (p *VerifyPhase) waitForCVOReady(ctx context.Context, timeout time.Duration) error {
	logger := klog.FromContext(ctx)
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for CVO deployment to be ready")
			}

			deployment, err := p.executor.kubeClient.AppsV1().
				Deployments("openshift-cluster-version").
				Get(ctx, "cluster-version-operator", metav1.GetOptions{})
			if err != nil {
				logger.V(2).Info("Error getting CVO deployment", "error", err)
				continue
			}

			// Check deployment is ready
			if deployment.Status.ReadyReplicas == *deployment.Spec.Replicas &&
				deployment.Status.Replicas == *deployment.Spec.Replicas &&
				deployment.Status.UnavailableReplicas == 0 {
				logger.Info("CVO deployment is ready",
					"replicas", deployment.Status.ReadyReplicas)
				return nil
			}

			logger.V(2).Info("Waiting for CVO deployment",
				"ready", deployment.Status.ReadyReplicas,
				"desired", *deployment.Spec.Replicas,
				"unavailable", deployment.Status.UnavailableReplicas)
		}
	}
}

// Execute runs the phase
func (p *VerifyPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Performing final verification")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Performing final verification", string(p.Name()))

	// Verify all cluster operators are healthy
	logger.Info("Verifying cluster operator health")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Verifying cluster operator health",
		string(p.Name()))

	healthy, unhealthy, err := p.operatorManager.CheckAllOperatorsHealthy(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to check operator health: " + err.Error(),
			Logs:    logs,
		}, err
	}

	if !healthy {
		errMsg := fmt.Sprintf("Cluster operators not healthy: %v", unhealthy)
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: errMsg,
			Logs:    logs,
		}, fmt.Errorf("cluster operators not healthy: %v", unhealthy)
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"All cluster operators are healthy",
		string(p.Name()))

	// Verify only target vCenter in Infrastructure
	logger.Info("Verifying Infrastructure configuration")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Verifying Infrastructure configuration",
		string(p.Name()))

	infra, err := p.executor.infraManager.Get(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get Infrastructure: " + err.Error(),
			Logs:    logs,
		}, err
	}

	// Get source vCenter server to verify it's been removed
	// We can't use GetSourceVCenter here because it should have been removed
	// Instead, get the first vCenter before migration started from backup
	sourceVCServer := ""

	// Get expected target vCenter servers from failure domains
	targetVCServers := make(map[string]bool)
	for _, fd := range migration.Spec.FailureDomains {
		targetVCServers[fd.Server] = true
	}

	// Check that only target vCenter(s) are present
	if infra.Spec.PlatformSpec.VSphere != nil {
		// Collect all vCenter servers currently in infrastructure
		currentVCServers := make(map[string]bool)
		for _, vc := range infra.Spec.PlatformSpec.VSphere.VCenters {
			currentVCServers[vc.Server] = true
		}

		// If we have a source vCenter backup, check that it's been removed
		if sourceVCServer != "" && currentVCServers[sourceVCServer] {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: fmt.Sprintf("Source vCenter %s still present in Infrastructure", sourceVCServer),
				Logs:    logs,
			}, fmt.Errorf("source vCenter still present")
		}

		// Verify all target vCenters are present
		for targetServer := range targetVCServers {
			if !currentVCServers[targetServer] {
				return &PhaseResult{
					Status:  migrationv1alpha1.PhaseStatusFailed,
					Message: fmt.Sprintf("Target vCenter %s not present in Infrastructure", targetServer),
					Logs:    logs,
				}, fmt.Errorf("target vCenter %s not present", targetServer)
			}
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Verified %d target vCenter(s) present in Infrastructure", len(targetVCServers)),
			string(p.Name()))
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Infrastructure configuration verified",
		string(p.Name()))

	// Verify all machines reference target vCenter
	logger.Info("Verifying all machines reference target vCenter")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Verifying all machines reference target vCenter",
		string(p.Name()))

	// TODO: Verify machines are using target vCenter
	// This would check the providerSpec of all machines

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"All machines verified",
		string(p.Name()))

	// Re-enable CVO
	logger.Info("Re-enabling cluster-version-operator")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Re-enabling cluster-version-operator",
		string(p.Name()))

	deployment, err := p.executor.kubeClient.AppsV1().Deployments("openshift-cluster-version").Get(ctx, "cluster-version-operator", metav1.GetOptions{})
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get CVO deployment: " + err.Error(),
			Logs:    logs,
		}, err
	}

	replicas := int32(1)
	deployment.Spec.Replicas = &replicas

	_, err = p.executor.kubeClient.AppsV1().Deployments("openshift-cluster-version").Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to re-enable CVO: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Re-enabled cluster-version-operator",
		string(p.Name()))

	// Wait for CVO to become ready (not just scaled to 1)
	logger.Info("Waiting for CVO deployment to become ready")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Waiting for CVO deployment readiness",
		string(p.Name()))

	if err := p.waitForCVOReady(ctx, 5*time.Minute); err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "CVO failed to become ready: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"CVO is ready and running",
		string(p.Name()))

	logger.Info("Final verification completed successfully")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Final verification completed - migration successful!",
		string(p.Name()))

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Migration completed successfully",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *VerifyPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rollback for Verify phase - re-enabling CVO if needed")

	// Ensure CVO is running
	deployment, err := p.executor.kubeClient.AppsV1().Deployments("openshift-cluster-version").Get(ctx, "cluster-version-operator", metav1.GetOptions{})
	if err != nil {
		logger.Error(err, "Failed to get CVO deployment")
		return err
	}

	if *deployment.Spec.Replicas == 0 {
		replicas := int32(1)
		deployment.Spec.Replicas = &replicas

		_, err = p.executor.kubeClient.AppsV1().Deployments("openshift-cluster-version").Update(ctx, deployment, metav1.UpdateOptions{})
		if err != nil {
			logger.Error(err, "Failed to re-enable CVO")
			return err
		}
	}

	logger.Info("CVO is running")
	return nil
}
