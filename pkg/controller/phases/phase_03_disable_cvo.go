package phases

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
)

const (
	CVONamespace = "openshift-cluster-version"
	CVOName      = "cluster-version-operator"
)

// DisableCVOPhase scales down the cluster-version-operator
type DisableCVOPhase struct {
	executor *PhaseExecutor
}

// NewDisableCVOPhase creates a new disable CVO phase
func NewDisableCVOPhase(executor *PhaseExecutor) *DisableCVOPhase {
	return &DisableCVOPhase{executor: executor}
}

// Name returns the phase name
func (p *DisableCVOPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseDisableCVO
}

// Validate checks if the phase can be executed
func (p *DisableCVOPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	return nil
}

// Execute runs the phase
func (p *DisableCVOPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Scaling down cluster-version-operator")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Scaling down cluster-version-operator", string(p.Name()))

	// Get deployment
	deployment, err := p.executor.kubeClient.AppsV1().Deployments(CVONamespace).Get(ctx, CVOName, metav1.GetOptions{})
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get CVO deployment: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("Current CVO replicas: %d", *deployment.Spec.Replicas),
		string(p.Name()))

	// Scale to 0
	replicas := int32(0)
	deployment.Spec.Replicas = &replicas

	_, err = p.executor.kubeClient.AppsV1().Deployments(CVONamespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to scale down CVO: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Successfully scaled down CVO to 0 replicas",
		string(p.Name()))

	logger.Info("Successfully scaled down CVO")

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully scaled down CVO",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *DisableCVOPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back DisableCVO phase - re-enabling CVO")

	// Get deployment
	deployment, err := p.executor.kubeClient.AppsV1().Deployments(CVONamespace).Get(ctx, CVOName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Scale back to 1
	replicas := int32(1)
	deployment.Spec.Replicas = &replicas

	_, err = p.executor.kubeClient.AppsV1().Deployments(CVONamespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	logger.Info("Successfully re-enabled CVO")
	return nil
}
