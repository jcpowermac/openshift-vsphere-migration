package vsphere

import (
	"context"
	"fmt"

	"github.com/vmware/govmomi/cns"
	cnstypes "github.com/vmware/govmomi/cns/types"
	"github.com/vmware/govmomi/vim25/types"
	"k8s.io/klog/v2"
)

// CNSManager manages Cloud Native Storage operations
type CNSManager struct {
	client    *Client
	cnsClient *cns.Client
}

// CNSVolumeInfo contains information about a CNS volume
type CNSVolumeInfo struct {
	VolumeID     string
	Name         string
	VolumeType   string
	DatastoreURL string
	BackingPath  string
	CapacityMB   int64
	HealthStatus string
}

// NewCNSManager creates a new CNS manager
func NewCNSManager(ctx context.Context, client *Client) (*CNSManager, error) {
	if client == nil || client.vimClient == nil {
		return nil, fmt.Errorf("vSphere client is nil")
	}

	// Create CNS client
	cnsClient, err := cns.NewClient(ctx, client.vimClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create CNS client: %w", err)
	}

	return &CNSManager{
		client:    client,
		cnsClient: cnsClient,
	}, nil
}

// QueryVolume queries CNS for a volume by ID
func (m *CNSManager) QueryVolume(ctx context.Context, volumeID string) (*CNSVolumeInfo, error) {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Querying CNS volume", "volumeID", volumeID)

	// Build query filter
	queryFilter := &cnstypes.CnsQueryFilter{
		VolumeIds: []cnstypes.CnsVolumeId{
			{Id: volumeID},
		},
	}

	result, err := m.cnsClient.QueryVolume(ctx, queryFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to query CNS volume: %w", err)
	}

	if len(result.Volumes) == 0 {
		return nil, fmt.Errorf("volume %s not found", volumeID)
	}

	vol := result.Volumes[0]
	info := &CNSVolumeInfo{
		VolumeID:   vol.VolumeId.Id,
		Name:       vol.Name,
		VolumeType: vol.VolumeType,
		CapacityMB: vol.BackingObjectDetails.GetCnsBackingObjectDetails().CapacityInMb,
	}

	// Extract backing details
	if backingDetails := vol.BackingObjectDetails; backingDetails != nil {
		if blockBacking, ok := backingDetails.(*cnstypes.CnsBlockBackingDetails); ok {
			info.BackingPath = blockBacking.BackingDiskPath
		}
	}

	// Extract datastore URL
	if len(vol.DatastoreUrl) > 0 {
		info.DatastoreURL = vol.DatastoreUrl
	}

	// Extract health status
	if vol.HealthStatus != "" {
		info.HealthStatus = vol.HealthStatus
	}

	logger.V(2).Info("Retrieved CNS volume info", "volumeID", info.VolumeID, "name", info.Name)
	return info, nil
}

