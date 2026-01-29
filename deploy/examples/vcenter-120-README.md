# vCenter-120 Migration Configuration

This directory contains the migration manifest for migrating an OpenShift cluster to vCenter-120.

## vCenter Configuration

- **Server**: vcenter-120.ci.ibmc.devcluster.openshift.com
- **Datacenter**: wldn-120-DC
- **Cluster**: wldn-120-cl01
- **Datastore**: wldn-120-cl01-vsan01 (vSAN)
- **Network**: ci-vlan-826
- **Credentials**: administrator@vsphere.local

## Files

- `vcenter-120-migration.yaml` - Complete migration resource and secret
- `../../scripts/deploy-vcenter-120.sh` - Automated deployment script
- `../../scripts/get-vcenter-info.sh` - Helper to retrieve vCenter information

## Quick Start

### Option 1: Automated Deployment (Recommended)

```bash
# Run the deployment script
./scripts/deploy-vcenter-120.sh
```

This script will:
1. ✓ Verify cluster connectivity
2. ✓ Install the VmwareCloudFoundationMigration CRD
3. ✓ Create the credentials secret
4. ✓ Create the migration resource in Pending state

### Option 2: Manual Deployment

```bash
# 1. Install CRD
oc apply -f deploy/crds/migration.crd.yaml

# 2. Create the migration resource (includes secret)
oc create -f deploy/examples/vcenter-120-migration.yaml

# 3. Verify creation
oc get vmwarecloudfoundationmigration vcenter-120-migration -n openshift-config
```

## Starting the Migration

The migration is created in `Pending` state and will not start automatically.

**To start the migration:**

```bash
oc patch vmwarecloudfoundationmigration vcenter-120-migration -n openshift-config \
  --type merge -p '{"spec":{"state":"Running"}}'
```

## Monitoring

### Watch migration progress

```bash
# Watch for status changes
oc get vmwarecloudfoundationmigration vcenter-120-migration -n openshift-config -w

# View current phase
oc get vmwarecloudfoundationmigration vcenter-120-migration -n openshift-config \
  -o jsonpath='{.status.phase}'

# View detailed status
oc describe vmwarecloudfoundationmigration vcenter-120-migration -n openshift-config
```

### View phase logs

```bash
# Get all phase logs
oc get vmwarecloudfoundationmigration vcenter-120-migration -n openshift-config \
  -o jsonpath='{.status.phaseHistory[*]}' | jq

# Get current phase state
oc get vmwarecloudfoundationmigration vcenter-120-migration -n openshift-config \
  -o jsonpath='{.status.currentPhaseState}' | jq
```

## Migration Phases

The migration will execute through 15 phases:

1. **Preflight** - Validate vCenter connectivity and cluster health
2. **Backup** - Backup critical resources
3. **DisableCVO** - Scale down cluster-version-operator
4. **UpdateSecrets** - Add target vCenter credentials
5. **CreateTags** - Create region/zone tags in target vCenter
6. **CreateFolder** - Create VM folder in target vCenter
7. **UpdateInfrastructure** - Add target vCenter to Infrastructure CRD
8. **UpdateConfig** - Update cloud-provider-config
9. **RestartPods** - Restart vSphere-related pods
10. **MonitorHealth** - Wait for cluster stabilization
11. **CreateWorkers** - Create new worker machines (3 replicas)
12. **RecreateCPMS** - Recreate Control Plane Machine Set
13. **ScaleOldMachines** - Scale down old machines
14. **Cleanup** - Remove source vCenter configuration
15. **Verify** - Final verification and re-enable CVO

## Pausing the Migration

```bash
oc patch vmwarecloudfoundationmigration vcenter-120-migration -n openshift-config \
  --type merge -p '{"spec":{"state":"Paused"}}'
```

## Triggering Rollback

```bash
oc patch vmwarecloudfoundationmigration vcenter-120-migration -n openshift-config \
  --type merge -p '{"spec":{"state":"Rollback"}}'
```

**Note**: Automatic rollback is enabled (`rollbackOnFailure: true`), so the controller will automatically rollback if a phase fails.

## Configuration Details

### Failure Domain

The migration creates a single failure domain:

- **Name**: wldn-120-zone-a
- **Region**: wldn-120
- **Zone**: wldn-120-zone-a

This failure domain maps to:
- Datacenter: wldn-120-DC
- Cluster: /wldn-120-DC/host/wldn-120-cl01
- Datastore: /wldn-120-DC/datastore/wldn-120-cl01-vsan01
- Network: ci-vlan-826

### Worker Configuration

- **Replicas**: 3
- **Failure Domain**: wldn-120-zone-a

### Control Plane Configuration

- **Failure Domain**: wldn-120-zone-a

## Troubleshooting

### View controller logs

```bash
oc logs -n openshift-config deployment/vmware-cloud-foundation-migration -f
```

### Check secret

```bash
oc get secret vcenter-120-creds -n openshift-config -o yaml
```

### Validate CRD

```bash
oc get crd vmwarecloudfoundationmigrations.migration.openshift.io
oc describe crd vmwarecloudfoundationmigrations.migration.openshift.io
```

### Common Issues

**Migration stuck in Pending**
- Ensure you've set `state: Running` to start the migration
- Check controller logs for errors

**Phase failed**
- View phase logs in `.status.phaseHistory`
- Check controller logs for detailed error messages
- Automatic rollback will trigger if `rollbackOnFailure: true`

**vCenter connection failed**
- Verify credentials in the secret
- Test connectivity: `ping vcenter-120.ci.ibmc.devcluster.openshift.com`
- Verify firewall rules allow HTTPS (443) to vCenter

## Cleanup

To remove the migration resource:

```bash
# Delete migration resource
oc delete vmwarecloudfoundationmigration vcenter-120-migration -n openshift-config

# Delete credentials secret
oc delete secret vcenter-120-creds -n openshift-config

# Optionally, remove the CRD (removes all migrations)
oc delete crd vmwarecloudfoundationmigrations.migration.openshift.io
```

## Support

For issues or questions:
- Check the main [README.md](../../README.md)
- Review [TROUBLESHOOTING.md](../../docs/TROUBLESHOOTING.md) (if available)
- File an issue in the project repository
