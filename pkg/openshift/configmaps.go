package openshift

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
)

const (
	CloudProviderConfigMapName      = "cloud-provider-config"
	CloudProviderConfigMapNamespace = "openshift-config"
)

// ConfigMapManager manages ConfigMap operations
type ConfigMapManager struct {
	client kubernetes.Interface
}

// NewConfigMapManager creates a new ConfigMap manager
func NewConfigMapManager(client kubernetes.Interface) *ConfigMapManager {
	return &ConfigMapManager{client: client}
}

// GetCloudProviderConfig gets the cloud-provider-config ConfigMap
func (m *ConfigMapManager) GetCloudProviderConfig(ctx context.Context) (*corev1.ConfigMap, error) {
	return m.client.CoreV1().ConfigMaps(CloudProviderConfigMapNamespace).Get(ctx, CloudProviderConfigMapName, metav1.GetOptions{})
}

// AddTargetVCenterToConfig adds target vCenter to cloud-provider-config
func (m *ConfigMapManager) AddTargetVCenterToConfig(ctx context.Context, cm *corev1.ConfigMap, migration *migrationv1alpha1.VSphereMigration) (*corev1.ConfigMap, error) {
	logger := klog.FromContext(ctx)

	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}

	if len(migration.Spec.FailureDomains) == 0 {
		return nil, fmt.Errorf("no failure domains specified in migration spec")
	}

	// Get current config
	currentConfig := cm.Data["config"]
	if currentConfig == "" {
		return nil, fmt.Errorf("config key not found or empty in ConfigMap")
	}

	// Parse YAML config into a map structure
	var configMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(currentConfig), &configMap); err != nil {
		return nil, fmt.Errorf("failed to parse config as YAML: %w", err)
	}

	// Get or create vcenter section
	vcenterSection, ok := configMap["vcenter"].(map[string]interface{})
	if !ok {
		vcenterSection = make(map[string]interface{})
		configMap["vcenter"] = vcenterSection
	}

	// Extract unique vCenters and datacenters from failure domains
	vCenterMap := make(map[string][]string) // server -> []datacenter
	for _, fd := range migration.Spec.FailureDomains {
		if fd.Server == "" {
			continue
		}
		// Add datacenter to the list for this server
		datacenters, exists := vCenterMap[fd.Server]
		if !exists {
			datacenters = []string{}
		}
		// Check if datacenter already in list
		found := false
		for _, dc := range datacenters {
			if dc == fd.Topology.Datacenter {
				found = true
				break
			}
		}
		if !found {
			datacenters = append(datacenters, fd.Topology.Datacenter)
		}
		vCenterMap[fd.Server] = datacenters
	}

	// Add each target vcenter from failure domains
	for server, datacenters := range vCenterMap {
		// Create vcenter config
		vcenterConfig := map[string]interface{}{
			"server":              server,
			"port":                443,
			"insecureFlag":        true,
			"datacenters":         datacenters,
			"user":                "",
			"password":            "",
			"tenantref":           "",
			"soapRoundtripCount":  0,
			"caFile":              "",
			"thumbprint":          "",
			"secretref":           "",
			"secretName":          "",
			"secretNamespace":     "",
			"ipFamily":            []string{},
		}

		// Add to vcenter section
		vcenterSection[server] = vcenterConfig

		logger.Info("Added vcenter to config", "server", server, "datacenters", datacenters)
	}

	// Marshal back to YAML
	newConfigBytes, err := yaml.Marshal(configMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config to YAML: %w", err)
	}

	// Update ConfigMap
	cm.Data["config"] = string(newConfigBytes)

	updated, err := m.client.CoreV1().ConfigMaps(CloudProviderConfigMapNamespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update cloud-provider-config: %w", err)
	}

	logger.Info("Successfully updated cloud-provider-config")
	return updated, nil
}

// RemoveSourceVCenterFromConfig removes source vCenter from cloud-provider-config
func (m *ConfigMapManager) RemoveSourceVCenterFromConfig(ctx context.Context, cm *corev1.ConfigMap, sourceServer string) (*corev1.ConfigMap, error) {
	logger := klog.FromContext(ctx)

	if cm.Data == nil {
		return cm, nil
	}

	currentConfig := cm.Data["config"]
	if currentConfig == "" {
		return cm, nil
	}

	// Parse YAML config into a map structure
	var configMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(currentConfig), &configMap); err != nil {
		return nil, fmt.Errorf("failed to parse config as YAML: %w", err)
	}

	// Get vcenter section
	vcenterSection, ok := configMap["vcenter"].(map[string]interface{})
	if !ok || vcenterSection == nil {
		// No vcenter section, nothing to remove
		return cm, nil
	}

	// Remove the source vCenter
	delete(vcenterSection, sourceServer)

	logger.Info("Removing source vCenter from cloud-provider-config", "server", sourceServer)

	// Marshal back to YAML
	newConfigBytes, err := yaml.Marshal(configMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config to YAML: %w", err)
	}

	// Update ConfigMap
	cm.Data["config"] = string(newConfigBytes)

	updated, err := m.client.CoreV1().ConfigMaps(CloudProviderConfigMapNamespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update cloud-provider-config: %w", err)
	}

	logger.Info("Successfully removed source vCenter from cloud-provider-config")
	return updated, nil
}
