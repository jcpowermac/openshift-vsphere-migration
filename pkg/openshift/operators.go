package openshift

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

var (
	// ExcludedOperators are operators that are allowed to be degraded during migration
	ExcludedOperators = map[string]bool{
		"machine-config": true, // Expected to be degraded when Infrastructure has multiple vcenters
	}
)

// OperatorManager manages cluster operator operations
type OperatorManager struct {
	client configclient.Interface
}

// NewOperatorManager creates a new operator manager
func NewOperatorManager(client configclient.Interface) *OperatorManager {
	return &OperatorManager{client: client}
}

// CheckAllOperatorsHealthy checks if all cluster operators are healthy
func (m *OperatorManager) CheckAllOperatorsHealthy(ctx context.Context) (bool, []string, error) {
	logger := klog.FromContext(ctx)

	operators, err := m.client.ConfigV1().ClusterOperators().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, nil, fmt.Errorf("failed to list cluster operators: %w", err)
	}

	var unhealthyOperators []string

	for _, operator := range operators.Items {
		available := false
		degraded := false
		progressing := false

		for _, condition := range operator.Status.Conditions {
			switch condition.Type {
			case configv1.OperatorAvailable:
				available = condition.Status == configv1.ConditionTrue
			case configv1.OperatorDegraded:
				degraded = condition.Status == configv1.ConditionTrue
			case configv1.OperatorProgressing:
				progressing = condition.Status == configv1.ConditionTrue
			}
		}

		// Skip excluded operators
		if ExcludedOperators[operator.Name] {
			if !available || degraded {
				logger.V(1).Info("Excluded operator is unhealthy (expected during migration)",
					"operator", operator.Name,
					"available", available,
					"degraded", degraded,
					"progressing", progressing)
			}
			continue
		}

		if !available || degraded {
			unhealthyOperators = append(unhealthyOperators,
				fmt.Sprintf("%s (available=%v, degraded=%v, progressing=%v)",
					operator.Name, available, degraded, progressing))
			logger.Info("Unhealthy operator detected",
				"operator", operator.Name,
				"available", available,
				"degraded", degraded,
				"progressing", progressing)
		}
	}

	if len(unhealthyOperators) > 0 {
		return false, unhealthyOperators, nil
	}

	logger.Info("All cluster operators are healthy", "count", len(operators.Items))
	return true, nil, nil
}

// WaitForOperatorsHealthy waits for all operators to become healthy
func (m *OperatorManager) WaitForOperatorsHealthy(ctx context.Context, timeout time.Duration) error {
	logger := klog.FromContext(ctx)

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for operators to become healthy")
			}

			healthy, unhealthy, err := m.CheckAllOperatorsHealthy(ctx)
			if err != nil {
				logger.V(2).Info("Error checking operator health", "error", err)
				continue
			}

			if healthy {
				logger.Info("All operators are healthy")
				return nil
			}

			logger.V(2).Info("Waiting for operators to become healthy",
				"unhealthy", len(unhealthy))
		}
	}
}

// GetOperator gets a specific cluster operator
func (m *OperatorManager) GetOperator(ctx context.Context, name string) (*configv1.ClusterOperator, error) {
	return m.client.ConfigV1().ClusterOperators().Get(ctx, name, metav1.GetOptions{})
}

// WaitForOperatorCondition waits for a specific operator condition
func (m *OperatorManager) WaitForOperatorCondition(ctx context.Context, operatorName string, conditionType configv1.ClusterStatusConditionType, status configv1.ConditionStatus, timeout time.Duration) error {
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
				return fmt.Errorf("timeout waiting for operator %s condition %s=%s",
					operatorName, conditionType, status)
			}

			operator, err := m.GetOperator(ctx, operatorName)
			if err != nil {
				logger.V(2).Info("Error getting operator", "operator", operatorName, "error", err)
				continue
			}

			for _, condition := range operator.Status.Conditions {
				if condition.Type == conditionType && condition.Status == status {
					logger.Info("Operator condition met",
						"operator", operatorName,
						"condition", conditionType,
						"status", status)
					return nil
				}
			}

			logger.V(2).Info("Waiting for operator condition",
				"operator", operatorName,
				"condition", conditionType,
				"desired", status)
		}
	}
}

// IsOperatorHealthy checks if a specific operator is healthy
func (m *OperatorManager) IsOperatorHealthy(ctx context.Context, name string) (bool, string, error) {
	operator, err := m.GetOperator(ctx, name)
	if err != nil {
		return false, "", err
	}

	available := false
	degraded := false
	var message string

	for _, condition := range operator.Status.Conditions {
		switch condition.Type {
		case configv1.OperatorAvailable:
			available = condition.Status == configv1.ConditionTrue
			if !available {
				message = fmt.Sprintf("Not available: %s", condition.Message)
			}
		case configv1.OperatorDegraded:
			degraded = condition.Status == configv1.ConditionTrue
			if degraded {
				message = fmt.Sprintf("Degraded: %s", condition.Message)
			}
		}
	}

	healthy := available && !degraded
	return healthy, message, nil
}
