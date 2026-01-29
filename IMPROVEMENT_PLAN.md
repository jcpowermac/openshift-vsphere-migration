# Comprehensive Implementation Plan: vSphere Migration Controller Improvements

## Executive Summary

This plan addresses **29 identified issues** across 4 priority levels plus comprehensive test coverage improvements. The plan is organized into 8 phases, estimated at **40-60 hours of implementation work**.

---

## Code Review Findings

### Overview

This is a well-structured Kubernetes controller that manages OpenShift cluster migrations from one vCenter to another. It uses the OpenShift library-go framework with a 16-phase state machine approach. The code demonstrates good architectural patterns but has several areas needing improvement.

---

## Issues Summary

### 🔴 Critical Issues (Should Fix Before Production)

| Issue | Location | Description |
|-------|----------|-------------|
| **Blocking `time.Sleep` in reconciler** | `reconciler.go:140` | `time.Sleep(requeueAfter)` blocks the goroutine, preventing concurrent processing |
| **Insecure TLS hardcoded** | `phase.go:175,228` | `Insecure: true` disables certificate verification for vSphere connections |
| **No leader election** | `main.go` | Multiple replicas could process the same migration simultaneously |
| **No retry logic for vSphere ops** | `pkg/vsphere/*` | Transient failures will immediately fail phases |
| **No operation timeouts** | `pkg/vsphere/client.go` | vSphere operations can hang indefinitely |

### 🟠 High Priority Issues

| Issue | Location | Description |
|-------|----------|-------------|
| **Event recorder unused** | `main.go:133` | Events created but not attached to phases - users can't see progress via `kubectl describe` |
| **No predicates on watch** | `main.go:159-171` | All updates trigger reconciliation, including status-only changes |
| **Resource leak on partial init** | `vsphere/client.go:80-114` | SOAP session not cleaned up if later initialization fails |
| **RestoreAllBackups silently fails** | `backup/restore.go:78-84` | Returns nil even when individual restores fail |
| **Missing finalizer** | `main.go:168-170` | No cleanup when VSphereMigration is deleted |
| **Phase objects recreated each sync** | `reconciler.go:147-184` | Memory-inefficient for high-frequency reconciliation |

### 🟡 Medium Priority Issues

| Issue | Location | Description |
|-------|----------|-------------|
| **No backup integrity checks** | `pkg/backup/` | No checksums to verify backup data hasn't been corrupted |
| **No backup size limits** | `pkg/backup/backup.go` | Could exceed etcd's 1.5MB object limit |
| **SOAP logger not wired** | `vsphere/client.go:68-74` | SOAPLogger created but never hooked into transport |
| **No session keep-alive** | `pkg/vsphere/` | Long operations may fail due to session expiry |
| **Unused VCenterConfig type** | `types.go:76-98` | Dead code that may confuse maintainers |
| **CRD type mismatch** | `types.go` vs `migration.crd.yaml` | FailureDomains definition differs between source and generated CRD |

### 🟢 Low Priority Improvements

| Issue | Location | Description |
|-------|----------|-------------|
| **Missing `+kubebuilder:printcolumn`** | `types.go` | Poor `kubectl get vsm` output |
| **No validation webhook** | N/A | Validation only via markers, no admission webhook |
| **RetryableError unused** | `util/errors.go` | Defined but `IsRetryable()` never called |
| **Missing machine backups** | `phase_02_backup.go:126` | TODO comment indicates incomplete implementation |

---

## Test Coverage Gaps

**Estimated coverage: ~20-30%** (approximately 4,500+ lines lack unit tests)

| Component | Coverage Status |
|-----------|-----------------|
| `pkg/controller/controller.go` | ❌ No tests |
| `pkg/controller/reconciler.go` | ❌ No tests |
| `pkg/controller/state/state_machine.go` | ❌ No tests |
| `pkg/backup/` | ❌ No tests |
| `pkg/openshift/` (6 files) | ❌ No tests |
| `pkg/vsphere/` (partial) | ⚠️ Basic tests only |
| `pkg/controller/phases/` | ⚠️ Validation tests only |
| Integration/E2E tests | ⚠️ Placeholders only |

---

## Implementation Phases

## Phase 1: Critical Runtime Issues (Highest Priority)

**Estimated Time: 8-10 hours**

### 1.1 Fix Blocking Sleep in Reconciler

**File:** `pkg/controller/reconciler.go`

