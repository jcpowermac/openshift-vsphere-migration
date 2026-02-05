# OLM Deployment Implementation Plan

## Overview

Add OpenShift Operator Lifecycle Manager (OLM) support to the VMware Cloud Foundation Migration Controller. Since the project uses library-go (not operator-sdk), we'll use manual bundle creation for full control.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Bundle approach | Manual creation | Project uses library-go, not operator-sdk |
| Install modes | OwnNamespace only | Operator needs cluster-admin, runs on masters |
| Channel | `alpha` | API is v1alpha1, matches maturity |
| Initial version | 0.1.0 | Semantic versioning |
| Webhooks | None | CRD has OpenAPI validation already |

## Directory Structure to Create

```
bundle/
├── manifests/
│   ├── vmware-cloud-foundation-migration.clusterserviceversion.yaml
│   ├── migration.openshift.io_vmwarecloudfoundationmigrations.yaml
│   ├── vmware-cloud-foundation-migration_clusterrole.yaml
│   ├── vmware-cloud-foundation-migration_clusterrolebinding.yaml
│   └── vmware-cloud-foundation-migration_serviceaccount.yaml
├── metadata/
│   ├── annotations.yaml
│   └── dependencies.yaml
└── tests/scorecard/
    └── config.yaml
bundle.Dockerfile
catalog/
├── vmware-cloud-foundation-migration-catalog.yaml
└── Dockerfile
config/samples/
└── migration_v1alpha1_vmwarecloudfoundationmigration.yaml
```

## Files to Create

### 1. bundle/manifests/vmware-cloud-foundation-migration.clusterserviceversion.yaml

The core CSV with:
- Operator metadata (name, description, icon, version)
- Deployment spec (copied from `deploy/deployment.yaml`)
- ClusterPermissions (from `deploy/rbac/clusterrole.yaml` + lease permissions)
- Owned CRD definition with spec/status descriptors
- Install modes (OwnNamespace only)

### 2. bundle/metadata/annotations.yaml

```yaml
annotations:
  operators.operatorframework.io.bundle.mediatype.v1: registry+v1
  operators.operatorframework.io.bundle.manifests.v1: manifests/
  operators.operatorframework.io.bundle.metadata.v1: metadata/
  operators.operatorframework.io.bundle.package.v1: vmware-cloud-foundation-migration
  operators.operatorframework.io.bundle.channels.v1: alpha
  operators.operatorframework.io.bundle.channel.default.v1: alpha
  com.redhat.openshift.versions: "v4.14-v4.18"
```

### 3. bundle/metadata/dependencies.yaml

```yaml
dependencies:
- type: olm.package
  value:
    packageName: machine-api-operator
    versionRange: ">=0.1.0"
```

### 4. bundle.Dockerfile

Builds bundle image with labels and copies manifests/metadata.

### 5. catalog/vmware-cloud-foundation-migration-catalog.yaml

File-Based Catalog (FBC) defining package, channel, and bundle entry.

### 6. catalog/Dockerfile

Builds catalog index image using ose-operator-registry.

### 7. bundle/tests/scorecard/config.yaml

Scorecard test configuration for bundle validation.

## Makefile Targets to Add

```makefile
# Variables
VERSION ?= 0.1.0
BUNDLE_IMG ?= quay.io/openshift/vmware-cloud-foundation-migration-bundle:v$(VERSION)
CATALOG_IMG ?= quay.io/openshift/vmware-cloud-foundation-migration-catalog:v$(VERSION)

# Targets
bundle-manifests    # Copy CRD/RBAC to bundle/manifests
bundle-validate     # Validate bundle with operator-sdk
bundle-build        # Build bundle image
bundle-push         # Push bundle image
catalog-build       # Build catalog image
catalog-push        # Push catalog image
catalog-validate    # Validate catalog with opm
olm-deploy          # Deploy via CatalogSource
olm-undeploy        # Remove CatalogSource
scorecard           # Run scorecard tests
bundle-run          # Test bundle locally
release-all         # Full release (operator + bundle + catalog)
opm                 # Install opm tool
operator-sdk        # Install operator-sdk tool
```

## Implementation Order

### Phase 1: Setup
1. Install tools: `opm`, `operator-sdk`
2. Create directory structure
3. Create `bundle/metadata/annotations.yaml`
4. Create `bundle/metadata/dependencies.yaml`

### Phase 2: Bundle Creation
5. Create the ClusterServiceVersion (CSV) - largest file
6. Copy manifests: `make bundle-manifests`
7. Create `bundle.Dockerfile`
8. Create sample CR in `config/samples/`

### Phase 3: Validation
9. Validate bundle: `make bundle-validate`
10. Create scorecard config
11. Run scorecard: `make scorecard`

### Phase 4: Catalog
12. Create File-Based Catalog YAML
13. Create catalog Dockerfile
14. Validate: `make catalog-validate`

### Phase 5: Testing
15. Build/push: `make release-all`
16. Deploy: `make olm-deploy`
17. Create Subscription and test

### Phase 6: CI Integration
18. Add Makefile targets
19. Add CI workflow for bundle validation

## Files to Modify

| File | Change |
|------|--------|
| `Makefile` | Add OLM targets (bundle-*, catalog-*, olm-*, scorecard, tools) |
| `deploy/deployment.yaml` | Change `:latest` to `:v0.1.0` |

## Source Files to Reference

| File | Purpose |
|------|---------|
| `deploy/deployment.yaml` | Copy deployment spec to CSV |
| `deploy/rbac/clusterrole.yaml` | Copy rules to CSV clusterPermissions |
| `deploy/crds/migration.openshift.io_vmwarecloudfoundationmigrations.yaml` | Copy to bundle/manifests |
| `deploy/examples/example-migration.yaml` | Basis for alm-examples in CSV |

## Verification

### 1. Bundle Validation
```bash
operator-sdk bundle validate ./bundle --select-optional suite=operatorframework
```

### 2. Scorecard Tests
```bash
operator-sdk scorecard bundle --wait-time 120s
```

### 3. OLM Installation Test
```bash
# Create namespace
oc create namespace vmware-cloud-foundation-migration

# Deploy CatalogSource
make olm-deploy

# Create OperatorGroup
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: vcfm-og
  namespace: vmware-cloud-foundation-migration
spec:
  targetNamespaces:
  - vmware-cloud-foundation-migration
EOF

# Create Subscription
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: vmware-cloud-foundation-migration
  namespace: vmware-cloud-foundation-migration
spec:
  channel: alpha
  name: vmware-cloud-foundation-migration
  source: vmware-cloud-foundation-migration-catalog
  sourceNamespace: openshift-marketplace
EOF

# Verify
oc get csv -n vmware-cloud-foundation-migration
oc get pods -n vmware-cloud-foundation-migration
```

### 4. Functional Test
```bash
oc apply -f config/samples/migration_v1alpha1_vmwarecloudfoundationmigration.yaml
oc get vmwarecloudfoundationmigrations
```

## Future Upgrades

For v0.2.0, add to CSV:
```yaml
spec:
  replaces: vmware-cloud-foundation-migration.v0.1.0
```

Update catalog channel entries:
```yaml
entries:
- name: vmware-cloud-foundation-migration.v0.2.0
  replaces: vmware-cloud-foundation-migration.v0.1.0
```
