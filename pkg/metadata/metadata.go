package metadata

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
)

// Metadata represents the installer metadata.json structure for vSphere
// This structure matches the openshift-installer metadata format
type Metadata struct {
	ClusterName       string     `json:"clusterName,omitempty"`
	ClusterID         string     `json:"clusterID,omitempty"`
	InfraID           string     `json:"infraID,omitempty"`
	VCenter           string     `json:"vCenter,omitempty"`
	Username          string     `json:"username,omitempty"`
	Password          string     `json:"password,omitempty"`
	TerraformPlatform string     `json:"terraform_platform,omitempty"`
	VCenters          []VCenters `json:"vcenters,omitempty"`
}

// VCenters represents a vCenter entry in the metadata
type VCenters struct {
	Server       string   `json:"server,omitempty"`
	Port         int      `json:"port,omitempty"`
	Username     string   `json:"username,omitempty"`
	Password     string   `json:"password,omitempty"`
	Datacenters  []string `json:"datacenters,omitempty"`
	DefaultDC    string   `json:"defaultDatacenter,omitempty"`
	Cluster      string   `json:"cluster,omitempty"`
	Datastore    string   `json:"datastore,omitempty"`
	Network      string   `json:"network,omitempty"`
	ResourcePool string   `json:"resourcePool,omitempty"`
	Folder       string   `json:"folder,omitempty"`
}

// MetadataManager handles metadata.json generation
type MetadataManager struct {
	kubeClient kubernetes.Interface
}

// NewMetadataManager creates a new MetadataManager
func NewMetadataManager(kubeClient kubernetes.Interface) *MetadataManager {
	return &MetadataManager{
		kubeClient: kubeClient,
	}
}

// GenerateMetadata creates the metadata structure from migration and infrastructure
func (m *MetadataManager) GenerateMetadata(
	ctx context.Context,
	migration *migrationv1alpha1.VmwareCloudFoundationMigration,
	infra *configv1.Infrastructure,
	credentials map[string]string,
) (*Metadata, error) {
	logger := klog.FromContext(ctx)
	logger.Info("Generating installer metadata")

	metadata := &Metadata{
		ClusterName:       infra.Status.InfrastructureName,
		ClusterID:         string(infra.Status.InfrastructureName),
		InfraID:           infra.Status.InfrastructureName,
		TerraformPlatform: "vsphere",
	}

	// Build VCenters from failure domains
	vcentersMap := make(map[string]*VCenters)

	for _, fd := range migration.Spec.FailureDomains {
		vc, exists := vcentersMap[fd.Server]
		if !exists {
			vc = &VCenters{
				Server:      fd.Server,
				Port:        443,
				Datacenters: []string{},
			}
			vcentersMap[fd.Server] = vc
		}

		// Add datacenter if not already present
		dcExists := false
		for _, dc := range vc.Datacenters {
			if dc == fd.Topology.Datacenter {
				dcExists = true
				break
			}
		}
		if !dcExists {
			vc.Datacenters = append(vc.Datacenters, fd.Topology.Datacenter)
		}

		// Set other topology fields (from first failure domain)
		if vc.DefaultDC == "" {
			vc.DefaultDC = fd.Topology.Datacenter
		}
		if vc.Cluster == "" {
			vc.Cluster = fd.Topology.ComputeCluster
		}
		if vc.Datastore == "" {
			vc.Datastore = fd.Topology.Datastore
		}
		if vc.Network == "" && len(fd.Topology.Networks) > 0 {
			vc.Network = fd.Topology.Networks[0]
		}
		if vc.ResourcePool == "" {
			vc.ResourcePool = fd.Topology.ResourcePool
		}
		if vc.Folder == "" {
			vc.Folder = fd.Topology.Folder
		}
	}

	// Add credentials to VCenters
	for server, vc := range vcentersMap {
		usernameKey := fmt.Sprintf("%s.username", server)
		passwordKey := fmt.Sprintf("%s.password", server)

		if username, ok := credentials[usernameKey]; ok {
			vc.Username = username
		}
		if password, ok := credentials[passwordKey]; ok {
			vc.Password = password
		}
		metadata.VCenters = append(metadata.VCenters, *vc)
	}

	// Set the primary vCenter (first one)
	if len(metadata.VCenters) > 0 {
		metadata.VCenter = metadata.VCenters[0].Server
		metadata.Username = metadata.VCenters[0].Username
		metadata.Password = metadata.VCenters[0].Password
	}

	return metadata, nil
}

// SaveToConfigMap saves the metadata to a ConfigMap
func (m *MetadataManager) SaveToConfigMap(
	ctx context.Context,
	metadata *Metadata,
	namespace string,
	name string,
) error {
	logger := klog.FromContext(ctx)
	logger.Info("Saving metadata to ConfigMap", "namespace", namespace, "name", name)

	// Marshal metadata to JSON
	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Create or update ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "vmware-cloud-foundation-migration",
				"app.kubernetes.io/component":  "metadata",
				"app.kubernetes.io/managed-by": "vmware-cloud-foundation-migration",
			},
		},
		Data: map[string]string{
			"metadata.json": string(metadataJSON),
		},
	}

	// Try to get existing ConfigMap
	existing, err := m.kubeClient.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		// Update existing
		existing.Data = cm.Data
		existing.Labels = cm.Labels
		_, err = m.kubeClient.CoreV1().ConfigMaps(namespace).Update(ctx, existing, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update ConfigMap: %w", err)
		}
		logger.Info("Updated metadata ConfigMap")
	} else {
		// Create new
		_, err = m.kubeClient.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create ConfigMap: %w", err)
		}
		logger.Info("Created metadata ConfigMap")
	}

	return nil
}

// GetMetadataConfigMapName returns the name for the metadata ConfigMap
func GetMetadataConfigMapName(migrationName string) string {
	return fmt.Sprintf("%s-metadata", migrationName)
}
