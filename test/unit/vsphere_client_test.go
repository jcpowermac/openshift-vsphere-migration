package unit

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/simulator"
	"k8s.io/klog/v2"

	"github.com/openshift/vmware-cloud-foundation-migration/pkg/vsphere"
)

func TestNewClient(t *testing.T) {
	// Start vcsim
	model := simulator.VPX()
	defer model.Remove()

	err := model.Create()
	if err != nil {
		t.Fatalf("Failed to create simulator model: %v", err)
	}

	server := model.Service.NewServer()
	defer server.Close()

	ctx := klog.NewContext(context.Background(), klog.NewKlogr())

	// Create client
	client, err := vsphere.NewClient(ctx,
		vsphere.Config{
			Server:   server.URL.String(),
			Insecure: true,
		},
		vsphere.Credentials{
			Username: simulator.DefaultLogin.Username(),
			Password: func() string { pwd, _ := simulator.DefaultLogin.Password(); return pwd }(),
		})

	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Logout(ctx)

	if client == nil {
		t.Fatal("Client is nil")
	}
}

func TestGetDatacenter(t *testing.T) {
	// Start vcsim
	model := simulator.VPX()
	defer model.Remove()

	err := model.Create()
	if err != nil {
		t.Fatalf("Failed to create simulator model: %v", err)
	}

	server := model.Service.NewServer()
	defer server.Close()

	ctx := klog.NewContext(context.Background(), klog.NewKlogr())

	// Create client
	client, err := vsphere.NewClient(ctx,
		vsphere.Config{
			Server:   server.URL.String(),
			Insecure: true,
		},
		vsphere.Credentials{
			Username: simulator.DefaultLogin.Username(),
			Password: func() string { pwd, _ := simulator.DefaultLogin.Password(); return pwd }(),
		})

	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Logout(ctx)

	// Get datacenter
	dc, err := client.GetDatacenter(ctx, "DC0")
	if err != nil {
		t.Fatalf("Failed to get datacenter: %v", err)
	}

	if dc == nil {
		t.Fatal("Datacenter is nil")
	}
}

func TestCreateTagsAndAttach(t *testing.T) {
	// Start vcsim
	model := simulator.VPX()
	defer model.Remove()

	err := model.Create()
	if err != nil {
		t.Fatalf("Failed to create simulator model: %v", err)
	}

	server := model.Service.NewServer()
	defer server.Close()

	ctx := klog.NewContext(context.Background(), klog.NewKlogr())

	// Create client
	client, err := vsphere.NewClient(ctx,
		vsphere.Config{
			Server:   server.URL.String(),
			Insecure: true,
		},
		vsphere.Credentials{
			Username: simulator.DefaultLogin.Username(),
			Password: func() string { pwd, _ := simulator.DefaultLogin.Password(); return pwd }(),
		})

	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Logout(ctx)

	// Create region and zone tags
	regionTagID, zoneTagID, err := client.CreateRegionAndZoneTags(ctx, "us-east", "us-east-1a")
	if err != nil {
		t.Fatalf("Failed to create tags: %v", err)
	}

	if regionTagID == "" || zoneTagID == "" {
		t.Fatal("Tag IDs are empty")
	}

	// Get datacenter and cluster for tag attachment
	dc, err := client.GetDatacenter(ctx, "DC0")
	if err != nil {
		t.Fatalf("Failed to get datacenter: %v", err)
	}

	cluster, err := client.GetCluster(ctx, "/DC0/host/DC0_C0")
	if err != nil {
		t.Fatalf("Failed to get cluster: %v", err)
	}

	// Attach tags
	err = client.AttachFailureDomainTags(ctx, regionTagID, zoneTagID, dc, cluster)
	if err != nil {
		t.Fatalf("Failed to attach tags: %v", err)
	}
}

func TestCreateVMFolder(t *testing.T) {
	// Start vcsim
	model := simulator.VPX()
	defer model.Remove()

	err := model.Create()
	if err != nil {
		t.Fatalf("Failed to create simulator model: %v", err)
	}

	server := model.Service.NewServer()
	defer server.Close()

	ctx := klog.NewContext(context.Background(), klog.NewKlogr())

	// Create client
	client, err := vsphere.NewClient(ctx,
		vsphere.Config{
			Server:   server.URL.String(),
			Insecure: true,
		},
		vsphere.Credentials{
			Username: simulator.DefaultLogin.Username(),
			Password: func() string { pwd, _ := simulator.DefaultLogin.Password(); return pwd }(),
		})

	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Logout(ctx)

	// Create VM folder
	folder, err := client.CreateVMFolder(ctx, "DC0", "test-cluster-12345")
	if err != nil {
		t.Fatalf("Failed to create VM folder: %v", err)
	}

	if folder == nil {
		t.Fatal("Folder is nil")
	}

	// Verify folder exists
	retrievedFolder, err := client.GetVMFolder(ctx, "DC0", "test-cluster-12345")
	if err != nil {
		t.Fatalf("Failed to get VM folder: %v", err)
	}

	if retrievedFolder == nil {
		t.Fatal("Retrieved folder is nil")
	}
}

func TestSOAPLogging(t *testing.T) {
	// Start vcsim
	model := simulator.VPX()
	defer model.Remove()

	err := model.Create()
	if err != nil {
		t.Fatalf("Failed to create simulator model: %v", err)
	}

	server := model.Service.NewServer()
	defer server.Close()

	ctx := klog.NewContext(context.Background(), klog.NewKlogr())

	// Create client
	client, err := vsphere.NewClient(ctx,
		vsphere.Config{
			Server:   server.URL.String(),
			Insecure: true,
		},
		vsphere.Credentials{
			Username: simulator.DefaultLogin.Username(),
			Password: func() string { pwd, _ := simulator.DefaultLogin.Password(); return pwd }(),
		})

	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Logout(ctx)

	// Clear existing logs
	client.ClearLogs()

	// Perform an operation
	_, err = client.GetDatacenter(ctx, "DC0")
	if err != nil {
		t.Fatalf("Failed to get datacenter: %v", err)
	}

	// Check SOAP logs
	soapLogs := client.GetSOAPLogs()
	if len(soapLogs) == 0 {
		t.Fatal("No SOAP logs recorded")
	}

	// Verify log contains method name
	foundLog := false
	for _, log := range soapLogs {
		if log.Method != "" {
			foundLog = true
			break
		}
	}

	if !foundLog {
		t.Fatal("No SOAP logs with method name found")
	}
}
