package controller

import (
	"context"
	"fmt"
	"time"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configclient "github.com/openshift/client-go/config/clientset/versioned"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/backup"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/controller/phases"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/controller/state"
)

// MigrationController manages vSphere migrations
type MigrationController struct {
	kubeClient     kubernetes.Interface
	configClient   configclient.Interface
	dynamicClient  dynamic.Interface
	scheme         *runtime.Scheme
	phaseExecutor  *phases.PhaseExecutor
	stateMachine   *state.StateMachine
	backupManager  *backup.BackupManager
	restoreManager *backup.RestoreManager
	workqueue      workqueue.RateLimitingInterface
	gvr            schema.GroupVersionResource
}

// NewMigrationController creates a new migration controller
func NewMigrationController(
	kubeClient kubernetes.Interface,
	configClient configclient.Interface,
	machineClient machineclient.Interface,
	dynamicClient dynamic.Interface,
	apiextensionsClient apiextensionsclient.Interface,
	runtimeClient client.Client,
	scheme *runtime.Scheme,
	recorder events.Recorder,
) (*MigrationController, factory.Controller) {

	c := &MigrationController{
		kubeClient:    kubeClient,
		configClient:  configClient,
		dynamicClient: dynamicClient,
		scheme:        scheme,
		workqueue:     workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "vmwarecloudfoundationmigrations"),
		gvr: schema.GroupVersionResource{
			Group:    "migration.openshift.io",
			Version:  "v1alpha1",
			Resource: "vmwarecloudfoundationmigrations",
		},
	}

	// Initialize managers
	c.backupManager = backup.NewBackupManager(scheme)
	c.restoreManager = backup.NewRestoreManager(runtimeClient, scheme)

	// Initialize phase executor
	c.phaseExecutor = phases.NewPhaseExecutor(
		kubeClient,
		configClient,
		apiextensionsClient,
		machineClient,
		dynamicClient,
		c.backupManager,
		c.restoreManager,
	)

	// Initialize state machine
	c.stateMachine = state.NewStateMachine(c.phaseExecutor)

	// Create factory controller
	factoryController := factory.New().
		WithSync(c.sync).
		ResyncEvery(1*time.Minute).
		ToController("vmware-cloud-foundation-migration", recorder)

	return c, factoryController
}

// EnqueueMigration adds a migration to the work queue
func (c *MigrationController) EnqueueMigration(obj interface{}) {
	logger := klog.Background()

	if unstructuredObj, ok := obj.(*unstructured.Unstructured); ok {
		key := fmt.Sprintf("%s/%s", unstructuredObj.GetNamespace(), unstructuredObj.GetName())
		logger.Info("Enqueuing VmwareCloudFoundationMigration", "key", key)
		c.workqueue.Add(key)
		return
	}

	logger.Error(fmt.Errorf("unexpected object type"), "Failed to enqueue migration", "obj", obj)
}

// sync is called by the library-go factory
func (c *MigrationController) sync(ctx context.Context, controllerContext factory.SyncContext) error {
	logger := klog.FromContext(ctx)

	// Process all items in the work queue
	for c.workqueue.Len() > 0 {
		item, shutdown := c.workqueue.Get()
		if shutdown {
			return nil
		}

		func() {
			defer c.workqueue.Done(item)

			key, ok := item.(string)
			if !ok {
				c.workqueue.Forget(item)
				logger.Error(fmt.Errorf("unexpected type in workqueue"), "Expected string", "got", item)
				return
			}

			if err := c.syncMigrationFromKey(ctx, key); err != nil {
				// Requeue on error
				c.workqueue.AddRateLimited(key)
				logger.Error(err, "Failed to sync migration", "key", key)
				return
			}

			c.workqueue.Forget(item)
			logger.V(4).Info("Successfully synced migration", "key", key)
		}()
	}

	return nil
}

// syncMigrationFromKey fetches a migration by key and syncs it
func (c *MigrationController) syncMigrationFromKey(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx).WithValues("key", key)
	ctx = klog.NewContext(ctx, logger)

	// Parse the key
	namespace, name, err := migrationFromQueueKey(key)
	if err != nil {
		return err
	}

	logger.Info("Syncing VmwareCloudFoundationMigration", "namespace", namespace, "name", name)

	// Fetch the migration resource using dynamic client
	unstructuredMigration, err := c.dynamicClient.Resource(c.gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get VmwareCloudFoundationMigration: %w", err)
	}

	// Convert unstructured to typed object
	migration := &migrationv1alpha1.VmwareCloudFoundationMigration{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredMigration.Object, migration); err != nil {
		return fmt.Errorf("failed to convert unstructured to VmwareCloudFoundationMigration: %w", err)
	}

	// Sync the migration
	if err := c.syncMigration(ctx, migration); err != nil {
		return err
	}

	// Update the status
	return c.updateMigrationStatus(ctx, migration)
}

// SyncMigration is a public wrapper for testing
func (c *MigrationController) SyncMigration(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	return c.syncMigration(ctx, migration)
}

// migrationQueueKey generates a queue key for a migration
func migrationQueueKey(migration *migrationv1alpha1.VmwareCloudFoundationMigration) string {
	return fmt.Sprintf("%s/%s", migration.Namespace, migration.Name)
}

// migrationFromQueueKey extracts namespace and name from a queue key
func migrationFromQueueKey(key string) (namespace, name string, err error) {
	namespace, name, err = cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return "", "", fmt.Errorf("invalid queue key: %w", err)
	}
	return namespace, name, nil
}

// updateMigrationStatus updates the status of a migration resource
func (c *MigrationController) updateMigrationStatus(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)

	// Convert typed object to unstructured
	unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(migration)
	if err != nil {
		return fmt.Errorf("failed to convert to unstructured: %w", err)
	}

	unstructuredMigration := &unstructured.Unstructured{Object: unstructuredObj}

	// Update the status subresource
	_, err = c.dynamicClient.Resource(c.gvr).Namespace(migration.Namespace).UpdateStatus(ctx, unstructuredMigration, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update migration status: %w", err)
	}

	logger.Info("Updated migration status", "namespace", migration.Namespace, "name", migration.Name, "phase", migration.Status.Phase)
	return nil
}
