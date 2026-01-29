package backup

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
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

	// Check if client is initialized
	if m.client == nil {
		return fmt.Errorf("restore manager not properly initialized: client is nil")
	}

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

	// Fetch current resource to get up-to-date ResourceVersion
	current := &unstructured.Unstructured{}
	current.SetAPIVersion(obj.GetAPIVersion())
	current.SetKind(obj.GetKind())

	key := client.ObjectKey{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	if err := m.client.Get(ctx, key, current); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to get current resource: %w", err)
		}
		// Resource doesn't exist - try to create it
		logger.Info("Resource doesn't exist, attempting to create from backup")
		obj.SetResourceVersion("")
		if err := m.client.Create(ctx, obj); err != nil {
			return fmt.Errorf("failed to create resource: %w", err)
		}
		logger.Info("Successfully created resource from backup",
			"resourceType", backup.ResourceType,
			"name", backup.Name)
		return nil
	}

	// Use current ResourceVersion with backup spec data
	obj.SetResourceVersion(current.GetResourceVersion())

	if err := m.client.Update(ctx, obj); err != nil {
		return fmt.Errorf("failed to update resource: %w", err)
	}

	logger.Info("Successfully restored resource",
		"resourceType", backup.ResourceType,
		"name", backup.Name)

	return nil
}

// RestoreResourceWithRetry restores a resource with exponential backoff retry
func (m *RestoreManager) RestoreResourceWithRetry(ctx context.Context, backup *migrationv1alpha1.BackupManifest) error {
	backoff := wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
		Steps:    3,
		Cap:      10 * time.Second,
	}

	var lastErr error
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		if err := m.RestoreResource(ctx, backup); err != nil {
			lastErr = err
			return false, nil // Retry
		}
		return true, nil // Success
	})

	if err != nil {
		return fmt.Errorf("restore failed after retries: %w", lastErr)
	}
	return nil
}

// RestoreAllBackups restores all backups from a migration
func (m *RestoreManager) RestoreAllBackups(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Restoring all backups", "count", len(migration.Status.BackupManifests))

	var errs []error

	// Restore in reverse order (most recent first)
	for i := len(migration.Status.BackupManifests) - 1; i >= 0; i-- {
		backup := migration.Status.BackupManifests[i]

		if err := m.RestoreResourceWithRetry(ctx, &backup); err != nil {
			logger.Error(err, "Failed to restore resource",
				"resourceType", backup.ResourceType,
				"name", backup.Name)
			errs = append(errs, fmt.Errorf("restore %s/%s: %w",
				backup.ResourceType, backup.Name, err))
			continue
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("restore completed with %d errors: %w", len(errs), errors.Join(errs...))
	}

	logger.Info("Completed restoring all backups")
	return nil
}
