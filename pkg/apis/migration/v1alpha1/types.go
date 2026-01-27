package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// VSphereMigration represents a migration from one vCenter to another
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=vspheremigrations,scope=Namespaced,shortName=vsm
type VSphereMigration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VSphereMigrationSpec   `json:"spec,omitempty"`
	Status VSphereMigrationStatus `json:"status,omitempty"`
}

// VSphereMigrationSpec defines the desired state of VSphereMigration
// +k8s:deepcopy-gen=true
type VSphereMigrationSpec struct {
	// State controls the workflow: Pending, Running, Paused, Rollback
	// +kubebuilder:validation:Enum=Pending;Running;Paused;Rollback
	// +kubebuilder:default=Pending
	State MigrationState `json:"state"`

	// ApprovalMode controls whether phases require manual approval
	// +kubebuilder:validation:Enum=Automatic;Manual
	// +kubebuilder:default=Automatic
	ApprovalMode ApprovalMode `json:"approvalMode"`

	// TargetVCenterCredentialsSecret references the secret containing target vCenter credentials
	// The secret should contain keys: {target-vcenter-fqdn}.username and {target-vcenter-fqdn}.password
	// Source vCenter configuration is read from the Infrastructure CRD
	TargetVCenterCredentialsSecret SecretReference `json:"targetVCenterCredentialsSecret"`

	// FailureDomains defines failure domains for the target vCenter
	FailureDomains []FailureDomain `json:"failureDomains"`

	// MachineSetConfig defines configuration for new worker machines
	MachineSetConfig MachineSetConfig `json:"machineSetConfig"`

	// ControlPlaneMachineSetConfig defines configuration for control plane machines
	ControlPlaneMachineSetConfig ControlPlaneMachineSetConfig `json:"controlPlaneMachineSetConfig"`

	// RollbackOnFailure automatically triggers rollback on phase failure
	// +kubebuilder:default=true
	RollbackOnFailure bool `json:"rollbackOnFailure"`
}

// MigrationState represents the overall state of the migration
type MigrationState string

const (
	MigrationStatePending  MigrationState = "Pending"
	MigrationStateRunning  MigrationState = "Running"
	MigrationStatePaused   MigrationState = "Paused"
	MigrationStateRollback MigrationState = "Rollback"
)

// ApprovalMode controls whether phases require manual approval
type ApprovalMode string

const (
	ApprovalModeAutomatic ApprovalMode = "Automatic"
	ApprovalModeManual    ApprovalMode = "Manual"
)

// VCenterConfig defines vCenter connection details
// +k8s:deepcopy-gen=true
type VCenterConfig struct {
	// Server is the vCenter FQDN or IP
	Server string `json:"server"`

	// Datacenter is the datacenter name
	Datacenter string `json:"datacenter"`

	// Cluster is the compute cluster path
	Cluster string `json:"cluster"`

	// Datastore is the datastore path
	Datastore string `json:"datastore"`

	// Network is the network name
	Network string `json:"network"`

	// Folder is the VM folder path
	Folder string `json:"folder"`

	// CredentialsSecret references the secret containing vCenter credentials
	CredentialsSecret SecretReference `json:"credentialsSecret"`
}