**Problem:** Line 140 uses `time.Sleep(requeueAfter)` which blocks the controller goroutine, preventing concurrent processing of other migrations.

**Solution:** Remove the blocking sleep and use the workqueue's requeue mechanism:

```go
// In syncMigration(), instead of:
if shouldRequeue {
    time.Sleep(requeueAfter)  // REMOVE THIS
}

// Return early and let the caller handle requeue via workqueue.AddAfter()
```

**Changes Required:**
1. Modify `syncMigration()` to return a `(requeueAfter time.Duration, error)` tuple
2. Update `syncMigrationFromKey()` in `controller.go` to call `c.workqueue.AddAfter(key, requeueAfter)` when needed
3. Remove the `time.Sleep` call entirely

---

### 1.2 Add Leader Election

**File:** `cmd/vsphere-migration-controller/main.go`

**Problem:** No leader election means multiple replicas could process the same migration simultaneously.

**Solution:** Add leader election using `k8s.io/client-go/tools/leaderelection`:

**Changes Required:**
1. Add new flags: `--leader-elect`, `--leader-elect-lease-duration`, `--leader-elect-renew-deadline`
2. Create a `Lease` resource lock in a configurable namespace (default: `openshift-vsphere-migration`)
3. Wrap the controller start in a `leaderelection.RunOrDie()` call
4. Update the deployment manifest to set `replicas: 2` (for HA) with leader election enabled

---

### 1.3 Make TLS Configuration Configurable

**Files:** 
- `pkg/vsphere/client.go`
- `pkg/controller/phases/phase.go`
- `pkg/apis/migration/v1alpha1/types.go`

**Problem:** `Insecure: true` is hardcoded, disabling TLS certificate verification.

**Solution:**

1. **Extend `vsphere.Config`:**
```go
type Config struct {
    Server      string
    Insecure    bool
    CACertPath  string        // Path to CA certificate file
    CACertData  []byte        // CA certificate data (PEM format)
    Thumbprint  string        // vCenter certificate thumbprint
}
```

2. **Add TLS config to VSphereMigrationSpec:**
```go
type TLSConfig struct {
    // Insecure disables TLS verification (NOT recommended for production)
    // +kubebuilder:default=false
    Insecure bool `json:"insecure,omitempty"`
    
    // CASecretRef references a Secret containing the CA certificate
    CASecretRef *SecretReference `json:"caSecretRef,omitempty"`
    
    // Thumbprint is the vCenter certificate thumbprint for verification
    Thumbprint string `json:"thumbprint,omitempty"`
}
```

3. **Update `vsphere.NewClient()`** to configure TLS based on the new options

---

### 1.4 Add Retry Logic for vSphere Operations

**Files:**
- `pkg/vsphere/client.go` (new `retry.go` file recommended)
- `pkg/vsphere/tags.go`
- `pkg/vsphere/folder.go`

**Problem:** No retry logic for transient vSphere failures (network issues, rate limiting).

**Solution:**

1. **Create `pkg/vsphere/retry.go`:**
```go
func (c *Client) withRetry(ctx context.Context, op func() error) error {
    backoff := wait.Backoff{
        Duration: 1 * time.Second,
        Factor:   2.0,
        Jitter:   0.1,
        Steps:    5,
        Cap:      30 * time.Second,
    }
    return retry.OnError(backoff, isRetryableError, op)
}

func isRetryableError(err error) bool {
    // Check for network errors, timeout, session expiry, etc.
    // soap.IsVimFault, net.Error, context.DeadlineExceeded
}
```

2. **Wrap all vSphere operations** in `tags.go` and `folder.go` with the retry wrapper

---

### 1.5 Add Operation Timeouts

**Files:**
- `pkg/vsphere/client.go`
- All phase files that call vSphere methods

**Problem:** vSphere operations can hang indefinitely with no timeout.

**Solution:**

1. **Add timeout configuration:**
```go
type Config struct {
    // ... existing fields
    ConnectTimeout   time.Duration  // Default: 30s
    OperationTimeout time.Duration  // Default: 5m
}
```

2. **Apply timeouts in client creation and operations:**
```go
func (c *Client) CreateTag(ctx context.Context, ...) error {
    ctx, cancel := context.WithTimeout(ctx, c.config.OperationTimeout)
    defer cancel()
    // ... operation
}
```

---

## Phase 2: High Priority Issues

**Estimated Time: 6-8 hours**

