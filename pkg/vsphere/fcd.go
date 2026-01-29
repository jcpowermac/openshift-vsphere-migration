package vsphere

import (
	"context"
	"fmt"
	"strings"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"
	"github.com/vmware/govmomi/vslm"
	vslmtypes "github.com/vmware/govmomi/vslm/types"
	"k8s.io/klog/v2"
)

// FCDManager manages First Class Disk (FCD) operations
type FCDManager struct {
	client         *Client
	vslmClient     *vslm.Client
	globalObjMgr   *vslm.GlobalObjectManager
}

// FCDInfo contains information about a First Class Disk
type FCDInfo struct {
	ID           string
	Name         string
	Path         string
	DatastoreMoRef string
	CapacityMB   int64
}

// NewFCDManager creates a new FCD manager
func NewFCDManager(ctx context.Context, client *Client) (*FCDManager, error) {
	if client == nil || client.vimClient == nil {
		return nil, fmt.Errorf("vSphere client is nil")
	}

	// Create vslm client
	vslmClient, err := vslm.NewClient(ctx, client.vimClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create vslm client: %w", err)
	}

	// Create GlobalObjectManager
	globalObjMgr := vslm.NewGlobalObjectManager(vslmClient)

	return &FCDManager{
		client:       client,
		vslmClient:   vslmClient,
		globalObjMgr: globalObjMgr,
	}, nil
}

// GetFCDByID retrieves a First Class Disk by its ID
func (m *FCDManager) GetFCDByID(ctx context.Context, fcdID string) (*FCDInfo, error) {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Getting FCD by ID", "fcdID", fcdID)

	id := types.ID{Id: fcdID}
	vStorageObject, err := m.globalObjMgr.Retrieve(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve FCD %s: %w", fcdID, err)
	}

	info := &FCDInfo{
		ID:         vStorageObject.Config.Id.Id,
		Name:       vStorageObject.Config.Name,
		CapacityMB: vStorageObject.Config.CapacityInMB,
	}

	// Extract backing info
	if backing, ok := vStorageObject.Config.Backing.(*types.BaseConfigInfoDiskFileBackingInfo); ok {
		info.Path = backing.FilePath
		info.DatastoreMoRef = backing.Datastore.Value
	}

	logger.V(2).Info("Retrieved FCD", "id", info.ID, "name", info.Name, "path", info.Path)
	return info, nil
}

// ListFCDs lists all First Class Disks using the global object manager
func (m *FCDManager) ListFCDs(ctx context.Context) ([]FCDInfo, error) {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Listing all FCDs")

	// Query all FCDs without filter
	result, err := m.globalObjMgr.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list FCDs: %w", err)
	}

	var fcds []FCDInfo
	if result != nil && result.Id != nil {
		for _, id := range result.Id {
			// Retrieve full object details
			fcdInfo, err := m.GetFCDByID(ctx, id.Id)
			if err != nil {
				logger.V(2).Info("Failed to get FCD details, skipping", "id", id.Id, "error", err)
				continue
			}
			fcds = append(fcds, *fcdInfo)
		}
	}

	logger.V(2).Info("Listed FCDs", "count", len(fcds))
	return fcds, nil
}

// ListFCDsOnDatastore lists First Class Disks on a specific datastore
func (m *FCDManager) ListFCDsOnDatastore(ctx context.Context, datastoreName string) ([]FCDInfo, error) {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("Listing FCDs on datastore", "datastore", datastoreName)

	// Get datastore reference
	ds, err := m.client.GetDatastore(ctx, datastoreName)
	if err != nil {
		return nil, fmt.Errorf("failed to get datastore %s: %w", datastoreName, err)
	}

	// Create query spec filtering by datastore
	querySpec := vslmtypes.VslmVsoVStorageObjectQuerySpec{
		QueryField:    "datastoreMoId",
		QueryOperator: "equals",
		QueryValue:    []string{ds.Reference().Value},
	}

	result, err := m.globalObjMgr.List(ctx, querySpec)
	if err != nil {
		return nil, fmt.Errorf("failed to list FCDs on datastore: %w", err)
	}

	var fcds []FCDInfo
	if result != nil && result.Id != nil {
		for _, id := range result.Id {
			fcdInfo, err := m.GetFCDByID(ctx, id.Id)
			if err != nil {
				logger.V(2).Info("Failed to get FCD details, skipping", "id", id.Id, "error", err)
				continue
			}
			fcds = append(fcds, *fcdInfo)
		}
	}

	logger.V(2).Info("Listed FCDs on datastore", "datastore", datastoreName, "count", len(fcds))
	return fcds, nil
}

