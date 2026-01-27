package vsphere

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"
	"k8s.io/klog/v2"
)

// CreateVMFolder creates a VM folder if it doesn't exist
func (c *Client) CreateVMFolder(ctx context.Context, datacenterName, folderPath string) (*object.Folder, error) {
	logger := klog.FromContext(ctx)
	logger.Info("Creating VM folder", "datacenter", datacenterName, "path", folderPath)

	// Get datacenter
	dc, err := c.GetDatacenter(ctx, datacenterName)
	if err != nil {
		return nil, err
	}

	// Set datacenter context for finder
	c.finder.SetDatacenter(dc)

	// Get VM folder root
	folders, err := dc.Folders(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get datacenter folders: %w", err)
	}

	vmFolder := folders.VmFolder

	// Parse the folder path
	// Expected format: /{datacenter}/vm/{folder-name} or just {folder-name}
	folderName := folderPath
	if strings.Contains(folderPath, "/vm/") {
		parts := strings.Split(folderPath, "/vm/")
		if len(parts) > 1 {
			folderName = parts[1]
		}
	}

	// Try to find existing folder
	fullPath := path.Join(dc.InventoryPath, "vm", folderName)
	existingFolder, err := c.finder.Folder(ctx, fullPath)
	if err == nil {
		logger.Info("VM folder already exists", "path", fullPath)
		return existingFolder, nil
	}

	// Create the folder
	newFolder, err := vmFolder.CreateFolder(ctx, folderName)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM folder %s: %w", folderName, err)
	}

	logger.Info("Successfully created VM folder", "path", fullPath, "moref", newFolder.Reference())
	return newFolder, nil
}

// GetVMFolder gets a VM folder by path
func (c *Client) GetVMFolder(ctx context.Context, datacenterName, folderPath string) (*object.Folder, error) {
	// Get datacenter
	dc, err := c.GetDatacenter(ctx, datacenterName)
	if err != nil {
		return nil, err
	}

	// Set datacenter context for finder
	c.finder.SetDatacenter(dc)

	// Parse the folder path
	folderName := folderPath
	if strings.Contains(folderPath, "/vm/") {
		parts := strings.Split(folderPath, "/vm/")
		if len(parts) > 1 {
			folderName = parts[1]
		}
	}

	fullPath := path.Join(dc.InventoryPath, "vm", folderName)
	folder, err := c.finder.Folder(ctx, fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to find VM folder %s: %w", fullPath, err)
	}

	return folder, nil
}

// DeleteVMFolder deletes a VM folder
func (c *Client) DeleteVMFolder(ctx context.Context, folder *object.Folder) error {
	logger := klog.FromContext(ctx)

	// Get folder name for logging
	var mo types.ManagedObjectReference
	mo = folder.Reference()

	task, err := folder.Destroy(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete VM folder: %w", err)
	}

	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("failed to wait for folder deletion: %w", err)
	}

	logger.Info("Successfully deleted VM folder", "moref", mo)
	return nil
}
