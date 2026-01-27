package backup

import (
	"context"
	"encoding/base64"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
)

// RestoreManager manages resource restoration
type RestoreManager struct {
	client client.Client
	scheme *runtime.Scheme
}

// NewRestoreManager creates a new restore manager
func NewRestoreManager(client client.Client, scheme *runtime.Scheme) *RestoreManager {
	return &RestoreManager{
		client: client,
		scheme: scheme,
	}
}

// RestoreResource restores a resource from a backup manifest
func (m *RestoreManager) RestoreResource(ctx context.Context, backup *migrationv1alpha1.BackupManifest) error {
	logger := klog.FromContext(ctx)

	// Decode base64
	yamlData, err := base64.StdEncoding.DecodeString(backup.BackupData)
	if err != nil {
		return fmt.Errorf("failed to decode backup data: %w", err)
	}

	// Unmarshal YAML to unstructured object
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(yamlData, obj); err != nil {
		return fmt.Errorf("failed to unmarshal YAML: %w", err)
	}

	logger.Info("Restoring resource",
		"resourceType", backup.ResourceType,
		"name", backup.Name,
		"namespace", backup.Namespace)

	// Update the resource
	if err := m.client.Update(ctx, obj); err != nil {
		return fmt.Errorf("failed to update resource: %w", err)
	}

	logger.Info("Successfully restored resource",
		"resourceType", backup.ResourceType,
		"name", backup.Name)

	return nil
}

// RestoreAllBackups restores all backups from a migration
func (m *RestoreManager) RestoreAllBackups(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Restoring all backups", "count", len(migration.Status.BackupManifests))

	// Restore in reverse order (most recent first)
	for i := len(migration.Status.BackupManifests) - 1; i >= 0; i-- {
		backup := migration.Status.BackupManifests[i]

		if err := m.RestoreResource(ctx, &backup); err != nil {
			logger.Error(err, "Failed to restore resource",
				"resourceType", backup.ResourceType,
				"name", backup.Name)
			// Continue with other restores
			continue
		}
	}

	logger.Info("Completed restoring all backups")
	return nil
}
