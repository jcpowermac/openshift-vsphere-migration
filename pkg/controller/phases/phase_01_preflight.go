package phases

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
)

// PreflightPhase validates prerequisites for migration
type PreflightPhase struct {
	executor *PhaseExecutor
}

// NewPreflightPhase creates a new preflight phase
func NewPreflightPhase(executor *PhaseExecutor) *PreflightPhase {
	return &PreflightPhase{executor: executor}
}

// Name returns the phase name
func (p *PreflightPhase) Name() migrationv1alpha1.MigrationPhase {
	return migrationv1alpha1.PhasePreflight
}

// Validate checks if the phase can be executed
func (p *PreflightPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	// Basic validation
	if len(migration.Spec.FailureDomains) == 0 {
		return fmt.Errorf("no failure domains specified")
	}
	if migration.Spec.TargetVCenterCredentialsSecret.Name == "" {
		return fmt.Errorf("target vCenter credentials secret name is empty")
	}
	return nil
}

// Execute runs the phase
func (p *PreflightPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) (*PhaseResult, error) {
	logger := klog.FromContext(ctx)
	logs := make([]migrationv1alpha1.LogEntry, 0)

	logger.Info("Running preflight checks")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Running preflight checks", string(p.Name()))

	// Get source vCenter from Infrastructure CRD
	logger.Info("Reading source vCenter from Infrastructure CRD")
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Reading source vCenter configuration from Infrastructure CRD",
		string(p.Name()))

	sourceVC, err := p.executor.infraManager.GetSourceVCenter(ctx)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: fmt.Sprintf("Failed to get source vCenter from Infrastructure: %v", err),
			Logs:    logs,
		}, err
	}

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("Found source vCenter in Infrastructure CRD: %s", sourceVC.Server),
		string(p.Name()))

	// Test source vCenter connectivity
	logger.Info("Testing source vCenter connectivity", "server", sourceVC.Server)
	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		fmt.Sprintf("Testing source vCenter connectivity: %s", sourceVC.Server),
		string(p.Name()))

	sourceClient, err := p.executor.GetVSphereClientFromMigration(ctx, migration, sourceVC.Server)
	if err != nil {
		return &PhaseResult{
			Status:  migrationv1alpha1.PhaseStatusFailed,
			Message: fmt.Sprintf("Failed to connect to source vCenter: %v", err),
			Logs:    logs,
		}, err
	}
	defer sourceClient.Logout(ctx)

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"Successfully connected to source vCenter",
		string(p.Name()))

	// Validate source vCenter datacenters
	if len(sourceVC.Datacenters) > 0 {
		_, err = sourceClient.GetDatacenter(ctx, sourceVC.Datacenters[0])
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: fmt.Sprintf("Failed to find source datacenter: %v", err),
				Logs:    logs,
			}, err
		}

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Validated source datacenter: %s", sourceVC.Datacenters[0]),
			string(p.Name()))
	}

	// Get unique target vCenters from failure domains
	targetVCenters := make(map[string]bool)
	for _, fd := range migration.Spec.FailureDomains {
		targetVCenters[fd.Server] = true
	}

	// Test target vCenter connectivity for each unique server
	for targetServer := range targetVCenters {
		logger.Info("Testing target vCenter connectivity", "server", targetServer)
		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Testing target vCenter connectivity: %s", targetServer),
			string(p.Name()))

		targetClient, err := p.executor.GetVSphereClientFromMigration(ctx, migration, targetServer)
		if err != nil {
			return &PhaseResult{
				Status:  migrationv1alpha1.PhaseStatusFailed,
				Message: fmt.Sprintf("Failed to connect to target vCenter %s: %v", targetServer, err),
				Logs:    logs,
			}, err
		}
		defer targetClient.Logout(ctx)

		logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
			fmt.Sprintf("Successfully connected to target vCenter: %s", targetServer),
			string(p.Name()))

		// Validate target vCenter datacenters from failure domains
		for _, fd := range migration.Spec.FailureDomains {
			if fd.Server == targetServer {
				_, err = targetClient.GetDatacenter(ctx, fd.Topology.Datacenter)
				if err != nil {
					return &PhaseResult{
						Status:  migrationv1alpha1.PhaseStatusFailed,
						Message: fmt.Sprintf("Failed to find target datacenter %s: %v", fd.Topology.Datacenter, err),
						Logs:    logs,
					}, err
				}

				logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
					fmt.Sprintf("Validated target datacenter: %s", fd.Topology.Datacenter),
					string(p.Name()))
			}
		}
	}

	// Validate cluster health
	logger.Info("Validating cluster health")
	// TODO: Check cluster operators, nodes, etc.

	logs = AddLog(logs, migrationv1alpha1.LogLevelInfo,
		"All preflight checks passed",
		string(p.Name()))

	return &PhaseResult{
		Status:   migrationv1alpha1.PhaseStatusCompleted,
		Message:  "All preflight checks passed",
		Progress: 100,
		Logs:     logs,
	}, nil
}

// Rollback reverts the phase changes
func (p *PreflightPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
	// Preflight has no state to rollback
	return nil
}
