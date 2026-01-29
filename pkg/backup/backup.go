package backup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
)

// BackupManager manages resource backups
type BackupManager struct {
	scheme *runtime.Scheme
}

// NewBackupManager creates a new backup manager
func NewBackupManager(scheme *runtime.Scheme) *BackupManager {
	return &BackupManager{scheme: scheme}
}

// getGVKForObject determines the GVK for a given object
func (m *BackupManager) getGVKForObject(obj client.Object) (schema.GroupVersionKind, error) {
	// First try to get GVK from the object itself
	gvk := obj.GetObjectKind().GroupVersionKind()
	if !gvk.Empty() {
		return gvk, nil
	}

	// Try to infer from scheme
	gvks, _, err := m.scheme.ObjectKinds(obj)
	if err == nil && len(gvks) > 0 {
		return gvks[0], nil
	}

	// Fall back to type assertions for well-known types
	switch obj.(type) {
	case *configv1.Infrastructure:
		return schema.GroupVersionKind{
			Group:   "config.openshift.io",
			Version: "v1",
			Kind:    "Infrastructure",
		}, nil
	case *corev1.Secret:
		return schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    "Secret",
		}, nil
	case *corev1.ConfigMap:
		return schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    "ConfigMap",
		}, nil
	default:
		return schema.GroupVersionKind{}, fmt.Errorf("cannot determine GVK for object of type %T", obj)
	}
}

// BackupResource backs up a Kubernetes resource
func (m *BackupManager) BackupResource(ctx context.Context, obj client.Object, resourceType string) (*migrationv1alpha1.BackupManifest, error) {
	logger := klog.FromContext(ctx)

	// Get the GVK from the object
	gvk, err := m.getGVKForObject(obj)
	if err != nil {
		return nil, fmt.Errorf("cannot determine GVK for object: %w", err)
	}

	// Convert to unstructured to ensure TypeMeta is included
	unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to unstructured: %w", err)
	}

	// Set apiVersion and kind explicitly
	unstructuredObj["apiVersion"] = gvk.GroupVersion().String()
	unstructuredObj["kind"] = gvk.Kind

	// Marshal the complete object
	jsonData, err := json.Marshal(unstructuredObj)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal resource to JSON: %w", err)
	}

	yamlData, err := yaml.JSONToYAML(jsonData)
	if err != nil {
		return nil, fmt.Errorf("failed to convert JSON to YAML: %w", err)
	}

	// Encode to base64
	encodedData := base64.StdEncoding.EncodeToString(yamlData)

	backup := &migrationv1alpha1.BackupManifest{
		ResourceType: resourceType,
		Name:         obj.GetName(),
		Namespace:    obj.GetNamespace(),
		BackupData:   encodedData,
		BackupTime:   metav1.Now(),
	}

	logger.Info("Backed up resource",
		"resourceType", resourceType,
		"name", obj.GetName(),
		"namespace", obj.GetNamespace(),
		"apiVersion", gvk.GroupVersion().String(),
		"kind", gvk.Kind)

	return backup, nil
}

// AddBackupToMigration adds a backup manifest to the migration status
func (m *BackupManager) AddBackupToMigration(migration *migrationv1alpha1.VSphereMigration, backup *migrationv1alpha1.BackupManifest) {
	// Check if backup already exists
	for i, existing := range migration.Status.BackupManifests {
		if existing.ResourceType == backup.ResourceType &&
			existing.Name == backup.Name &&
			existing.Namespace == backup.Namespace {
			// Update existing backup
			migration.Status.BackupManifests[i] = *backup
			return
		}
	}

	// Add new backup
	migration.Status.BackupManifests = append(migration.Status.BackupManifests, *backup)
}

// GetBackup retrieves a backup manifest from the migration
func (m *BackupManager) GetBackup(migration *migrationv1alpha1.VSphereMigration, resourceType, name, namespace string) (*migrationv1alpha1.BackupManifest, error) {
	for _, backup := range migration.Status.BackupManifests {
		if backup.ResourceType == resourceType &&
			backup.Name == name &&
			backup.Namespace == namespace {
			return &backup, nil
		}
	}

	return nil, fmt.Errorf("backup not found for %s/%s in namespace %s", resourceType, name, namespace)
}