### 2.1 Wire Up Event Recorder

**Files:**
- `pkg/controller/controller.go`
- `pkg/controller/phases/phase.go`
- `pkg/controller/reconciler.go`

**Problem:** Event recorder is created but never used - users can't see migration progress via `kubectl describe`.

**Solution:**

1. Pass `events.Recorder` to `PhaseExecutor`
2. Add event recording at key points:
   - Phase start: `Event(Normal, "PhaseStarting", "...")`
   - Phase complete: `Event(Normal, "PhaseCompleted", "...")`
   - Phase failed: `Event(Warning, "PhaseFailed", "...")`
   - Rollback initiated: `Event(Warning, "RollbackInitiated", "...")`
   - Migration completed: `Event(Normal, "MigrationCompleted", "...")`

---

### 2.2 Add Predicates to Filter Status Updates

**File:** `cmd/vsphere-migration-controller/main.go`

**Problem:** All updates trigger reconciliation, including status-only changes (generation unchanged).

**Solution:**

```go
migrationInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
    AddFunc: func(obj interface{}) {
        migrationController.EnqueueMigration(obj)
    },
    UpdateFunc: func(oldObj, newObj interface{}) {
        oldU := oldObj.(*unstructured.Unstructured)
        newU := newObj.(*unstructured.Unstructured)
        // Only enqueue if spec changed (generation increased)
        if oldU.GetGeneration() != newU.GetGeneration() {
            migrationController.EnqueueMigration(newObj)
        }
        // Also enqueue if state changed from Pending to Running
        // (manual state changes won't bump generation)
        oldState, _, _ := unstructured.NestedString(oldU.Object, "spec", "state")
        newState, _, _ := unstructured.NestedString(newU.Object, "spec", "state")
        if oldState != newState {
            migrationController.EnqueueMigration(newObj)
        }
    },
    // ... DeleteFunc
})
```

---

### 2.3 Fix Resource Leak in vSphere Client

**File:** `pkg/vsphere/client.go`

**Problem:** If initialization fails after SOAP login but before completion, the session is not cleaned up.

**Solution:**

```go
func NewClient(ctx context.Context, config Config, creds Credentials) (*Client, error) {
    // Create SOAP client
    vimClient, err := vim25.NewClient(ctx, soapClient)
    // ...
    
    err = sessionManager.Login(ctx, serverURL.User)
    if err != nil {
        return nil, fmt.Errorf("failed to login: %w", err)
    }
    
    // Track that we need cleanup on subsequent failures
    needsCleanup := true
    cleanup := func() {
        if needsCleanup {
            sessionManager.Logout(ctx)
        }
    }
    defer cleanup()
    
    // REST client creation...
    restClient := rest.NewClient(vimClient)
    if err := restClient.Login(ctx, serverURL.User); err != nil {
        // SOAP session will be cleaned up by deferred cleanup
        return nil, fmt.Errorf("failed to login REST client: %w", err)
    }
    
    // Success - disable cleanup
    needsCleanup = false
    return &Client{...}, nil
}
```

---

### 2.4 Fix RestoreAllBackups Error Handling

**File:** `pkg/backup/restore.go`

**Problem:** `RestoreAllBackups()` returns nil even when individual restores fail.

**Solution:**

```go
func (m *RestoreManager) RestoreAllBackups(ctx context.Context, migration *VSphereMigration) error {
    var errs []error
    // ... restore loop
    if err := m.RestoreResource(ctx, &backup); err != nil {
        logger.Error(err, "Failed to restore resource")
        errs = append(errs, fmt.Errorf("restore %s/%s: %w", backup.ResourceType, backup.Name, err))
        // Continue with other restores
    }
    // ...
    if len(errs) > 0 {
        return fmt.Errorf("restore completed with %d errors: %v", len(errs), errors.Join(errs...))
    }
    return nil
}
```

---

### 2.5 Add Finalizer for Cleanup

**Files:**
- `pkg/controller/controller.go`
- `cmd/vsphere-migration-controller/main.go`

**Problem:** No cleanup when VSphereMigration is deleted (vSphere tags/folders remain orphaned).

**Solution:**

1. Define finalizer: `const migrationFinalizer = "migration.openshift.io/finalizer"`