// RegisterDisk registers an existing VMDK as a First Class Disk
// Note: This operation requires using the ObjectManager with a datastore, not GlobalObjectManager
func (m *FCDManager) RegisterDisk(ctx context.Context, datastoreName string, path string, name string) (*FCDInfo, error) {
	logger := klog.FromContext(ctx)
	logger.Info("Registering disk as FCD", "datastore", datastoreName, "path", path, "name", name)

	// Get datastore reference
	ds, err := m.client.GetDatastore(ctx, datastoreName)
	if err != nil {
		return nil, fmt.Errorf("failed to get datastore %s: %w", datastoreName, err)
	}

	// Create object manager (uses the vim25 client)
	objMgr := vslm.NewObjectManager(m.client.vimClient)

	// Construct the full datastore path
	fullPath := fmt.Sprintf("[%s] %s", datastoreName, path)

	// Register the disk
	vStorageObject, err := objMgr.RegisterDisk(ctx, fullPath, name)
	if err != nil {
		return nil, fmt.Errorf("failed to register disk: %w", err)
	}

	info := &FCDInfo{
		ID:             vStorageObject.Config.Id.Id,
		Name:           vStorageObject.Config.Name,
		Path:           fullPath,
		DatastoreMoRef: ds.Reference().Value,
		CapacityMB:     vStorageObject.Config.CapacityInMB,
	}

	logger.Info("Successfully registered disk as FCD", "fcdID", info.ID, "name", info.Name)
	return info, nil
}

// AttachDisk attaches an FCD to a virtual machine
func (m *FCDManager) AttachDisk(ctx context.Context, vm *object.VirtualMachine, datastore *object.Datastore, fcdID string, controllerKey int32, unitNumber int32) error {
	logger := klog.FromContext(ctx)
	logger.Info("Attaching FCD to VM", "fcdID", fcdID, "vm", vm.Name())

	err := vm.AttachDisk(ctx, fcdID, datastore, controllerKey, &unitNumber)
	if err != nil {
		return fmt.Errorf("failed to attach disk: %w", err)
	}

	logger.Info("Successfully attached FCD to VM", "fcdID", fcdID, "vm", vm.Name())
	return nil
}

// DetachDisk detaches an FCD from a virtual machine
func (m *FCDManager) DetachDisk(ctx context.Context, vm *object.VirtualMachine, fcdID string) error {
	logger := klog.FromContext(ctx)
	logger.Info("Detaching FCD from VM", "fcdID", fcdID, "vm", vm.Name())

	err := vm.DetachDisk(ctx, fcdID)
	if err != nil {
		return fmt.Errorf("failed to detach disk: %w", err)
	}

	logger.Info("Successfully detached FCD from VM", "fcdID", fcdID, "vm", vm.Name())
	return nil
}

// DeleteFCD deletes a First Class Disk
func (m *FCDManager) DeleteFCD(ctx context.Context, datastoreName string, fcdID string) error {
	logger := klog.FromContext(ctx)
	logger.Info("Deleting FCD", "fcdID", fcdID)

	// Get datastore reference
	ds, err := m.client.GetDatastore(ctx, datastoreName)
	if err != nil {
		return fmt.Errorf("failed to get datastore %s: %w", datastoreName, err)
	}

	// Create object manager
	objMgr := vslm.NewObjectManager(m.client.vimClient)

	task, err := objMgr.Delete(ctx, ds, fcdID)
	if err != nil {
		return fmt.Errorf("failed to delete FCD: %w", err)
	}

	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("failed to wait for delete FCD task: %w", err)
	}

	logger.Info("Successfully deleted FCD", "fcdID", fcdID)
	return nil
}

// ParseDatastorePath parses a datastore path in the format [datastore] path/to/file.vmdk
func ParseDatastorePath(path string) (datastoreName, filePath string, err error) {
	// Remove leading bracket
	if !strings.HasPrefix(path, "[") {
		return "", "", fmt.Errorf("invalid datastore path format: %s", path)
	}

	// Find closing bracket
	closeBracket := strings.Index(path, "]")
	if closeBracket == -1 {
		return "", "", fmt.Errorf("invalid datastore path format: %s", path)
	}

	datastoreName = path[1:closeBracket]
	filePath = strings.TrimSpace(path[closeBracket+1:])

	return datastoreName, filePath, nil
}

// GetDatastoreFromPath extracts the datastore object from a datastore path
func (m *FCDManager) GetDatastoreFromPath(ctx context.Context, path string) (*object.Datastore, error) {
	datastoreName, _, err := ParseDatastorePath(path)
	if err != nil {
		return nil, err
	}

	ds, err := m.client.GetDatastore(ctx, datastoreName)
	if err != nil {
		return nil, fmt.Errorf("failed to get datastore %s: %w", datastoreName, err)
	}

	return ds, nil
}

// ParseCSIVolumeHandle parses a vSphere CSI volume handle
// Format: file://<uuid> or just <uuid>
// Returns the FCD ID
func ParseCSIVolumeHandle(volumeHandle string) (fcdID string, err error) {
	if strings.HasPrefix(volumeHandle, "file://") {
		return strings.TrimPrefix(volumeHandle, "file://"), nil
	}
	// Some formats may just be the UUID
	return volumeHandle, nil
}

// BuildCSIVolumeHandle builds a vSphere CSI volume handle from an FCD ID
func BuildCSIVolumeHandle(fcdID string) string {
	return fmt.Sprintf("file://%s", fcdID)
}

// Close is a no-op as the vslm client uses the parent vim25 client session
func (m *FCDManager) Close(ctx context.Context) error {
	// The vslm.Client doesn't have its own logout method,
	// it shares the session with the parent vim25.Client
	return nil
}
