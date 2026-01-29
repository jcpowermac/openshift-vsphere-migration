package openshift

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	migrationv1alpha1 "github.com/openshift/vmware-cloud-foundation-migration/pkg/apis/migration/v1alpha1"
)

const (
	VSphereCredsSecretName      = "vsphere-creds"
	VSphereCredsSecretNamespace = "kube-system"
)

// SecretManager manages secret operations
type SecretManager struct {
	client kubernetes.Interface
}

// NewSecretManager creates a new secret manager
func NewSecretManager(client kubernetes.Interface) *SecretManager {
	return &SecretManager{client: client}
}

// GetVSphereCredsSecret retrieves the vsphere-creds secret
func (m *SecretManager) GetVSphereCredsSecret(ctx context.Context) (*corev1.Secret, error) {
	return m.client.CoreV1().Secrets(VSphereCredsSecretNamespace).Get(ctx, VSphereCredsSecretName, metav1.GetOptions{})
}

// AddTargetVCenterCreds adds target vCenter credentials to the secret
func (m *SecretManager) AddTargetVCenterCreds(ctx context.Context, secret *corev1.Secret, server, username, password string) (*corev1.Secret, error) {
	logger := klog.FromContext(ctx)

	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}

	usernameKey := fmt.Sprintf("%s.username", server)
	passwordKey := fmt.Sprintf("%s.password", server)

	// Check if credentials already exist
	if _, exists := secret.Data[usernameKey]; exists {
		logger.Info("Target vCenter credentials already exist in secret")
		return secret, nil
	}

	// Add credentials
	secret.Data[usernameKey] = []byte(username)
	secret.Data[passwordKey] = []byte(password)

	logger.Info("Adding target vCenter credentials to secret", "server", server)

	// Update secret
	updated, err := m.client.CoreV1().Secrets(VSphereCredsSecretNamespace).Update(ctx, secret, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update secret: %w", err)
	}

	logger.Info("Successfully updated vsphere-creds secret")
	return updated, nil
}

// RemoveSourceVCenterCreds removes source vCenter credentials from the secret
func (m *SecretManager) RemoveSourceVCenterCreds(ctx context.Context, secret *corev1.Secret, server string) (*corev1.Secret, error) {
	logger := klog.FromContext(ctx)

	if secret.Data == nil {
		return secret, nil
	}

	usernameKey := fmt.Sprintf("%s.username", server)
	passwordKey := fmt.Sprintf("%s.password", server)

	// Remove credentials
	delete(secret.Data, usernameKey)
	delete(secret.Data, passwordKey)

	logger.Info("Removing source vCenter credentials from secret", "server", server)

	// Update secret
	updated, err := m.client.CoreV1().Secrets(VSphereCredsSecretNamespace).Update(ctx, secret, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update secret: %w", err)
	}

	logger.Info("Successfully removed source vCenter credentials from secret")
	return updated, nil
}

// GetCredentials retrieves credentials for a vCenter from the secret
func (m *SecretManager) GetCredentials(ctx context.Context, server string) (username, password string, err error) {
	secret, err := m.GetVSphereCredsSecret(ctx)
	if err != nil {
		return "", "", err
	}

	usernameKey := fmt.Sprintf("%s.username", server)
	passwordKey := fmt.Sprintf("%s.password", server)

	usernameBytes, ok := secret.Data[usernameKey]
	if !ok {
		return "", "", fmt.Errorf("username not found for server %s", server)
	}

	passwordBytes, ok := secret.Data[passwordKey]
	if !ok {
		return "", "", fmt.Errorf("password not found for server %s", server)
	}

	return string(usernameBytes), string(passwordBytes), nil
}

// GetVCenterCredsFromSecret retrieves vCenter credentials from a specific secret
func (m *SecretManager) GetVCenterCredsFromSecret(ctx context.Context, namespace, name, server string) (username, password string, err error) {
	secret, err := m.client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("failed to get secret %s/%s: %w", namespace, name, err)
	}

	usernameKey := fmt.Sprintf("%s.username", server)
	passwordKey := fmt.Sprintf("%s.password", server)

	usernameBytes, ok := secret.Data[usernameKey]
	if !ok {
		return "", "", fmt.Errorf("username not found for server %s in secret %s/%s (expected key: %s)", server, namespace, name, usernameKey)
	}

	passwordBytes, ok := secret.Data[passwordKey]
	if !ok {
		return "", "", fmt.Errorf("password not found for server %s in secret %s/%s (expected key: %s)", server, namespace, name, passwordKey)
	}

	return string(usernameBytes), string(passwordBytes), nil
}

// GetTargetVCenterCredentials retrieves the target vCenter credentials secret from the migration spec
func (m *SecretManager) GetTargetVCenterCredentials(ctx context.Context, migration *migrationv1alpha1.VmwareCloudFoundationMigration) (*corev1.Secret, error) {
	secretRef := migration.Spec.TargetVCenterCredentialsSecret
	return m.client.CoreV1().Secrets(secretRef.Namespace).Get(ctx, secretRef.Name, metav1.GetOptions{})
}
