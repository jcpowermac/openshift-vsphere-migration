package unit

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openshift/vmware-cloud-foundation-migration/pkg/vsphere"
)

func TestGetServerThumbprint(t *testing.T) {
	// Create a test server with a self-signed certificate
	server := httptest.NewTLSServer(nil)
	defer server.Close()

	// Get the thumbprint of the test server's certificate
	ctx := context.Background()
	thumbprint, err := vsphere.GetServerThumbprint(ctx, server.URL)
	if err != nil {
		t.Fatalf("GetServerThumbprint failed: %v", err)
	}

	// Verify thumbprint format (SHA-256 = 32 bytes = 64 hex chars + 31 colons)
	if len(thumbprint) != 95 {
		t.Errorf("Expected thumbprint length 95 (32 bytes with colons), got %d", len(thumbprint))
	}

	// Verify it contains colons
	if !strings.Contains(thumbprint, ":") {
		t.Error("Expected thumbprint to contain colons")
	}

	// Verify each segment is 2 characters
	parts := strings.Split(thumbprint, ":")
	if len(parts) != 32 {
		t.Errorf("Expected 32 parts in thumbprint, got %d", len(parts))
	}

	for i, part := range parts {
		if len(part) != 2 {
			t.Errorf("Expected part %d to be 2 characters, got %d: %s", i, len(part), part)
		}
	}
}

func TestGetServerThumbprint_InvalidURL(t *testing.T) {
	ctx := context.Background()

	// Test with an invalid URL
	_, err := vsphere.GetServerThumbprint(ctx, "not-a-valid-url")
	if err == nil {
		t.Error("Expected error for invalid URL, got nil")
	}
}

func TestGetServerThumbprint_ConnectionRefused(t *testing.T) {
	ctx := context.Background()

	// Test with a port that should refuse connections
	_, err := vsphere.GetServerThumbprint(ctx, "https://127.0.0.1:65534/sdk")
	if err == nil {
		t.Error("Expected error for connection refused, got nil")
	}
}

func TestThumbprintCalculation(t *testing.T) {
	// Create a test certificate
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "test-vcenter.example.com",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("Failed to create certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("Failed to parse certificate: %v", err)
	}

	// Calculate expected thumbprint
	hash := sha256.Sum256(cert.Raw)
	expectedParts := make([]string, len(hash))
	for i, b := range hash {
		expectedParts[i] = fmt.Sprintf("%02X", b)
	}
	expectedThumbprint := strings.Join(expectedParts, ":")

	// Verify expected format
	if len(expectedThumbprint) != 95 {
		t.Errorf("Expected thumbprint length 95, got %d", len(expectedThumbprint))
	}

	// Verify all uppercase hex
	for _, part := range expectedParts {
		if part != strings.ToUpper(part) {
			t.Errorf("Expected uppercase hex, got: %s", part)
		}
	}
}

func TestRelocateConfig_ThumbprintField(t *testing.T) {
	config := vsphere.RelocateConfig{
		TargetVCenterURL:        "https://target-vcenter.example.com/sdk",
		TargetVCenterUser:       "admin@vsphere.local",
		TargetVCenterPassword:   "password123",
		TargetVCenterThumbprint: "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99",
		TargetDatacenter:        "DC1",
		TargetCluster:           "/DC1/host/cluster1",
		TargetDatastore:         "/DC1/datastore/ds1",
		TargetFolder:            "/DC1/vm/folder1",
		TargetResourcePool:      "/DC1/host/cluster1/Resources",
	}

	// Verify the thumbprint is set
	if config.TargetVCenterThumbprint == "" {
		t.Error("Expected TargetVCenterThumbprint to be set")
	}

	// Verify thumbprint format
	if len(strings.Split(config.TargetVCenterThumbprint, ":")) != 32 {
		t.Error("Expected thumbprint to have 32 parts")
	}
}
