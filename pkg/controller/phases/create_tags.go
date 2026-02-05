package phases

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
	"github.com/openshift/vmware-cloud-foundation-migration/pkg/vsphere"
)

// CreateTagsPhase creates vSphere tags for failure domains
type CreateTagsPhase struct {
	executor *PhaseExecutor
}

// NewCreateTagsPhase creates a new create tags phase
func NewCreateTagsPhase(executor *PhaseExecutor) *CreateTagsPhase {
	return &CreateTagsPhase{executor: executor}
}

// Name returns the phase name
func (p *CreateTagsPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhaseCreateTags
}

// Validate checks if the phase can be executed
func (p *CreateTagsPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	if len(migration.Spec.FailureDomains) == 0 {
		return fmt.Errorf("no failure domains specified")
	}
	return nil
}

// Execute runs the phase
func (p *CreateTagsPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Creating vSphere tags for failure domains")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Creating vSphere tags for failure domains", string(p.Name()))

	// Track vSphere clients by server to reuse connections
	vSphereClients := make(map[string]*vsphere.Client)
	defer func() {
		// Cleanup all clients
		for _, client := range vSphereClients {
			client.Logout(ctx)
		}
	}()

	// Process each failure domain
	for i, fd := range migration.Spec.FailureDomains {
		// Get or create vSphere client for this failure domain's server
		targetClient, exists := vSphereClients[fd.Server]
		if !exists {
			var err error
			targetClient, err = p.executor.GetVSphereClientFromMigration(ctx, migration, fd.Server)
			if err != nil {
				return &PhaseResult{
					Status:  migrationv1alpha1.PhaseStatusFailed,
					Message: fmt.Sprintf("Failed to connect to target vCenter %s: %v", fd.Server, err),
					Logs:    logs,
				}, err
			}
			vSphereClients[fd.Server] = targetClient
			logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
				fmt.Sprintf("Connected to target vCenter: %s", fd.Server),
				string(p.Name()))
		}
		logger.Info("Creating tags for failure domain",
			"name", fd.Name,
			"region", fd.Region,
			"zone", fd.Zone)

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Creating tags for failure domain: %s (region: %s, zone: %s)", fd.Name, fd.Region, fd.Zone),
			string(p.Name()))

		// Create region and zone tags
		regionTagID, zoneTagID, err := targetClient.CreateRegionAndZoneTags(ctx, fd.Region, fd.Zone)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: fmt.Sprintf("Failed to create tags for failure domain %s: %v", fd.Name, err),
				Logs:    logs,
			}, err
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Created tags - Region: %s, Zone: %s", regionTagID, zoneTagID),
			string(p.Name()))

		// Get datacenter and cluster
		dc, err := targetClient.GetDatacenter(ctx, fd.Topology.Datacenter)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: fmt.Sprintf("Failed to get datacenter %s: %v", fd.Topology.Datacenter, err),
				Logs:    logs,
			}, err
		}

		cluster, err := targetClient.GetCluster(ctx, fd.Topology.ComputeCluster)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: fmt.Sprintf("Failed to get cluster %s: %v", fd.Topology.ComputeCluster, err),
				Logs:    logs,
			}, err
		}

		// Attach tags
		if err := targetClient.AttachFailureDomainTags(ctx, regionTagID, zoneTagID, dc, cluster); err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: fmt.Sprintf("Failed to attach tags: %v", err),
				Logs:    logs,
			}, err
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Attached tags to datacenter %s and cluster %s", fd.Topology.Datacenter, fd.Topology.ComputeCluster),
			string(p.Name()))

		// Update progress
		progress := int32((i + 1) * 100 / len(migration.Spec.FailureDomains))
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Progress: %d%%", progress),
			string(p.Name()))
	}

	logger.Info("Successfully created all vSphere tags")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Successfully created all vSphere tags", string(p.Name()))

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "Successfully created all vSphere tags",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *CreateTagsPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) error {
	logger := klog.FromContext(ctx)
	logger.Info("Rollback for CreateTags phase - tags will remain (not harmful)")
	// Tags are not harmful to leave behind, so we don't delete them
	return nil
}
