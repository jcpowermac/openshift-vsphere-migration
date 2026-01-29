package vsphere

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"k8s.io/klog/v2"
)

// VMRelocator handles cross-vCenter VM relocation operations
type VMRelocator struct {
	sourceClient *Client
	targetClient *Client
}

// RelocateConfig holds configuration for VM relocation
type RelocateConfig struct {
	// Target vCenter connection info
	TargetVCenterURL      string
	TargetVCenterUser     string
	TargetVCenterPassword string
	TargetVCenterThumbprint string

	// Target location
	TargetDatacenter  string
	TargetCluster     string
	TargetDatastore   string
	TargetFolder      string
	TargetResourcePool string
	TargetNetwork     string
}

// DummyVMConfig holds configuration for creating a dummy VM
type DummyVMConfig struct {
	Name           string
	Datacenter     string
	Cluster        string
	Datastore      string
	Folder         string
	ResourcePool   string
	Network        string
	NumCPUs        int32
	MemoryMB       int64
}

// NewVMRelocator creates a new VM relocator
func NewVMRelocator(sourceClient, targetClient *Client) *VMRelocator {
	return &VMRelocator{
		sourceClient: sourceClient,
		targetClient: targetClient,
	}
}

// CreateDummyVM creates a minimal VM to attach FCDs to for cross-vCenter migration
func (r *VMRelocator) CreateDummyVM(ctx context.Context, config DummyVMConfig) (*object.VirtualMachine, error) {
	logger := klog.FromContext(ctx)

	if config.Name == "" {
		config.Name = fmt.Sprintf("csi-migration-dummy-%s", uuid.New().String()[:8])
	}

	logger.Info("Creating dummy VM for CSI volume migration",
		"name", config.Name,
		"datacenter", config.Datacenter,
		"cluster", config.Cluster)

	// Set finder datacenter
	dc, err := r.sourceClient.GetDatacenter(ctx, config.Datacenter)
	if err != nil {
		return nil, fmt.Errorf("failed to get datacenter %s: %w", config.Datacenter, err)
	}
	r.sourceClient.finder.SetDatacenter(dc)

	// Get folder
	folder, err := r.sourceClient.GetFolder(ctx, config.Folder)
	if err != nil {
		return nil, fmt.Errorf("failed to get folder %s: %w", config.Folder, err)
	}

	// Get resource pool
	resourcePool, err := r.sourceClient.GetResourcePool(ctx, config.ResourcePool)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource pool %s: %w", config.ResourcePool, err)
	}

	// Get datastore
	datastore, err := r.sourceClient.GetDatastore(ctx, config.Datastore)
	if err != nil {
		return nil, fmt.Errorf("failed to get datastore %s: %w", config.Datastore, err)
	}

	// Create VM config spec
	vmConfigSpec := types.VirtualMachineConfigSpec{
		Name:     config.Name,
		GuestId:  "otherGuest64",
		NumCPUs:  config.NumCPUs,
		MemoryMB: config.MemoryMB,
		Files: &types.VirtualMachineFileInfo{
			VmPathName: fmt.Sprintf("[%s]", datastore.Name()),
		},
		// Add a SCSI controller to attach disks
		DeviceChange: []types.BaseVirtualDeviceConfigSpec{
			&types.VirtualDeviceConfigSpec{
				Operation: types.VirtualDeviceConfigSpecOperationAdd,
				Device: &types.ParaVirtualSCSIController{
					VirtualSCSIController: types.VirtualSCSIController{
						SharedBus: types.VirtualSCSISharingNoSharing,
						VirtualController: types.VirtualController{
							BusNumber: 0,
							VirtualDevice: types.VirtualDevice{
								Key: 1000,
							},
						},
					},
				},
			},
		},
	}

	// Set defaults
	if config.NumCPUs == 0 {
		vmConfigSpec.NumCPUs = 1
	}
	if config.MemoryMB == 0 {
		vmConfigSpec.MemoryMB = 128
	}

	// Create VM
	task, err := folder.CreateVM(ctx, vmConfigSpec, resourcePool, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM: %w", err)
	}

	taskInfo, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for VM creation: %w", err)
	}

	vmRef := taskInfo.Result.(types.ManagedObjectReference)
	vm := object.NewVirtualMachine(r.sourceClient.vimClient, vmRef)

	logger.Info("Successfully created dummy VM", "name", config.Name, "moref", vmRef.Value)
	return vm, nil
}

