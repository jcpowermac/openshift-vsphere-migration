# Testing the Controller Locally

## What Was Fixed

The controller was not responding to VSphereMigration resources because:

**Problem**: The controller had no informer setup - it wasn't watching for VSphereMigration resources being created or updated.

**Solution**: Added complete informer infrastructure:
1. ✅ Dynamic client to access VSphereMigration resources
2. ✅ Informer to watch for resource changes
3. ✅ Workqueue to handle reconciliation
4. ✅ Event handlers for Add/Update/Delete events
5. ✅ Status update functionality

## Quick Test

### Option 1: Automated Test Script (Recommended)

```bash
# This script will build, check prerequisites, and run the controller
./scripts/test-controller-locally.sh
```

### Option 2: Manual Steps

```bash
# 1. Build the controller
make build

# 2. Run the controller (in one terminal)
./bin/vsphere-migration-controller --kubeconfig=$HOME/.kube/config --v=2

# 3. In another terminal, create a migration
oc create -f deploy/examples/vcenter-120-migration.yaml

# 4. Watch the first terminal for logs like:
# "VSphereMigration added"
# "Enqueuing VSphereMigration"
# "Syncing VSphereMigration"
# "Reconciling migration"
```

## Expected Log Output

When the controller starts, you should see:

```
Starting vSphere Migration Controller
Starting informers
Waiting for informer cache sync
Informer cache synced
Starting controller
Controller started, waiting for shutdown signal
```

When you create a VSphereMigration, you should see:

```
VSphereMigration added
Enqueuing VSphereMigration key="openshift-config/vcenter-120-migration"
Syncing VSphereMigration namespace="openshift-config" name="vcenter-120-migration"
Reconciling migration phase="Preflight" state="Pending"
Migration is pending, waiting for state to be set to Running
Updated migration status phase="Preflight"
```

## Verifying the Controller Works

### 1. Check the migration was created

```bash
oc get vspheremigration -n openshift-config
```

Expected output:
```
NAME                     AGE
vcenter-120-migration    1m
```

### 2. Check the migration status

```bash
oc get vspheremigration vcenter-120-migration -n openshift-config -o yaml
```

You should see `status.phase` populated (e.g., `Preflight`)

### 3. Start the migration

```bash
oc patch vspheremigration vcenter-120-migration -n openshift-config \
  --type merge -p '{"spec":{"state":"Running"}}'
```

You should see the controller logs show:
```
VSphereMigration updated
Enqueuing VSphereMigration
Syncing VSphereMigration
Reconciling migration phase="Preflight" state="Running"
Executing phase: Preflight
```

## Troubleshooting

### Controller doesn't see the migration

**Symptom**: No logs when you create/update a VSphereMigration

**Check**:
1. Is the CRD installed?
   ```bash
   oc get crd vspheremigrations.migration.openshift.io
   ```

2. Is the controller connected to the cluster?
   ```bash
   # Look for these lines in controller output:
   # "Informer cache synced"
   # "Controller started"
   ```

3. Is the migration in the right namespace?
   ```bash
   # Controller watches all namespaces, but example uses openshift-config
   oc get vspheremigration --all-namespaces
   ```

### "Failed to get VSphereMigration" error

**Cause**: Resource might have been deleted or namespace is wrong

**Fix**: Verify the migration exists:
```bash
oc get vspheremigration vcenter-120-migration -n openshift-config
```

### "Failed to update migration status" error

**Cause**: Status subresource might not be enabled on the CRD

**Fix**: Reinstall the CRD (it has `subresource:status` marker):
```bash
oc delete crd vspheremigrations.migration.openshift.io
oc create -f deploy/crds/migration.crd.yaml
```

### Permission errors

**Cause**: The controller needs RBAC permissions

**Note**: When running locally with your kubeconfig, you're using your user's permissions. For in-cluster deployment, you'll need proper RBAC (ServiceAccount, Role, RoleBinding).

## Architecture

The controller now works like this:

```
VSphereMigration Resource (create/update)
           ↓
    Kubernetes API
           ↓
   Dynamic Informer (watches for changes)
           ↓
  Event Handler (AddFunc/UpdateFunc)
           ↓
   EnqueueMigration (adds to workqueue)
           ↓
      Workqueue
           ↓
    sync() (processes queue items)
           ↓
  syncMigrationFromKey() (fetches resource)
           ↓
   syncMigration() (reconciliation logic)
           ↓
 State Machine → Phase Executor → Phases
           ↓
  updateMigrationStatus() (updates resource status)
```

## Key Code Changes

### cmd/vsphere-migration-controller/main.go
- Added dynamic client creation
- Set up informer factory
- Added event handlers for Add/Update/Delete
- Wired informer to controller

### pkg/controller/controller.go
- Added `dynamicClient` and `workqueue` fields
- Added `EnqueueMigration()` method
- Rewrote `sync()` to process workqueue
- Added `syncMigrationFromKey()` to fetch and reconcile
- Added `updateMigrationStatus()` to update status subresource

## Next Steps

Once you've verified the controller responds to migrations:

1. **Test with your vCenter-120 config**:
   ```bash
   oc create -f deploy/examples/vcenter-120-migration.yaml
   ```

2. **Start the migration**:
   ```bash
   oc patch vspheremigration vcenter-120-migration -n openshift-config \
     --type merge -p '{"spec":{"state":"Running"}}'
   ```

3. **Monitor progress**:
   ```bash
   # Watch controller logs for phase execution
   # Watch migration status
   oc get vspheremigration vcenter-120-migration -n openshift-config -w
   ```

4. **Check phase logs**:
   ```bash
   oc get vspheremigration vcenter-120-migration -n openshift-config \
     -o jsonpath='{.status.phaseHistory}' | jq
   ```

## Happy Testing! 🎉

Your controller should now properly detect and respond to VSphereMigration resources!
