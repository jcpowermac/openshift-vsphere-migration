package util

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
)

// SetCondition sets a condition on the migration status
func SetCondition(migration *migrationv1alpha1.VSphereMigration, conditionType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()

	// Find existing condition
	for i := range migration.Status.Conditions {
		if migration.Status.Conditions[i].Type == conditionType {
			// Update existing condition
			migration.Status.Conditions[i].Status = status
			migration.Status.Conditions[i].Reason = reason
			migration.Status.Conditions[i].Message = message
			migration.Status.Conditions[i].LastTransitionTime = now
			migration.Status.Conditions[i].ObservedGeneration = migration.Generation
			return
		}
	}

	// Add new condition
	migration.Status.Conditions = append(migration.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: migration.Generation,
	})
}

// IsConditionTrue checks if a condition is true
func IsConditionTrue(migration *migrationv1alpha1.VSphereMigration, conditionType string) bool {
	for _, condition := range migration.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Status == metav1.ConditionTrue
		}
	}
	return false
}

// GetCondition gets a condition by type
func GetCondition(migration *migrationv1alpha1.VSphereMigration, conditionType string) *metav1.Condition {
	for i := range migration.Status.Conditions {
		if migration.Status.Conditions[i].Type == conditionType {
			return &migration.Status.Conditions[i]
		}
	}
	return nil
}