2. Add finalizer logic in reconciler:
```go
func (c *MigrationController) syncMigration(ctx context.Context, migration *VSphereMigration) error {
    // Handle deletion
    if !migration.DeletionTimestamp.IsZero() {
        if containsString(migration.Finalizers, migrationFinalizer) {
            // Perform cleanup (delete vSphere tags/folders if configured)
            if err := c.cleanupVSphereResources(ctx, migration); err != nil {
                return err
            }
            // Remove finalizer
            migration.Finalizers = removeString(migration.Finalizers, migrationFinalizer)
            // Update
        }
        return nil
    }
    
    // Add finalizer if not present
    if !containsString(migration.Finalizers, migrationFinalizer) {
        migration.Finalizers = append(migration.Finalizers, migrationFinalizer)
        // Update
    }
    // ... rest of reconciliation
}
```

---

### 2.6 Cache Phase Implementations

**File:** `pkg/controller/reconciler.go`

**Problem:** Phase objects are recreated on every reconciliation, wasting memory.

**Solution:**

```go
type MigrationController struct {
    // ... existing fields
    phaseCache map[migrationv1alpha1.MigrationPhase]phases.Phase
}

func (c *MigrationController) getPhaseImplementation(phase MigrationPhase) phases.Phase {
    if c.phaseCache == nil {
        c.phaseCache = make(map[MigrationPhase]phases.Phase)
    }
    if p, ok := c.phaseCache[phase]; ok {
        return p
    }
    // Create and cache
    var p phases.Phase
    switch phase {
    case PhasePreflight:
        p = phases.NewPreflightPhase(c.phaseExecutor)
    // ... etc
    }
    c.phaseCache[phase] = p
    return p
}
```

---

## Phase 3: Medium Priority Issues

**Estimated Time: 4-6 hours**

### 3.1 Add Backup Integrity Checks

**Files:**
- `pkg/apis/migration/v1alpha1/types.go`
- `pkg/backup/backup.go`
- `pkg/backup/restore.go`

**Solution:**

1. Add checksum field to `BackupManifest`:
```go
type BackupManifest struct {
    // ... existing fields
    Checksum string `json:"checksum"`  // SHA256 hash of backupData
}
```

2. Calculate checksum during backup:
```go
import "crypto/sha256"

checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(encodedData)))
```

3. Verify checksum during restore before decoding

---

### 3.2 Add Backup Size Limits

**File:** `pkg/backup/backup.go`

**Solution:**

```go
const (
    MaxBackupDataSize  = 500 * 1024  // 500KB per backup
    MaxTotalBackupSize = 1024 * 1024 // 1MB total
)

func (m *BackupManager) AddBackupToMigration(migration *VSphereMigration, backup *BackupManifest) error {
    if len(backup.BackupData) > MaxBackupDataSize {
        return fmt.Errorf("backup %s exceeds maximum size of %d bytes", backup.Name, MaxBackupDataSize)
    }
    
    totalSize := len(backup.BackupData)
    for _, b := range migration.Status.BackupManifests {
        totalSize += len(b.BackupData)
    }
    if totalSize > MaxTotalBackupSize {
        return fmt.Errorf("total backup size would exceed %d bytes", MaxTotalBackupSize)
    }
    // ... continue
}
```

---

### 3.3 Wire SOAP Logger

**File:** `pkg/vsphere/client.go`

**Problem:** SOAPLogger is created but never connected to the transport.

**Solution:**

```go
func NewClient(ctx context.Context, config Config, creds Credentials) (*Client, error) {
    // ...
    soapClient := soap.NewClient(serverURL, config.Insecure)
    
    // Wire up SOAP logger
    soapLogger := NewSOAPLogger()
    soapClient.SetDebug(true)
    soapClient.Dump = soapLogger  // soapLogger implements io.Writer
    
    // ...
}
```

---

### 3.4 Add Session Keep-Alive

**File:** `pkg/vsphere/client.go`

**Solution:**

```go
func (c *Client) StartKeepAlive(ctx context.Context, interval time.Duration) {
    go func() {
        ticker := time.NewTicker(interval)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                if err := c.vimClient.RoundTripper.(*vim25.Client).ServiceContent(ctx); err != nil {
                    klog.Warning("Keep-alive failed, session may have expired")
                }
            }
        }
    }()
}
```

---

### 3.5 Remove Unused VCenterConfig Type

**File:** `pkg/apis/migration/v1alpha1/types.go`

Remove lines 76-98 (`VCenterConfig` struct) that are defined but never used.

---

### 3.6 Fix CRD Type Mismatch