// SecretReference references a secret by name and namespace
// +k8s:deepcopy-gen=true
type SecretReference struct {
	// Name is the secret name
	Name string `json:"name"`

	// Namespace is the secret namespace
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// FailureDomain defines a failure domain for machine placement
// +k8s:deepcopy-gen=true
type FailureDomain struct{
	// Name is the unique identifier for this failure domain
	Name string `json:"name"`

	// Region is the region tag value
	Region string `json:"region"`

	// Zone is the zone tag value
	Zone string `json:"zone"`

	// Server is the vCenter server for this failure domain
	Server string `json:"server"`

	// Topology defines the vSphere topology
	Topology FailureDomainTopology `json:"topology"`
}

// FailureDomainTopology defines vSphere infrastructure topology
// +k8s:deepcopy-gen=true
type FailureDomainTopology struct {
	// Datacenter is the datacenter name
	Datacenter string `json:"datacenter"`

	// ComputeCluster is the compute cluster path
	ComputeCluster string `json:"computeCluster"`

	// Datastore is the datastore path
	Datastore string `json:"datastore"`

	// Networks is the list of network names
	Networks []string `json:"networks"`
}

// MachineSetConfig defines worker machine configuration
// +k8s:deepcopy-gen=true
type MachineSetConfig struct{
	// Replicas is the number of worker machines to create
	// +kubebuilder:validation:Minimum=1
	Replicas int32 `json:"replicas"`

	// FailureDomain is the failure domain name to use
	FailureDomain string `json:"failureDomain"`
}

// ControlPlaneMachineSetConfig defines control plane machine configuration
// +k8s:deepcopy-gen=true
type ControlPlaneMachineSetConfig struct{
	// FailureDomain is the failure domain name to use
	FailureDomain string `json:"failureDomain"`
}

// VSphereMigrationStatus defines the observed state of VSphereMigration
// +k8s:deepcopy-gen=true
type VSphereMigrationStatus struct{
	// Phase is the current migration phase
	Phase MigrationPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations of the migration state
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// PhaseHistory tracks completed phases with logs
	PhaseHistory []PhaseHistoryEntry `json:"phaseHistory,omitempty"`

	// CurrentPhaseState tracks the current phase execution
	CurrentPhaseState *PhaseState `json:"currentPhaseState,omitempty"`

	// BackupManifests stores backups for rollback
	BackupManifests []BackupManifest `json:"backupManifests,omitempty"`

	// StartTime is when the migration started
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the migration completed
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
}

// MigrationPhase represents the current phase of migration
type MigrationPhase string

const (
	PhaseNone                   MigrationPhase = ""
	PhasePreflight              MigrationPhase = "Preflight"
	PhaseBackup                 MigrationPhase = "Backup"
	PhaseDisableCVO             MigrationPhase = "DisableCVO"
	PhaseUpdateSecrets          MigrationPhase = "UpdateSecrets"
	PhaseCreateTags             MigrationPhase = "CreateTags"
	PhaseCreateFolder           MigrationPhase = "CreateFolder"
	PhaseUpdateInfrastructure   MigrationPhase = "UpdateInfrastructure"
	PhaseUpdateConfig           MigrationPhase = "UpdateConfig"
	PhaseRestartPods            MigrationPhase = "RestartPods"
	PhaseMonitorHealth          MigrationPhase = "MonitorHealth"
	PhaseCreateWorkers          MigrationPhase = "CreateWorkers"
	PhaseRecreateCPMS           MigrationPhase = "RecreateCPMS"
	PhaseScaleOldMachines       MigrationPhase = "ScaleOldMachines"
	PhaseCleanup                MigrationPhase = "Cleanup"
	PhaseVerify                 MigrationPhase = "Verify"
	PhaseCompleted              MigrationPhase = "Completed"
	PhaseFailed                 MigrationPhase = "Failed"
	PhaseRollingBack            MigrationPhase = "RollingBack"
	PhaseRollbackCompleted      MigrationPhase = "RollbackCompleted"
)

// PhaseHistoryEntry records the execution of a phase
// +k8s:deepcopy-gen=true
type PhaseHistoryEntry struct{
	// Phase is the phase name
	Phase MigrationPhase `json:"phase"`

	// Status is the final status of the phase
	Status PhaseStatus `json:"status"`

	// StartTime is when the phase started
	StartTime metav1.Time `json:"startTime"`

	// CompletionTime is when the phase completed
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message is a human-readable message about the phase
	Message string `json:"message,omitempty"`

	// Logs contains structured log entries from the phase
	Logs []LogEntry `json:"logs,omitempty"`
}

// PhaseState tracks the current phase execution
// +k8s:deepcopy-gen=true
type PhaseState struct{
	// Name is the phase name
	Name MigrationPhase `json:"name"`

	// Status is the current status
	Status PhaseStatus `json:"status"`

	// Progress is the completion percentage (0-100)
	Progress int32 `json:"progress,omitempty"`

	// Message is a human-readable status message
	Message string `json:"message,omitempty"`

	// RequiresApproval indicates if manual approval is needed
	RequiresApproval bool `json:"requiresApproval,omitempty"`

	// Approved indicates if the phase has been approved
	Approved bool `json:"approved,omitempty"`
}

// PhaseStatus represents the status of a phase
type PhaseStatus string

const (
	PhaseStatusPending   PhaseStatus = "Pending"
	PhaseStatusRunning   PhaseStatus = "Running"
	PhaseStatusCompleted PhaseStatus = "Completed"
	PhaseStatusFailed    PhaseStatus = "Failed"
	PhaseStatusSkipped   PhaseStatus = "Skipped"
)

// LogEntry represents a structured log entry
// +k8s:deepcopy-gen=true
type LogEntry struct{
	// Timestamp is when the log was created
	Timestamp metav1.Time `json:"timestamp"`

	// Level is the log level
	Level LogLevel `json:"level"`

	// Message is the log message
	Message string `json:"message"`

	// Component is the component that generated the log
	Component string `json:"component,omitempty"`

	// Fields contains additional structured data
	Fields map[string]string `json:"fields,omitempty"`
}

// LogLevel represents log severity
type LogLevel string

const (
	LogLevelDebug   LogLevel = "Debug"
	LogLevelInfo    LogLevel = "Info"
	LogLevelWarning LogLevel = "Warning"
	LogLevelError   LogLevel = "Error"
)

// BackupManifest stores a backup of a resource
// +k8s:deepcopy-gen=true
type BackupManifest struct{
	// ResourceType is the type of resource
	ResourceType string `json:"resourceType"`

	// Name is the resource name
	Name string `json:"name"`

	// Namespace is the resource namespace (if applicable)
	Namespace string `json:"namespace,omitempty"`

	// BackupData is the base64-encoded YAML
	BackupData string `json:"backupData"`

	// BackupTime is when the backup was created
	BackupTime metav1.Time `json:"backupTime"`
}

// Condition types
const (
	// ConditionReconciled indicates whether the migration has been reconciled
	ConditionReconciled string = "Reconciled"

	// ConditionHealthy indicates whether the cluster is healthy
	ConditionHealthy string = "Healthy"

	// ConditionProgressing indicates whether the migration is progressing
	ConditionProgressing string = "Progressing"
)

// Condition reasons
const (
	ReasonReconcileSucceeded string = "ReconcileSucceeded"
	ReasonReconcileFailed    string = "ReconcileFailed"
	ReasonHealthy            string = "Healthy"
	ReasonUnhealthy          string = "Unhealthy"
	ReasonProgressing        string = "Progressing"
	ReasonCompleted          string = "Completed"
	ReasonFailed             string = "Failed"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// VSphereMigrationList contains a list of VSphereMigration
type VSphereMigrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VSphereMigration `json:"items"`
}
