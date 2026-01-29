# vSphere Migration Controller

A Kubernetes controller for automating OpenShift cluster migration from one vCenter to another using a VM recreation approach.

## Overview

This controller automates the complex process of migrating an OpenShift cluster between vCenter environments by:

1. Configuring the cluster to support both source and target vCenters simultaneously
2. Creating new machines in the target vCenter (VMs are automatically provisioned)
3. Waiting for new nodes to join and workloads to migrate
4. Scaling down old machines and removing old vCenter configuration

## Features

- **Standalone Controller**: Built with library-go (not operator-sdk)
- **VM Recreation Approach**: No UUID mapping needed
- **Dual Approval Modes**: Automated or manual approval workflows
- **Extensive Logging**: All vSphere SOAP/REST API calls are logged
- **Automatic Rollback**: Optionally rollback on failure
- **Test-Driven**: Comprehensive tests using govmomi simulator

## Architecture

### Migration Phases

The controller executes migration through 15 sequential phases:

1. **Preflight** - Validate vCenter connectivity and cluster health
2. **Backup** - Backup critical resources for rollback
3. **DisableCVO** - Scale down cluster-version-operator
4. **UpdateSecrets** - Add target vCenter credentials
5. **CreateTags** - Create failure domain tags in target vCenter
6. **CreateFolder** - Create VM folder in target vCenter
7. **UpdateInfrastructure** - Add target vCenter to Infrastructure CRD
8. **UpdateConfig** - Update cloud-provider-config
9. **RestartPods** - Restart vSphere-related pods
10. **MonitorHealth** - Wait for cluster to stabilize
11. **CreateWorkers** - Create new worker machines in target vCenter
12. **RecreateCPMS** - Recreate Control Plane Machine Set
13. **ScaleOldMachines** - Scale down old machines
14. **Cleanup** - Remove source vCenter configuration
15. **Verify** - Final health check and re-enable CVO

## Installation

### Prerequisites

- OpenShift 4.x cluster running on vSphere
- Cluster admin access
- Target vCenter credentials

### Build

```bash
make build
```

### Deploy

```bash
# Apply CRD
oc apply -f deploy/crds/migration.crd.yaml

# Apply RBAC
oc apply -f deploy/rbac/

# Deploy controller
oc apply -f deploy/controller.yaml
```

## Usage

### Create Migration Resource

Create a `VmwareCloudFoundationMigration` resource to start a migration:

```yaml
apiVersion: migration.openshift.io/v1alpha1
kind: VmwareCloudFoundationMigration
metadata:
  name: my-migration
  namespace: openshift-config
spec:
  # Start in Pending state, set to Running when ready
  state: Pending

  # Automatic approval (or Manual for step-by-step)
  approvalMode: Automatic

  # Target vCenter credentials
  # Source vCenter configuration is read from the existing Infrastructure CRD
  targetVCenterCredentialsSecret:
    name: vsphere-creds-new
    namespace: openshift-config

  # Failure domains for target vCenter
  failureDomains:
  - name: us-east-1a
    region: us-east
    zone: us-east-1a
    server: new-vcenter.example.com
    topology:
      datacenter: new-dc
      computeCluster: /new-dc/host/cluster1
      datastore: /new-dc/datastore/ds1
      networks:
      - VM Network

  # Worker machine configuration
  machineSetConfig:
    replicas: 3
    failureDomain: us-east-1a

  # Control plane configuration
  controlPlaneMachineSetConfig:
    failureDomain: us-east-1a

  # Automatically rollback on failure
  rollbackOnFailure: true
```

### Start Migration

```bash
# Set state to Running
oc patch vmwarecloudfoundationmigration my-migration -n openshift-config \
  --type merge -p '{"spec":{"state":"Running"}}'
```

### Monitor Progress

```bash
# Watch migration status
oc get vmwarecloudfoundationmigration my-migration -n openshift-config -w

# View detailed status
oc get vmwarecloudfoundationmigration my-migration -n openshift-config -o yaml

# Check current phase
oc get vmwarecloudfoundationmigration my-migration -n openshift-config \
  -o jsonpath='{.status.phase}'
```