// DeleteDummyVM deletes a dummy VM used for migration
func (r *VMRelocator) DeleteDummyVM(ctx context.Context, vm *object.VirtualMachine) error {
	logger := klog.FromContext(ctx)
	logger.Info("Deleting dummy VM", "name", vm.Name())

	// Power off if running
	powerState, err := vm.PowerState(ctx)
	if err != nil {
		logger.V(2).Info("Failed to get power state", "error", err)
	} else if powerState == types.VirtualMachinePowerStatePoweredOn {
		task, err := vm.PowerOff(ctx)
		if err != nil {
			return fmt.Errorf("failed to power off VM: %w", err)
		}
		if err := task.Wait(ctx); err != nil {
			return fmt.Errorf("failed to wait for power off: %w", err)
		}
	}

	// Delete VM
	task, err := vm.Destroy(ctx)
	if err != nil {
		return fmt.Errorf("failed to destroy VM: %w", err)
	}

	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("failed to wait for VM destruction: %w", err)
	}

	logger.Info("Successfully deleted dummy VM", "name", vm.Name())
	return nil
}

// RelocateVM performs a cross-vCenter vMotion of a VM to the target vCenter
func (r *VMRelocator) RelocateVM(ctx context.Context, vm *object.VirtualMachine, config RelocateConfig) error {
	logger := klog.FromContext(ctx)
	logger.Info("Relocating VM to target vCenter",
		"vm", vm.Name(),
		"targetVCenter", config.TargetVCenterURL,
		"targetDatacenter", config.TargetDatacenter)

	// Build service locator for target vCenter
	serviceLocator, err := r.buildServiceLocator(config)
	if err != nil {
		return fmt.Errorf("failed to build service locator: %w", err)
	}

	// Get target datacenter
	targetDC, err := r.targetClient.GetDatacenter(ctx, config.TargetDatacenter)
	if err != nil {
		return fmt.Errorf("failed to get target datacenter %s: %w", config.TargetDatacenter, err)
	}
	r.targetClient.finder.SetDatacenter(targetDC)

	// Get target folder
	targetFolder, err := r.targetClient.GetFolder(ctx, config.TargetFolder)
	if err != nil {
		return fmt.Errorf("failed to get target folder %s: %w", config.TargetFolder, err)
	}

	// Get target resource pool
	targetResourcePool, err := r.targetClient.GetResourcePool(ctx, config.TargetResourcePool)
	if err != nil {
		return fmt.Errorf("failed to get target resource pool %s: %w", config.TargetResourcePool, err)
	}

	// Get target datastore
	targetDatastore, err := r.targetClient.GetDatastore(ctx, config.TargetDatastore)
	if err != nil {
		return fmt.Errorf("failed to get target datastore %s: %w", config.TargetDatastore, err)
	}

	// Build relocate spec
	folderRef := targetFolder.Reference()
	poolRef := targetResourcePool.Reference()
	dsRef := targetDatastore.Reference()

	relocateSpec := types.VirtualMachineRelocateSpec{
		Service:  serviceLocator,
		Folder:   &folderRef,
		Pool:     &poolRef,
		Datastore: &dsRef,
	}

	// Relocate the VM
	logger.Info("Starting VM relocation task")
	task, err := vm.Relocate(ctx, relocateSpec, types.VirtualMachineMovePriorityDefaultPriority)
	if err != nil {
		return fmt.Errorf("failed to start relocate task: %w", err)
	}

	// Wait for relocation with progress logging
	if err := r.waitForRelocateTask(ctx, task, vm.Name()); err != nil {
		return fmt.Errorf("relocation failed: %w", err)
	}

	logger.Info("Successfully relocated VM to target vCenter", "vm", vm.Name())
	return nil
}

// buildServiceLocator creates a ServiceLocator for cross-vCenter operations
func (r *VMRelocator) buildServiceLocator(config RelocateConfig) (*types.ServiceLocator, error) {
	return &types.ServiceLocator{
		InstanceUuid:  "", // Will be filled by vCenter
		Url:           config.TargetVCenterURL,
		Credential: &types.ServiceLocatorNamePassword{
			Username: config.TargetVCenterUser,
			Password: config.TargetVCenterPassword,
		},
		SslThumbprint: config.TargetVCenterThumbprint,
	}, nil
}

