package vsphere

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vapi/rest"
	"github.com/vmware/govmomi/vapi/tags"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
	"k8s.io/klog/v2"
)

// Client wraps vSphere clients with logging
type Client struct {
	govmomiClient *govmomi.Client
	vimClient     *vim25.Client
	restClient    *rest.Client
	tagManager    *tags.Manager
	finder        *find.Finder
	soapLogger    *SOAPLogger
	restLogger    *RESTLogger
}

// Credentials holds vCenter credentials
type Credentials struct {
	Username string
	Password string
}

// Config holds vCenter connection configuration
type Config struct {
	Server   string
	Insecure bool
}

// NewClient creates a new vSphere client with logging
func NewClient(ctx context.Context, config Config, creds Credentials) (*Client, error) {
	logger := klog.FromContext(ctx)

	// Parse server URL
	var serverURL *url.URL
	var err error
	// If server already has a scheme, use it as-is
	if strings.HasPrefix(config.Server, "http://") || strings.HasPrefix(config.Server, "https://") {
		serverURL, err = url.Parse(config.Server)
		// Only append /sdk if not already present
		if err == nil && !strings.HasSuffix(serverURL.Path, "/sdk") {
			serverURL.Path = serverURL.Path + "/sdk"
		}
	} else {
		serverURL, err = url.Parse(fmt.Sprintf("https://%s/sdk", config.Server))
	}
	if err != nil {
		return nil, fmt.Errorf("failed to parse server URL: %w", err)
	}

	// Set credentials
	serverURL.User = url.UserPassword(creds.Username, creds.Password)

	// Create SOAP logger
	soapLogger := NewSOAPLogger()

	// Create SOAP client
	soapClient := soap.NewClient(serverURL, config.Insecure)

	// Create vim25 client
	vimClient, err := vim25.NewClient(ctx, soapClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create vim25 client: %w", err)
	}

	// Create session manager and login
	sessionManager := session.NewManager(vimClient)
	err = sessionManager.Login(ctx, serverURL.User)
	if err != nil {
		return nil, fmt.Errorf("failed to login to vCenter: %w", err)
	}

	logger.Info("Successfully logged in to vCenter", "server", config.Server)

	// Create govmomi client
	govmomiClient := &govmomi.Client{
		Client:         vimClient,
		SessionManager: sessionManager,
	}

	// Create REST logger
	restLogger := NewRESTLogger()

	// Create REST client
	restClient := rest.NewClient(vimClient)

	// Wrap REST transport with logger
	if restClient.Transport != nil {
		restClient.Transport = restLogger.RoundTrip(restClient.Transport)
	}

	// Login to REST API (non-fatal for testing with vcsim)
	var tagManager *tags.Manager
	err = restClient.Login(ctx, serverURL.User)
	if err != nil {
		logger.V(2).Info("REST API login failed (continuing without tags support)", "error", err)
		// Don't create tag manager if REST login failed
	} else {
		// Create tag manager only if REST login succeeded
		tagManager = tags.NewManager(restClient)
	}

	// Create finder
	finder := find.NewFinder(vimClient)

	return &Client{
		govmomiClient: govmomiClient,
		vimClient:     vimClient,
		restClient:    restClient,
		tagManager:    tagManager,
		finder:        finder,
		soapLogger:    soapLogger,
		restLogger:    restLogger,
	}, nil
}

// Logout logs out from vCenter
func (c *Client) Logout(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	if c.restClient != nil {
		if err := c.restClient.Logout(ctx); err != nil {
			logger.Error(err, "Failed to logout from REST API")
		}
	}

	if c.govmomiClient != nil {
		if err := c.govmomiClient.Logout(ctx); err != nil {
			logger.Error(err, "Failed to logout from vCenter")
			return err
		}
	}

	logger.Info("Successfully logged out from vCenter")
	return nil
}

// GetDatacenter returns a datacenter object
func (c *Client) GetDatacenter(ctx context.Context, name string) (*object.Datacenter, error) {
	dc, err := c.finder.Datacenter(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to find datacenter %s: %w", name, err)
	}
	return dc, nil
}

// GetCluster returns a cluster object
func (c *Client) GetCluster(ctx context.Context, path string) (*object.ClusterComputeResource, error) {
	cluster, err := c.finder.ClusterComputeResource(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to find cluster %s: %w", path, err)
	}
	return cluster, nil
}

// GetFolder returns a folder object
func (c *Client) GetFolder(ctx context.Context, path string) (*object.Folder, error) {
	folder, err := c.finder.Folder(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to find folder %s: %w", path, err)
	}
	return folder, nil
}

// TagManager returns the tag manager
func (c *Client) TagManager() *tags.Manager {
	return c.tagManager
}

// Finder returns the finder
func (c *Client) Finder() *find.Finder {
	return c.finder
}

// VimClient returns the vim25 client
func (c *Client) VimClient() *vim25.Client {
	return c.vimClient
}

// GetSOAPLogs returns SOAP log entries
func (c *Client) GetSOAPLogs() []SOAPLogEntry {
	return c.soapLogger.GetEntries()
}

// GetRESTLogs returns REST log entries
func (c *Client) GetRESTLogs() []RESTLogEntry {
	return c.restLogger.GetEntries()
}

// ClearLogs clears all logged entries
func (c *Client) ClearLogs() {
	c.soapLogger.Clear()
	c.restLogger.Clear()
}