### Manual Approval Mode

For manual approval, set `approvalMode: Manual` and approve each phase:

```bash
# Check if approval needed
oc get vmwarecloudfoundationmigration my-migration -n openshift-config \
  -o jsonpath='{.status.currentPhaseState}'

# Approve phase (implementation TBD - would patch currentPhaseState.approved)
```

### Rollback

```bash
# Trigger manual rollback
oc patch vmwarecloudfoundationmigration my-migration -n openshift-config \
  --type merge -p '{"spec":{"state":"Rollback"}}'
```

## Development

### Generate CRD Manifests

The CRD is generated from the API definitions in `pkg/apis/migration/v1alpha1/` using controller-gen.

The API group and version are defined in `groupversion_info.go`:

```go
// +kubebuilder:object:generate=true
// +groupName=migration.openshift.io
package v1alpha1
```

To regenerate the CRD after making changes to the API:

```bash
make manifests
```

This generates `deploy/crds/migration.crd.yaml` with the correct metadata:
- Group: `migration.openshift.io`
- Version: `v1alpha1`
- Kind: `VmwareCloudFoundationMigration`

### Run Tests

```bash
# Unit tests
make test-unit

# Integration tests (requires govmomi simulator)
make test-integration

# E2E tests (requires E2E_TEST=true)
E2E_TEST=true make test-e2e
```

### Run Locally

```bash
# Build binary
make build

# Run controller locally (requires kubeconfig)
./bin/vmware-cloud-foundation-migration \
  --kubeconfig=$HOME/.kube/config \
  --v=2
```

### Code Quality

```bash
# Format code
make fmt

# Run linter
make lint

# Vet code
make vet
```

## Project Structure

```
vmware-cloud-foundation-migration/
├── cmd/vmware-cloud-foundation-migration/  # Main entrypoint
├── pkg/
│   ├── apis/migration/v1alpha1/       # CRD definitions
│   ├── controller/                    # Controller logic
│   │   ├── phases/                    # Phase implementations
│   │   └── state/                     # State machine
│   ├── vsphere/                       # vSphere client with logging
│   ├── openshift/                     # OpenShift resource management
│   ├── backup/                        # Backup and restore
│   └── util/                          # Utilities
├── test/                              # Tests
├── deploy/                            # Deployment manifests
└── Makefile
```

## API Reference

### VmwareCloudFoundationMigration

The `VmwareCloudFoundationMigration` custom resource defines a migration from one vCenter to another.

#### Spec Fields

- `state` (string): Migration state - `Pending`, `Running`, `Paused`, `Rollback`
- `approvalMode` (string): Approval mode - `Automatic`, `Manual`
- `targetVCenterCredentialsSecret` (object): Secret reference containing target vCenter credentials (source is read from Infrastructure CRD)
- `failureDomains` (array): Failure domains for target vCenter
- `machineSetConfig` (object): Worker machine configuration
- `controlPlaneMachineSetConfig` (object): Control plane configuration
- `rollbackOnFailure` (bool): Automatically rollback on failure

#### Status Fields

- `phase` (string): Current migration phase
- `conditions` (array): Standard Kubernetes conditions
- `phaseHistory` (array): History of completed phases with logs
- `currentPhaseState` (object): Current phase execution state
- `backupManifests` (array): Backup data for rollback
- `startTime` (timestamp): Migration start time
- `completionTime` (timestamp): Migration completion time

## Troubleshooting

### View Controller Logs

```bash
oc logs -n openshift-config deployment/vmware-cloud-foundation-migration -f
```

### View Phase Logs

Phase logs are stored in the migration status:

```bash
oc get vmwarecloudfoundationmigration my-migration -n openshift-config \
  -o jsonpath='{.status.phaseHistory[*].logs}' | jq
```

### Common Issues

**Migration stuck in pending**: Check that `state: Running` is set

**Phase failed**: Check phase logs and controller logs for details

**vCenter connection failed**: Verify credentials in secrets and network connectivity

**Rollback failed**: May need manual intervention to restore resources

## Contributing

This is a reference implementation for vCenter-to-vCenter migration. Contributions welcome!

## License

Apache License 2.0
