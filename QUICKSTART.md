# Quick Start Guide

This guide will help you get started with the vSphere Migration Controller implementation.

## Prerequisites

- Go 1.22 or later
- OpenShift cluster (for testing)
- Two vCenter servers (source and target)

## Initial Setup

```bash
# Run the development setup script
./scripts/dev-setup.sh

# This will:
# - Check Go installation
# - Install development tools (golangci-lint, controller-gen)
# - Download dependencies
# - Build the controller
# - Run initial tests
```

## Project Structure

```
├── cmd/                    # Main application entry point
├── pkg/
│   ├── apis/              # CRD API definitions
│   ├── controller/        # Controller logic
│   │   ├── phases/        # Individual migration phases
│   │   └── state/         # State machine
│   ├── vsphere/           # vSphere client
│   ├── openshift/         # OpenShift resource managers
│   ├── backup/            # Backup and restore
│   └── util/              # Utilities
├── test/                  # Tests
├── deploy/                # Deployment manifests
└── scripts/               # Helper scripts
```

## Development Workflow

### 1. Generate CRD Manifests

After modifying the API types in `pkg/apis/migration/v1alpha1/types.go`:

```bash
make manifests
```

This uses controller-gen to generate the CRD from kubebuilder markers. The API group and version are defined in `pkg/apis/migration/v1alpha1/groupversion_info.go`:

```go
// +kubebuilder:object:generate=true
// +groupName=migration.openshift.io
package v1alpha1
```

Generated CRD will be at `deploy/crds/migration.crd.yaml`

### 2. Build the Controller

```bash
make build
```

Binary will be in `bin/vsphere-migration-controller`

### 3. Run Tests

```bash
# Unit tests
make test-unit

# Integration tests (when implemented)
make test-integration

# E2E tests (when implemented, requires E2E_TEST=true)
E2E_TEST=true make test-e2e
```

### 4. Code Quality

```bash
# Format code
make fmt

# Run linter
make lint

# Run vet
make vet
```

### 5. Clean Build

```bash
make clean
```

## Implementing a New Phase

Follow this pattern (see existing phases for examples):

```go
// pkg/controller/phases/phase_XX_name.go

package phases

import (
    "context"
    migrationv1alpha1 "github.com/openshift/vsphere-migration-controller/pkg/apis/migration/v1alpha1"
)

type MyPhase struct {
    executor *PhaseExecutor
}

func NewMyPhase(executor *PhaseExecutor) *MyPhase {
    return &MyPhase{executor: executor}
}

func (p *MyPhase) Name() migrationv1alpha1.MigrationPhase {
    return migrationv1alpha1.PhaseMyPhase
}

func (p *MyPhase) Validate(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
    // Validate prerequisites
    return nil
}

func (p *MyPhase) Execute(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) (*PhaseResult, error) {
    logs := make([]migrationv1alpha1.LogEntry, 0)

    // Do work
    logs = AddLog(logs, migrationv1alpha1.LogLevelInfo, "Phase started", string(p.Name()))

    // Return result
    return &PhaseResult{
        Status:   migrationv1alpha1.PhaseStatusCompleted,
        Message:  "Phase completed successfully",
        Progress: 100,
        Logs:     logs,
    }, nil
}

func (p *MyPhase) Rollback(ctx context.Context, migration *migrationv1alpha1.VSphereMigration) error {
    // Revert changes
    return nil
}
```

Then add to `pkg/controller/reconciler.go`:

```go
case migrationv1alpha1.PhaseMyPhase:
    return phases.NewMyPhase(c.phaseExecutor)
```

## Testing Your Phase

Create a test file `test/unit/phase_XX_name_test.go`:

```go
package unit

import (
    "testing"
    // ... imports
)

func TestMyPhase_Execute(t *testing.T) {
    // Setup
    kubeClient := fake.NewSimpleClientset()
    configClient := configfake.NewSimpleClientset()
    scheme := runtime.NewScheme()

    executor := phases.NewPhaseExecutor(kubeClient, configClient, backup.NewBackupManager(scheme), nil)
    phase := phases.NewMyPhase(executor)

    migration := &migrationv1alpha1.VSphereMigration{
        // ... setup migration
    }

    // Execute
    result, err := phase.Execute(context.Background(), migration)

    // Assert
    if err != nil {
        t.Errorf("unexpected error: %v", err)
    }
    if result.Status != migrationv1alpha1.PhaseStatusCompleted {
        t.Errorf("expected completed, got %s", result.Status)
    }
}
```

