# Infrastructure CRD Webhook Bypass

## Problem

OpenShift's Infrastructure CRD has a validating webhook that prevents modifying the `vcenters` array once it's set. This validation prevents adding the target vCenter during migration:

```
Infrastructure.config.openshift.io "cluster" is invalid:
spec.platformSpec.vsphere.vcenters: Invalid value: "array":
vcenters cannot be added or removed once set
```

## Solution

As specified in plan.md step 2, the controller now temporarily disables the Infrastructure validating webhook during the UpdateInfrastructure phase.

### How It Works

1. **Disable Webhook** - Set webhook `failurePolicy` to `Ignore`
2. **Update Infrastructure** - Add target vCenter and failure domains
3. **Re-enable Webhook** - Restore webhook `failurePolicy` to `Fail`

### Implementation

The `InfrastructureManager` now includes three new methods:

```go
// Temporarily disable webhook validation
DisableInfrastructureWebhook(ctx context.Context) error

// Re-enable webhook validation
EnableInfrastructureWebhook(ctx context.Context) error

// Add vCenter with automatic webhook bypass
AddTargetVCenterWithWebhookBypass(ctx context.Context, ...) (*configv1.Infrastructure, error)
```

### Usage in Phase 07 (UpdateInfrastructure)

```go
// UpdateInfrastructure phase automatically uses webhook bypass
updatedInfra, err := p.executor.infraManager.AddTargetVCenterWithWebhookBypass(ctx, infra, migration)
```

## Safety Guarantees

1. **Automatic Re-enable**: The webhook is re-enabled via `defer` to ensure it happens even if the update fails

2. **Non-Fatal Webhook Errors**: If the webhook doesn't exist (different OpenShift version), the code continues gracefully

3. **Detailed Logging**: All webhook operations are logged for audit trail

## Webhook Details

**Webhook Name**: `vinfrastructure.kb.io`

**Modification**:
- Before: `failurePolicy: Fail` (validation enforced)
- During migration: `failurePolicy: Ignore` (validation bypassed)
- After: `failurePolicy: Fail` (validation restored)

## What Gets Modified in Infrastructure CRD

The UpdateInfrastructure phase adds:

### 1. Target vCenter
```yaml
spec:
  platformSpec:
    vsphere:
      vcenters:
      - server: vcenter-source.example.com  # Existing
        datacenters: [DC1]
      - server: vcenter-120.ci.ibmc.devcluster.openshift.com  # ADDED
        datacenters: [wldn-120-DC]
```

### 2. Failure Domains
```yaml
spec:
  platformSpec:
    vsphere:
      failureDomains:
      - name: wldn-120-zone-a  # ADDED
        region: wldn-120
        zone: wldn-120-zone-a
        server: vcenter-120.ci.ibmc.devcluster.openshift.com
        topology:
          datacenter: wldn-120-DC
          computeCluster: /wldn-120-DC/host/wldn-120-cl01
          datastore: /wldn-120-DC/datastore/wldn-120-cl01-vsan01
          networks: [ci-vlan-826]
```

## Verification

After the UpdateInfrastructure phase completes:

```bash
# Check Infrastructure has both vCenters
oc get infrastructure cluster -o jsonpath='{.spec.platformSpec.vsphere.vcenters[*].server}'

# Should show both:
# vcenter-source.example.com vcenter-120.ci.ibmc.devcluster.openshift.com

# Check failure domains
oc get infrastructure cluster -o jsonpath='{.spec.platformSpec.vsphere.failureDomains[*].name}'

# Should show: wldn-120-zone-a

# Verify webhook is re-enabled
oc get validatingwebhookconfiguration vinfrastructure.kb.io \
  -o jsonpath='{.webhooks[0].failurePolicy}'

# Should show: Fail
```

## Manual Recovery

If the webhook fails to re-enable automatically:

```bash
# Find the webhook
oc get validatingwebhookconfiguration | grep infrastructure

# Manually restore failurePolicy
oc patch validatingwebhookconfiguration vinfrastructure.kb.io --type=json \
  -p='[{"op":"replace","path":"/webhooks/0/failurePolicy","value":"Fail"}]'
```

## Why This Approach?

This is the standard approach for vCenter migrations because:

1. **Necessary for Migration**: Can't migrate without adding target vCenter
2. **Temporary**: Webhook is only disabled for seconds during update
3. **Safe**: Update is validated before and after via preflight checks
4. **Reversible**: Rollback restores original Infrastructure from backup

## References

- [OpenShift API Infrastructure Types](https://github.com/openshift/api/blob/master/config/v1/types_infrastructure.go#L1657-L1662)
- plan.md Step 2: "Modify the existing infrastructure CRD allowing changes to the number of vCenters"