// waitForRelocateTask waits for a relocate task with progress logging
func (r *VMRelocator) waitForRelocateTask(ctx context.Context, task *object.Task, vmName string) error {
	logger := klog.FromContext(ctx)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	const maxConsecutiveErrors = 3
	var consecutiveErrors int

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Get task progress
			var taskMo mo.Task
			err := task.Properties(ctx, task.Reference(), []string{"info"}, &taskMo)
			if err != nil {
				consecutiveErrors++
				if consecutiveErrors >= maxConsecutiveErrors {
					return fmt.Errorf("failed to get task status after %d consecutive attempts: %w", maxConsecutiveErrors, err)
				}
				logger.V(2).Info("Failed to get task progress, retrying",
					"error", err,
					"attempt", consecutiveErrors,
					"maxAttempts", maxConsecutiveErrors)
				continue
			}
			// Reset error counter on successful query
			consecutiveErrors = 0

			// Check for task completion states
			switch taskMo.Info.State {
			case types.TaskInfoStateSuccess:
				logger.Info("VM relocation task completed successfully", "vm", vmName)
				return nil

			case types.TaskInfoStateError:
				if taskMo.Info.Error != nil {
					return fmt.Errorf("VM relocation task failed: %s", taskMo.Info.Error.LocalizedMessage)
				}
				return fmt.Errorf("VM relocation task failed with unknown error")

			case types.TaskInfoStateRunning, types.TaskInfoStateQueued:
				progress := taskMo.Info.Progress
				logger.Info("VM relocation in progress",
					"vm", vmName,
					"progress", fmt.Sprintf("%d%%", progress),
					"state", taskMo.Info.State)

			default:
				logger.V(2).Info("Unexpected task state",
					"vm", vmName,
					"state", taskMo.Info.State)
			}
		}
	}
}

// GetVMSCSIControllerKey gets the SCSI controller key from a VM
func (r *VMRelocator) GetVMSCSIControllerKey(ctx context.Context, vm *object.VirtualMachine) (int32, error) {
	var vmMo mo.VirtualMachine
	err := vm.Properties(ctx, vm.Reference(), []string{"config.hardware.device"}, &vmMo)
	if err != nil {
		return 0, fmt.Errorf("failed to get VM properties: %w", err)
	}

	for _, device := range vmMo.Config.Hardware.Device {
		switch d := device.(type) {
		case *types.ParaVirtualSCSIController:
			return d.Key, nil
		case *types.VirtualLsiLogicController:
			return d.Key, nil
		case *types.VirtualLsiLogicSASController:
			return d.Key, nil
		case *types.VirtualBusLogicController:
			return d.Key, nil
		}
	}

	return 0, fmt.Errorf("no SCSI controller found on VM")
}

// GetNextFreeUnitNumber finds the next free unit number on a SCSI controller
func (r *VMRelocator) GetNextFreeUnitNumber(ctx context.Context, vm *object.VirtualMachine, controllerKey int32) (int32, error) {
	var vmMo mo.VirtualMachine
	err := vm.Properties(ctx, vm.Reference(), []string{"config.hardware.device"}, &vmMo)
	if err != nil {
		return 0, fmt.Errorf("failed to get VM properties: %w", err)
	}

	usedUnits := make(map[int32]bool)
	for _, device := range vmMo.Config.Hardware.Device {
		if disk, ok := device.(*types.VirtualDisk); ok {
			if disk.ControllerKey == controllerKey && disk.UnitNumber != nil {
				usedUnits[*disk.UnitNumber] = true
			}
		}
	}

	// Find first free unit (skip 7 which is reserved for the controller)
	for i := int32(0); i < 16; i++ {
		if i == 7 {
			continue // Reserved for SCSI controller
		}
		if !usedUnits[i] {
			return i, nil
		}
	}

	return 0, fmt.Errorf("no free unit numbers available on controller")
}

// GetVMFromMoRef gets a VirtualMachine object from a ManagedObjectReference
func (r *VMRelocator) GetVMFromMoRef(ctx context.Context, moRef types.ManagedObjectReference, useTarget bool) *object.VirtualMachine {
	if useTarget {
		return object.NewVirtualMachine(r.targetClient.vimClient, moRef)
	}
	return object.NewVirtualMachine(r.sourceClient.vimClient, moRef)
}