## Running the Controller Locally

Once controller integration is complete:

```bash
./bin/vsphere-migration-controller \
  --kubeconfig=$HOME/.kube/config \
  --v=2
```

## Creating a Migration

1. Create secret for target vCenter credentials:

```yaml
# The secret keys must use the format: {vcenter-fqdn}.username and {vcenter-fqdn}.password
apiVersion: v1
kind: Secret
metadata:
  name: vsphere-creds-new
  namespace: openshift-config
type: Opaque
stringData:
  vcenter-new.example.com.username: "administrator@vsphere.local"
  vcenter-new.example.com.password: "password"
```

Note: Source vCenter configuration is automatically read from the existing Infrastructure CRD.

2. Create the migration resource:

```yaml
apiVersion: migration.openshift.io/v1alpha1
kind: VSphereMigration
metadata:
  name: my-migration
  namespace: openshift-config
spec:
  state: Pending
  approvalMode: Automatic
  targetVCenterCredentialsSecret:
    name: vsphere-creds-new
    namespace: openshift-config
  failureDomains:
  - name: us-east-1a
    region: us-east
    zone: us-east-1a
    server: vcenter-new.example.com
    topology:
      datacenter: new-dc
      computeCluster: /new-dc/host/cluster1
      datastore: /new-dc/datastore/ds1
      networks:
      - VM Network
  machineSetConfig:
    replicas: 3
    failureDomain: us-east-1a
  controlPlaneMachineSetConfig:
    failureDomain: us-east-1a
  rollbackOnFailure: true
```

3. Start the migration:

```bash
oc patch vspheremigration my-migration -n openshift-config \
  --type merge -p '{"spec":{"state":"Running"}}'
```

4. Monitor progress:

```bash
# Watch migration status
oc get vspheremigration my-migration -n openshift-config -w

# View detailed status
oc get vspheremigration my-migration -n openshift-config -o yaml

# Check phase logs
oc get vspheremigration my-migration -n openshift-config \
  -o jsonpath='{.status.phaseHistory[*]}' | jq
```

## Current Implementation Status

See `IMPLEMENTATION_STATUS.md` for detailed status.

**Completed:**
- ✅ CRD API definitions
- ✅ vSphere client with logging
- ✅ Phase framework
- ✅ State machine
- ✅ 6 of 15 phases
- ✅ Backup/restore system

**In Progress:**
- 🟡 Remaining 9 phases
- 🟡 Controller informer setup
- 🟡 Full test coverage

**Completed:**
- ✅ CRD manifest generation

**Not Started:**
- 🔴 RBAC manifests
- 🔴 Integration tests
- 🔴 E2E tests

## Next Steps

1. **Implement remaining phases** (highest priority)
   - See `pkg/controller/phases/phase_11_create_workers.go` for TODOs
   - Implement `pkg/openshift/machines.go`
   - Follow the pattern from existing phases

2. **Complete controller integration**
   - Add informers in `cmd/vsphere-migration-controller/main.go`
   - Implement proper status updates

3. **Write comprehensive tests**
   - Unit tests for each phase
   - Integration tests with vcsim
   - E2E tests for full flow

4. **Generate deployment manifests**
   - Run `make manifests` to generate CRD
   - Create RBAC resources
   - Create deployment YAML

## Getting Help

- **Architecture questions**: See `IMPLEMENTATION_SUMMARY.md`
- **Status tracking**: See `IMPLEMENTATION_STATUS.md`
- **User documentation**: See `README.md`
- **Code examples**: See existing phases in `pkg/controller/phases/`

## Common Issues

**Build fails with missing dependencies:**
```bash
go mod download
go mod tidy
```

**Tests fail:**
- Some tests may fail due to incomplete implementation
- This is expected during development
- Focus on implementing phases first

**Linter errors:**
```bash
make fmt
make vet
```

## Useful Resources

- [OpenShift API Documentation](https://docs.openshift.com/container-platform/latest/rest_api/index.html)
- [govmomi Documentation](https://pkg.go.dev/github.com/vmware/govmomi)
- [library-go Examples](https://github.com/openshift/library-go/tree/master/examples)
- [Kubernetes Controller Pattern](https://kubernetes.io/docs/concepts/architecture/controller/)

## Contributing

When implementing new features:

1. Follow the existing code patterns
2. Add comprehensive logging
3. Write unit tests
4. Update documentation
5. Run linter before committing

```bash
make fmt
make lint
make test-unit
```

Happy coding! 🚀
