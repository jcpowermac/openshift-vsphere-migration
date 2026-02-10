package phases

import (
	"context"
	"time"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	configclient "github.com/openshift/client-go/config/clientset/versioned"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/backup"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/openshift"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/vsphere"
)

// Phase represents a migration phase
type Phase interface {
	// Name returns the phase name
	Name() migrationv1alpha1.MigrationPhase

	// Validate checks if the phase can be executed
	Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error

	// Execute runs the phase
	Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error)

	// Rollback reverts the phase changes
	Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error
}

// PhaseResult represents the result of a phase execution
type PhaseResult struct {
	// Status is the final status of the phase
	Status migrationv1alpha1.PhaseStatus

	// Message is a human-readable message
	Message string

	// Progress is the completion percentage (0-100)
	Progress int32

	// Logs contains structured log entries
	Logs []migrationv1alpha1.LogEntry

	// RequeueAfter specifies when to requeue for async operations
	RequeueAfter time.Duration
}

// PhaseExecutor executes phases and manages state
type PhaseExecutor struct {
	kubeClient          kubernetes.Interface
	configClient        configclient.Interface
	apiextensionsClient apiextensionsclient.Interface
	machineClient       machineclient.Interface
	dynamicClient       dynamic.Interface
	backupManager       *backup.BackupManager
	restoreManager      *backup.RestoreManager
	infraManager        *openshift.InfrastructureManager
	secretManager       *openshift.SecretManager
	sourceClient        *vsphere.Client
	targetClient        *vsphere.Client
}

// NewPhaseExecutor creates a new phase executor
func NewPhaseExecutor(
	kubeClient kubernetes.Interface,
	configClient configclient.Interface,
	apiextensionsClient apiextensionsclient.Interface,
	machineClient machineclient.Interface,
	dynamicClient dynamic.Interface,
	backupManager *backup.BackupManager,
	restoreManager *backup.RestoreManager,
) *PhaseExecutor {
	return &PhaseExecutor{
		kubeClient:          kubeClient,
		configClient:        configClient,
		apiextensionsClient: apiextensionsClient,
		machineClient:       machineClient,
		dynamicClient:       dynamicClient,
		backupManager:       backupManager,
		restoreManager:      restoreManager,
		infraManager:        openshift.NewInfrastructureManagerWithClients(configClient, kubeClient, apiextensionsClient),
		secretManager:       openshift.NewSecretManager(kubeClient),
	}
}

// ExecutePhase executes a phase and updates the migration status
func (e *PhaseExecutor) ExecutePhase(ctx context.Context, phase Phase, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	// Only initialize phase state for a new phase execution.
	// If the phase is already running (requeue/resume), preserve the existing state
	// so that phase.Execute() can detect the resume via CurrentPhaseState.Status.
	if migration.Status.CurrentPhaseState == nil ||
		migration.Status.CurrentPhaseState.Name != phase.Name() ||
		migration.Status.CurrentPhaseState.Status != migrationv1alpha1.PhaseStatusRunning {

		phaseState := &migrationv1alpha1.PhaseState{
			Name:     phase.Name(),
			Status:   migrationv1alpha1.PhaseStatusPending,
			Progress: 0,
			Message:  "Pending phase",
		}
		migration.Status.CurrentPhaseState = phaseState
	}

	// Add log entry
	startLog := migrationv1alpha1.LogEntry{
		Timestamp: metav1.Now(),
		Level:     migrationv1alpha1.LogLevelInfo,
		Message:   "Phase pending",
		Component: string(phase.Name()),
	}

	// Validate phase
	if err := phase.Validate(ctx, migration); err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Validation failed: " + err.Error(),
			Logs: []migrationv1alpha1.LogEntry{
				startLog,
				{
					Timestamp: metav1.Now(),
					Level:     migrationv1alpha1.LogLevelError,
					Message:   "Validation failed: " + err.Error(),
					Component: string(phase.Name()),
				},
			},
		}, err
	}

	// Execute phase
	result, err := phase.Execute(ctx, migration)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Execution failed: " + err.Error(),
			Logs: append([]migrationv1alpha1.LogEntry{startLog}, migrationv1alpha1.LogEntry{
				Timestamp: metav1.Now(),
				Level:     migrationv1alpha1.LogLevelError,
				Message:   "Execution failed: " + err.Error(),
				Component: string(phase.Name()),
			}),
		}, err
	}

	// Add start log to results
	if result.Logs == nil {
		result.Logs = make([]migrationv1alpha1.LogEntry, 0)
	}
	result.Logs = append([]migrationv1alpha1.LogEntry{startLog}, result.Logs...)

	return result, nil
}

// AddLog adds a log entry to the phase result
func AddLog(logs []migrationv1alpha1.LogEntry, level migrationv1alpha1.LogLevel, message, component string) []migrationv1alpha1.LogEntry {
	entry := migrationv1alpha1.LogEntry{
		Timestamp: metav1.Now(),
		Level:     level,
		Message:   message,
		Component: component,
	}
	return append(logs, entry)
}

// GetVSphereClient creates a vSphere client for a vCenter config
// Uses the default vsphere-creds secret in kube-system (for source vCenter)
func (e *PhaseExecutor) GetVSphereClient(ctx context.Context, server string) (*vsphere.Client, error) {
	// Get credentials from secret
	username, password, err := e.secretManager.GetCredentials(ctx, server)
	if err != nil {
		return nil, err
	}

	// Create client
	client, err := vsphere.NewClient(ctx,
		vsphere.Config{
			Server:   server,
			Insecure: true, // TODO: make configurable
		},
		vsphere.Credentials{
			Username: username,
			Password: password,
		})
	if err != nil {
		return nil, err
	}

	return client, nil
}

// GetVSphereClientFromMigration creates a vSphere client using credentials from the migration spec
// Use this for target vCenter which may have credentials in a custom secret
func (e *PhaseExecutor) GetVSphereClientFromMigration(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration, server string) (*vsphere.Client, error) {
	// Determine which secret to use based on the server
	var username, password string
	var err error

	// Check if this is the target vCenter (matches any of the failure domain servers)
	isTargetVCenter := false
	for _, fd := range migration.Spec.FailureDomains {
		if fd.Server == server {
			isTargetVCenter = true
			break
		}
	}

	if isTargetVCenter {
		// Use the target vCenter credentials secret from migration spec
		secretNamespace := migration.Spec.TargetVCenterCredentialsSecret.Namespace
		if secretNamespace == "" {
			secretNamespace = migration.Namespace
		}
		secretName := migration.Spec.TargetVCenterCredentialsSecret.Name

		username, password, err = e.secretManager.GetVCenterCredsFromSecret(ctx, secretNamespace, secretName, server)
		if err != nil {
			return nil, err
		}
	} else {
		// Use the default vsphere-creds secret for source vCenter
		username, password, err = e.secretManager.GetCredentials(ctx, server)
		if err != nil {
			return nil, err
		}
	}

	// Create client
	client, err := vsphere.NewClient(ctx,
		vsphere.Config{
			Server:   server,
			Insecure: true, // TODO: make configurable
		},
		vsphere.Credentials{
			Username: username,
			Password: password,
		})
	if err != nil {
		return nil, err
	}

	return client, nil
}

// GetMachineManager returns a machine manager for the executor
func (e *PhaseExecutor) GetMachineManager() *openshift.MachineManager {
	return openshift.NewMachineManagerWithClients(e.kubeClient, e.machineClient, e.dynamicClient)
}

// GetKubeClient returns the Kubernetes client
func (e *PhaseExecutor) GetKubeClient() kubernetes.Interface {
	return e.kubeClient
}