**Files:**
- `pkg/apis/migration/v1alpha1/types.go`
- Regenerate `deploy/crds/migration.crd.yaml`

**Problem:** `FailureDomains` uses `configv1.VSpherePlatformFailureDomainSpec` but the generated CRD has a simplified schema.

**Solution:** :
Add kubebuilder markers to properly handle the external type

---

## Phase 4: Low Priority Improvements

**Estimated Time: 3-4 hours**

### 4.1 Add PrintColumns to CRD

**File:** `pkg/apis/migration/v1alpha1/types.go`

```go
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.spec.state`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type VSphereMigration struct {
```

---

### 4.2 Use RetryableError

**File:** `pkg/controller/reconciler.go`

```go
import "github.com/openshift/vsphere-migration-controller/pkg/util"

result, err := c.phaseExecutor.ExecutePhase(ctx, phase, migration)
if err != nil {
    if util.IsRetryable(err) {
        // Requeue with backoff
        return requeueAfter, nil
    }
    // Permanent failure - trigger rollback
    // ...
}
```

---

### 4.3 Implement Machine Backups

**File:** `pkg/controller/phases/phase_02_backup.go`

Complete the TODO at line 126 to backup Machine and MachineSet resources.

---

## Phase 5: Test Coverage Improvements

**Estimated Time: 20-25 hours**

### 5.1 State Machine Tests (High Priority)

**File:** `test/unit/state_machine_test.go` (new file)

Test cases:
- `TestGetNextPhase_AllPhasesInOrder`
- `TestGetNextPhase_SkipsApprovedPhases`
- `TestShouldExecutePhase_AutomaticMode`
- `TestShouldExecutePhase_ManualModeWithApproval`
- `TestShouldExecutePhase_ManualModeWithoutApproval`
- `TestInitiateRollback_ReverseOrder`
- `TestInitiateRollback_SkipsFailedPhases`
- `TestRecordPhaseCompletion_AddsToHistory`
- `TestApprovePhase_UpdatesState`

---

### 5.2 Reconciler Tests (High Priority)

**File:** `test/unit/reconciler_test.go` (new file)

Test cases:
- `TestSyncMigration_InitializesStatus`
- `TestSyncMigration_PendingState_DoesNotExecute`
- `TestSyncMigration_PausedState_DoesNotExecute`
- `TestSyncMigration_RollbackState_InitiatesRollback`
- `TestSyncMigration_RunningState_ExecutesPhase`
- `TestSyncMigration_PhaseFailure_TriggersRollback`
- `TestSyncMigration_PhaseSuccess_MovesToNextPhase`
- `TestSyncMigration_AllPhasesComplete_SetsCompleted`
- `TestSyncMigration_WaitsForApproval`

---

### 5.3 Backup/Restore Tests (High Priority)

**File:** `test/unit/backup_test.go` (new file)

Test cases:
- `TestBackupResource_Infrastructure`
- `TestBackupResource_Secret`
- `TestBackupResource_ConfigMap`
- `TestBackupResource_InvalidObject`
- `TestAddBackupToMigration_New`
- `TestAddBackupToMigration_Update`
- `TestGetBackup_Exists`
- `TestGetBackup_NotFound`
- `TestRestoreResource_Success`
- `TestRestoreResource_InvalidData`
- `TestRestoreResource_ChecksumMismatch` (after adding checksums)
- `TestRestoreAllBackups_MultipleResources`
- `TestRestoreAllBackups_PartialFailure`

---

### 5.4 Phase Execute/Rollback Tests (Medium Priority)

**Location:** `test/unit/phases/` (new directory)

For each phase, test:
- `Execute` with valid inputs
- `Execute` with invalid inputs
- `Execute` with dependency failures (e.g., vSphere unavailable)
- `Rollback` restores original state

---

### 5.5 OpenShift Helper Tests (Medium Priority)

**Location:** `test/unit/openshift/` (new directory)

- `infrastructure_test.go` - Test Infrastructure CRD operations
- `secrets_test.go` - Test credential management
- `machines_test.go` - Test Machine/MachineSet operations
- `pods_test.go` - Test pod restart operations
- `operators_test.go` - Test ClusterOperator health checks

---

### 5.6 Integration Tests (Lower Priority)

Complete the placeholder tests in `test/integration/migration_integration_test.go`:
- Use envtest for realistic Kubernetes API interactions
- Test full phase sequences with fake clients
- Test rollback flows

---

### 5.7 E2E Tests (Lower Priority)

Complete placeholder tests in `test/e2e/`:
- Requires real vSphere (or vcsim) and Kubernetes cluster
- Test manual approval workflow
- Test pause/resume
- Test rollback on failure

---

## Phase 6: Documentation Updates

**Estimated Time: 2-3 hours**

### 6.1 Update README

- Add TLS configuration examples
- Document leader election configuration
- Add troubleshooting section

### 6.2 Add Architecture Documentation

Create `docs/ARCHITECTURE.md`:
- Controller design overview
- State machine diagram
- Phase descriptions
- Backup/restore mechanism

### 6.3 Add Operations Guide

Create `docs/OPERATIONS.md`:
- Deployment instructions
- Monitoring recommendations
- Common failure scenarios and resolutions

---

## Phase 7: Build/CI Improvements

**Estimated Time: 2-3 hours**

### 7.1 Update Makefile

```makefile
# Add coverage reporting
coverage:
    $(GOTEST) -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html

# Add security scanning
security:
    gosec ./...
    trivy fs --security-checks vuln .
```

### 7.2 Add golangci-lint Configuration

Create `.golangci.yml` with appropriate linters enabled.

### 7.3 Add Pre-commit Hooks

Create `.pre-commit-config.yaml` for code quality checks.

---

## Phase 8: Deployment Manifest Updates

**Estimated Time: 1-2 hours**

### 8.1 Update Deployment for Leader Election

**File:** `deploy/deployment.yaml`

```yaml
spec:
  replicas: 2  # For HA
  template:
    spec:
      containers:
      - name: controller
        args:
        - --leader-elect=true
        - --leader-elect-lease-duration=15s
        - --leader-elect-renew-deadline=10s
```

### 8.2 Add RBAC for Leader Election

**File:** `deploy/rbac/clusterrole.yaml`

Add rules for `coordination.k8s.io/leases` resources.

### 8.3 Add Resource Limits

```yaml
resources:
  requests:
    cpu: 100m
    memory: 256Mi
  limits:
    cpu: 500m
    memory: 512Mi
```

---

## Summary and Priorities

| Phase | Description | Time Estimate | Priority |
|-------|-------------|---------------|----------|
| 1 | Critical Runtime Issues | 8-10 hours | P0 - Block production |
| 2 | High Priority Issues | 6-8 hours | P1 - Should fix before production |
| 3 | Medium Priority Issues | 4-6 hours | P2 - Fix soon after production |
| 4 | Low Priority Improvements | 3-4 hours | P3 - Nice to have |
| 5 | Test Coverage | 20-25 hours | P1 - Critical for reliability |
| 6 | Documentation | 2-3 hours | P2 - Important for maintainability |
| 7 | Build/CI | 2-3 hours | P2 - Important for development |
| 8 | Deployment Updates | 1-2 hours | P1 - Required for HA |

**Total Estimated Time: 46-61 hours**

---

## Recommended Implementation Order

For a phased rollout, we recommend:

### Sprint 1 (Week 1-2): Critical + Tests Foundation
1. Phase 1.1 - Fix blocking sleep
2. Phase 1.2 - Add leader election
3. Phase 1.3 - Make TLS configurable
4. Phase 5.1 - State machine tests
5. Phase 5.2 - Reconciler tests

### Sprint 2 (Week 3): Critical Completion + High Priority Start
1. Phase 1.4 - Add vSphere retry logic
2. Phase 1.5 - Add operation timeouts
3. Phase 2.1 - Wire up event recorder
4. Phase 2.2 - Add predicates
5. Phase 5.3 - Backup/restore tests

### Sprint 3 (Week 4): High Priority Completion
1. Phase 2.3 - Fix vSphere resource leak
2. Phase 2.4 - Fix RestoreAllBackups error handling
3. Phase 2.5 - Add finalizer
4. Phase 2.6 - Cache phase implementations
5. Phase 8 - Deployment manifest updates

### Sprint 4 (Week 5): Medium Priority + Remaining Tests
1. Phase 3 - All medium priority issues
2. Phase 5.4 - Phase Execute/Rollback tests
3. Phase 5.5 - OpenShift helper tests

### Sprint 5 (Week 6): Polish
1. Phase 4 - Low priority improvements
2. Phase 6 - Documentation
3. Phase 7 - Build/CI improvements
4. Phase 5.6-5.7 - Integration and E2E tests
