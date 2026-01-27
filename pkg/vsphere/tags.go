package vsphere

import (
	"context"
	"fmt"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vapi/tags"
	"github.com/vmware/govmomi/vim25/mo"
	"k8s.io/klog/v2"
)

const (
	// Tag category names
	TagCategoryRegion = "openshift-region"
	TagCategoryZone   = "openshift-zone"

	// Tag category descriptions
	TagCategoryRegionDescription = "OpenShift region for vSphere failure domains"
	TagCategoryZoneDescription   = "OpenShift zone for vSphere failure domains"
)

// CreateTagCategory creates a tag category if it doesn't exist
func (c *Client) CreateTagCategory(ctx context.Context, name, description string, cardinality string) (string, error) {
	logger := klog.FromContext(ctx)

	if c.tagManager == nil {
		return "", fmt.Errorf("tag manager not available (REST API not initialized)")
	}

	// Check if category already exists
	categories, err := c.tagManager.GetCategories(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get tag categories: %w", err)
	}

	for _, cat := range categories {
		if cat.Name == name {
			logger.Info("Tag category already exists", "category", name, "id", cat.ID)
			return cat.ID, nil
		}
	}

	// Create new category
	categoryID, err := c.tagManager.CreateCategory(ctx, &tags.Category{
		Name:            name,
		Description:     description,
		Cardinality:     cardinality,
		AssociableTypes: []string{"Datacenter", "ClusterComputeResource"},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create tag category %s: %w", name, err)
	}

	logger.Info("Created tag category", "category", name, "id", categoryID)
	return categoryID, nil
}

// CreateTag creates a tag in a category if it doesn't exist
func (c *Client) CreateTag(ctx context.Context, categoryID, name, description string) (string, error) {
	logger := klog.FromContext(ctx)

	if c.tagManager == nil {
		return "", fmt.Errorf("tag manager not available (REST API not initialized)")
	}

	// Check if tag already exists
	tagList, err := c.tagManager.GetTagsForCategory(ctx, categoryID)
	if err != nil {
		return "", fmt.Errorf("failed to get tags for category: %w", err)
	}

	for _, tag := range tagList {
		if tag.Name == name {
			logger.Info("Tag already exists", "tag", name, "id", tag.ID)
			return tag.ID, nil
		}
	}

	// Create new tag
	tagID, err := c.tagManager.CreateTag(ctx, &tags.Tag{
		Name:        name,
		Description: description,
		CategoryID:  categoryID,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create tag %s: %w", name, err)
	}

	logger.Info("Created tag", "tag", name, "id", tagID)
	return tagID, nil
}

// AttachTag attaches a tag to an object
func (c *Client) AttachTag(ctx context.Context, tagID string, obj object.Reference) error {
	logger := klog.FromContext(ctx)

	if c.tagManager == nil {
		return fmt.Errorf("tag manager not available (REST API not initialized)")
	}

	err := c.tagManager.AttachTag(ctx, tagID, obj)
	if err != nil {
		return fmt.Errorf("failed to attach tag %s to object: %w", tagID, err)
	}

	// Get object name for logging
	var objName string
	var me mo.ManagedEntity
	pc := property.DefaultCollector(c.vimClient)
	if err := pc.RetrieveOne(ctx, obj.Reference(), []string{"name"}, &me); err == nil {
		objName = me.Name
	}

	logger.Info("Attached tag to object", "tag", tagID, "object", objName)
	return nil
}

// CreateRegionAndZoneTags creates region and zone tag categories and tags
func (c *Client) CreateRegionAndZoneTags(ctx context.Context, region, zone string) (regionTagID, zoneTagID string, err error) {
	logger := klog.FromContext(ctx)
	logger.Info("Creating region and zone tags", "region", region, "zone", zone)

	// Create region category
	regionCatID, err := c.CreateTagCategory(ctx, TagCategoryRegion, TagCategoryRegionDescription, "SINGLE")
	if err != nil {
		return "", "", err
	}

	// Create zone category
	zoneCatID, err := c.CreateTagCategory(ctx, TagCategoryZone, TagCategoryZoneDescription, "SINGLE")
	if err != nil {
		return "", "", err
	}

	// Create region tag
	regionTagID, err = c.CreateTag(ctx, regionCatID, region, fmt.Sprintf("Region: %s", region))
	if err != nil {
		return "", "", err
	}

	// Create zone tag
	zoneTagID, err = c.CreateTag(ctx, zoneCatID, zone, fmt.Sprintf("Zone: %s", zone))
	if err != nil {
		return "", "", err
	}

	logger.Info("Successfully created region and zone tags",
		"region", region,
		"regionTagID", regionTagID,
		"zone", zone,
		"zoneTagID", zoneTagID)

	return regionTagID, zoneTagID, nil
}

// AttachFailureDomainTags attaches region tag to datacenter and zone tag to cluster
func (c *Client) AttachFailureDomainTags(ctx context.Context, regionTagID, zoneTagID string, datacenter *object.Datacenter, cluster *object.ClusterComputeResource) error {
	logger := klog.FromContext(ctx)

	// Attach region tag to datacenter
	if err := c.AttachTag(ctx, regionTagID, datacenter); err != nil {
		return fmt.Errorf("failed to attach region tag to datacenter: %w", err)
	}

	// Attach zone tag to cluster
	if err := c.AttachTag(ctx, zoneTagID, cluster); err != nil {
		return fmt.Errorf("failed to attach zone tag to cluster: %w", err)
	}

	logger.Info("Successfully attached failure domain tags")
	return nil
}