// QueryVolumeByPath queries CNS for a volume by its backing path
func (m *CNSManager) QueryVolumeByPath(ctx context.Context, backingPath string) (*CNSVolumeInfo, error) {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Querying CNS volume by path", "path", backingPath)

	// Query all volumes and filter by path
	queryFilter := &cnstypes.CnsQueryFilter{}
	result, err := m.cnsClient.QueryVolume(ctx, queryFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to query CNS volumes: %w", err)
	}

	for _, vol := range result.Volumes {
		if backingDetails := vol.BackingObjectDetails; backingDetails != nil {
			if blockBacking, ok := backingDetails.(*cnstypes.CnsBlockBackingDetails); ok {
				if blockBacking.BackingDiskPath == backingPath {
					return &CNSVolumeInfo{
						VolumeID:     vol.VolumeId.Id,
						Name:         vol.Name,
						VolumeType:   vol.VolumeType,
						BackingPath:  blockBacking.BackingDiskPath,
						DatastoreURL: vol.DatastoreUrl,
						CapacityMB:   vol.BackingObjectDetails.GetCnsBackingObjectDetails().CapacityInMb,
						HealthStatus: vol.HealthStatus,
					}, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("volume with backing path %s not found", backingPath)
}

// RegisterVolume registers a VMDK as a CNS volume
func (m *CNSManager) RegisterVolume(ctx context.Context, backingPath string, name string, datastoreURL string, containerClusterID string) (*CNSVolumeInfo, error) {
	logger := klog.FromContext(ctx)
	logger.Info("Registering CNS volume", "path", backingPath, "name", name)

	// Parse the datastore path to get datastore name
	datastoreName, _, err := ParseDatastorePath(backingPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse backing path: %w", err)
	}

	// Get datastore reference
	ds, err := m.client.GetDatastore(ctx, datastoreName)
	if err != nil {
		return nil, fmt.Errorf("failed to get datastore %s: %w", datastoreName, err)
	}

	// Build create spec for block volume
	createSpec := cnstypes.CnsVolumeCreateSpec{
		Name:       name,
		VolumeType: string(cnstypes.CnsVolumeTypeBlock),
		Datastores: []types.ManagedObjectReference{ds.Reference()},
		BackingObjectDetails: &cnstypes.CnsBlockBackingDetails{
			CnsBackingObjectDetails: cnstypes.CnsBackingObjectDetails{},
			BackingDiskPath:         backingPath,
		},
		Metadata: cnstypes.CnsVolumeMetadata{
			ContainerCluster: cnstypes.CnsContainerCluster{
				ClusterType:   string(cnstypes.CnsClusterTypeKubernetes),
				ClusterId:     containerClusterID,
				ClusterFlavor: string(cnstypes.CnsClusterFlavorVanilla),
			},
		},
	}

	// Create/Register the volume
	task, err := m.cnsClient.CreateVolume(ctx, []cnstypes.CnsVolumeCreateSpec{createSpec})
	if err != nil {
		return nil, fmt.Errorf("failed to create CNS volume: %w", err)
	}

	taskInfo, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for CNS volume creation: %w", err)
	}

	// Extract volume ID from result
	operationResult, ok := taskInfo.Result.(cnstypes.CnsVolumeOperationBatchResult)
	if !ok {
		return nil, fmt.Errorf("unexpected result type from CNS create volume")
	}

	if len(operationResult.VolumeResults) == 0 {
		return nil, fmt.Errorf("no volume results returned")
	}

	volResult := operationResult.VolumeResults[0]
	if volResult.GetCnsVolumeOperationResult().Fault != nil {
		return nil, fmt.Errorf("CNS volume creation failed: %s",
			volResult.GetCnsVolumeOperationResult().Fault.LocalizedMessage)
	}

	// Get the volume ID from the result
	createResult, ok := volResult.(*cnstypes.CnsVolumeCreateResult)
	if !ok {
		return nil, fmt.Errorf("unexpected volume result type")
	}

	info := &CNSVolumeInfo{
		VolumeID:    createResult.VolumeId.Id,
		Name:        name,
		VolumeType:  string(cnstypes.CnsVolumeTypeBlock),
		BackingPath: backingPath,
	}

	logger.Info("Successfully registered CNS volume", "volumeID", info.VolumeID, "name", info.Name)
	return info, nil
}

// DeleteVolume deletes a CNS volume
func (m *CNSManager) DeleteVolume(ctx context.Context, volumeID string, deleteDisk bool) error {
	logger := klog.FromContext(ctx)
	logger.Info("Deleting CNS volume", "volumeID", volumeID, "deleteDisk", deleteDisk)

	volumeIDs := []cnstypes.CnsVolumeId{
		{Id: volumeID},
	}

	task, err := m.cnsClient.DeleteVolume(ctx, volumeIDs, deleteDisk)
	if err != nil {
		return fmt.Errorf("failed to delete CNS volume: %w", err)
	}

	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("failed to wait for CNS volume deletion: %w", err)
	}

	logger.Info("Successfully deleted CNS volume", "volumeID", volumeID)
	return nil
}

// ListVolumes lists all CNS volumes
func (m *CNSManager) ListVolumes(ctx context.Context) ([]CNSVolumeInfo, error) {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Listing all CNS volumes")

	queryFilter := &cnstypes.CnsQueryFilter{}
	result, err := m.cnsClient.QueryVolume(ctx, queryFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to query CNS volumes: %w", err)
	}

	var volumes []CNSVolumeInfo
	for _, vol := range result.Volumes {
		info := CNSVolumeInfo{
			VolumeID:     vol.VolumeId.Id,
			Name:         vol.Name,
			VolumeType:   vol.VolumeType,
			DatastoreURL: vol.DatastoreUrl,
			CapacityMB:   vol.BackingObjectDetails.GetCnsBackingObjectDetails().CapacityInMb,
			HealthStatus: vol.HealthStatus,
		}

		if backingDetails := vol.BackingObjectDetails; backingDetails != nil {
			if blockBacking, ok := backingDetails.(*cnstypes.CnsBlockBackingDetails); ok {
				info.BackingPath = blockBacking.BackingDiskPath
			}
		}

		volumes = append(volumes, info)
	}

	logger.V(2).Info("Listed CNS volumes", "count", len(volumes))
	return volumes, nil
}

// UpdateVolumeMetadata updates metadata for a CNS volume
func (m *CNSManager) UpdateVolumeMetadata(ctx context.Context, volumeID string, metadata map[string]string) error {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Updating CNS volume metadata", "volumeID", volumeID)

	// Build entity metadata entries
	var entityMetadata []cnstypes.BaseCnsEntityMetadata
	for key, value := range metadata {
		entityMetadata = append(entityMetadata, &cnstypes.CnsKubernetesEntityMetadata{
			CnsEntityMetadata: cnstypes.CnsEntityMetadata{
				EntityName: key,
			},
			EntityType: string(cnstypes.CnsKubernetesEntityTypePV),
			Namespace:  value,
		})
	}

	updateSpec := cnstypes.CnsVolumeMetadataUpdateSpec{
		VolumeId: cnstypes.CnsVolumeId{Id: volumeID},
		Metadata: cnstypes.CnsVolumeMetadata{
			EntityMetadata: entityMetadata,
		},
	}

	task, err := m.cnsClient.UpdateVolumeMetadata(ctx, []cnstypes.CnsVolumeMetadataUpdateSpec{updateSpec})
	if err != nil {
		return fmt.Errorf("failed to update CNS volume metadata: %w", err)
	}

	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("failed to wait for metadata update: %w", err)
	}

	logger.V(2).Info("Successfully updated CNS volume metadata", "volumeID", volumeID)
	return nil
}

// Close closes the CNS manager (no-op as it shares the vim25 session)
func (m *CNSManager) Close(ctx context.Context) error {
	return nil
}
