package phases

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
)

// CreateFolderPhase creates VM folder in target vCenter
type CreateFolderPhase struct {
	executor *PhaseExecutor
}

// NewCreateFolderPhase creates a new create folder phase
func NewCreateFolderPhase(executor *PhaseExecutor) *CreateFolderPhase {
	return &CreateFolderPhase{executor: executor}
}

// Name returns the phase name
func (p *CreateFolderPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseCreateFolder
}

// Validate checks if the phase can be executed
func (p *CreateFolderPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	if len(migration.Spec.FailureDomains) == 0 {
		return fmt.Errorf("no failure domains specified")
	}
	return nil
}

// Execute runs the phase
func (p *CreateFolderPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Creating VM folder in target vCenter")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Creating VM folder in target vCenter", string(p.Name()))

	// Get infrastructure ID to ensure folder matches expected pattern
	infraID, err := p.executor.infraManager.GetInfrastructureID(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: "Failed to get infrastructure ID: " + err.Error(),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("Infrastructure ID: %s", infraID),
		string(p.Name()))

	// Auto-generate folder path if not specified in failure domains
	for i := range migration.Spec.FailureDomains {
		fd := &migration.Spec.FailureDomains[i]
		if fd.Topology.Folder == "" {
			fd.Topology.Folder = fmt.Sprintf("/%s/vm/%s", fd.Topology.Datacenter, infraID)
			logger.Info("Generated folder path", "failureDomain", fd.Name, "folder", fd.Topology.Folder)
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
				fmt.Sprintf("Generated folder path for %s: %s", fd.Name, fd.Topology.Folder),
				string(p.Name()))
		}
	}

	// Construct folder path: /{datacenter}/vm/{infrastructure-id}
	folderName := infraID

	// Group failure domains by server and datacenter
	type ServerDC struct {
		Server     string
		Datacenter string
	}
	serverDCs := make(map[ServerDC]bool)
	for _, fd := range migration.Spec.FailureDomains {
		serverDCs[ServerDC{Server: fd.Server, Datacenter: fd.Topology.Datacenter}] = true
	}

	// Create folder in each unique server/datacenter combination
	for serverDC := range serverDCs {
		logger.Info("Creating VM folder", "server", serverDC.Server, "datacenter", serverDC.Datacenter, "folder", folderName)
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Creating VM folder in %s/%s: %s", serverDC.Server, serverDC.Datacenter, folderName),
			string(p.Name()))

		// Connect to target vCenter
		targetClient, err := p.executor.GetVSphereClientFromMigration(ctx, migration, serverDC.Server)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: fmt.Sprintf("Failed to connect to target vCenter %s: %v", serverDC.Server, err),
				Logs:    logs,
			}, err
		}
		defer targetClient.Logout(ctx)

		// Create folder
		folder, err := targetClient.CreateVMFolder(ctx, serverDC.Datacenter, folderName)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: fmt.Sprintf("Failed to create VM folder in %s: %v", serverDC.Server, err),
				Logs:    logs,
			}, err
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Created VM folder: %s (moref: %s)", folderName, folder.Reference()),
			string(p.Name()))

		// Verify folder is accessible
		_, err = targetClient.GetVMFolder(ctx, serverDC.Datacenter, folderName)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: fmt.Sprintf("Failed to verify VM folder in %s: %v", serverDC.Server, err),
				Logs:    logs,
			}, err
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Verified VM folder is accessible in %s/%s", serverDC.Server, serverDC.Datacenter),
			string(p.Name()))
	}

	logger.Info("Successfully created VM folder")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Successfully created VM folder", string(p.Name()))

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully created VM folder",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *CreateFolderPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rolling back CreateFolder phase - folder will be left in place for safety")

	// We don't delete the folder as it may contain VMs or other resources
	// Manual cleanup may be required

	return nil
}
