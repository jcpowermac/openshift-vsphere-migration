# CRD Generation Guide

## Overview

This project uses [controller-gen](https://github.com/kubernetes-sigs/controller-tools) from the Kubebuilder project to generate CustomResourceDefinition (CRD) manifests from Go API definitions.

## How It Works

The CRD is generated from the API types defined in `pkg/apis/migration/v1alpha1/`:

### 1. Package-Level Markers (`groupversion_info.go`)

This file defines the API group name that controller-gen uses to generate the CRD:

```go
// Package v1alpha1 contains API Schema definitions for the migration v1alpha1 API group
// +kubebuilder:object:generate=true
// +groupName=migration.openshift.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "migration.openshift.io", Version: "v1alpha1"}
)
```

**Key markers:**
- `+kubebuilder:object:generate=true` - Enables object generation for this package
- `+groupName=migration.openshift.io` - Sets the API group name for the CRD

### 2. Type-Level Markers (`types.go`)

The main resource type includes markers that define CRD-specific configuration:

```go
// VmwareCloudFoundationMigration represents a migration from one vCenter to another
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=vmwarecloudfoundationmigrations,scope=Namespaced,shortName=vsm
type VmwareCloudFoundationMigration struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   VmwareCloudFoundationMigrationSpec   `json:"spec,omitempty"`
    Status VmwareCloudFoundationMigrationStatus `json:"status,omitempty"`
}
```

**Key markers:**
- `+kubebuilder:object:root=true` - Marks this as a root Kubernetes object (CRD)
- `+kubebuilder:subresource:status` - Enables the status subresource
- `+kubebuilder:resource:...` - Defines resource plural name, scope, and short names

### 3. Field-Level Markers

Individual fields can have validation and default markers:

```go
// State controls the workflow: Pending, Running, Paused, Rollback
// +kubebuilder:validation:Enum=Pending;Running;Paused;Rollback
// +kubebuilder:default=Pending
State MigrationState `json:"state"`
```

## Generating the CRD

### Using Make (Recommended)

```bash
make manifests
```

This runs the controller-gen command configured in the Makefile:

```makefile
manifests:
	controller-gen crd:crdVersions=v1 paths="./pkg/apis/..." output:crd:artifacts:config=deploy/crds
```

### Manual Generation

```bash
controller-gen crd:crdVersions=v1 paths="./pkg/apis/migration/v1alpha1" output:crd:artifacts:config=deploy/crds
```

**Important:** Use `output:crd:artifacts:config` instead of `output:crd:dir` to ensure controller-gen properly reads package-level markers.

## Generated CRD Output

The generated CRD is saved to `deploy/crds/migration.openshift.io_vmwarecloudfoundationmigrations.yaml` with:

- `metadata.name`: `vmwarecloudfoundationmigrations.migration.openshift.io`
- `spec.group`: `migration.openshift.io`
- `spec.versions[0].name`: `v1alpha1`

After generation, you can rename it to `migration.crd.yaml` for consistency:

```bash
mv deploy/crds/migration.openshift.io_vmwarecloudfoundationmigrations.yaml deploy/crds/migration.crd.yaml
```

## Validation

Verify the generated CRD is valid:

```bash
# Dry-run creation (validates against Kubernetes API)
oc create -f deploy/crds/migration.crd.yaml --dry-run=client

# Check metadata fields
grep -E "^  name:|^  group:|^    - name:" deploy/crds/migration.crd.yaml
```

Expected output:
```
  name: vmwarecloudfoundationmigrations.migration.openshift.io
  group: migration.openshift.io
    - name: v1alpha1
```

## Common Issues

### Issue: Empty group or version in generated CRD

**Symptom:**
```yaml
metadata:
  name: vmwarecloudfoundationmigrations.
spec:
  group: ""
  versions:
  - name: ""
```

**Cause:** Missing `groupversion_info.go` file or using wrong controller-gen output flag

**Solution:**
1. Ensure `pkg/apis/migration/v1alpha1/groupversion_info.go` exists with the `+groupName` marker
2. Use `output:crd:artifacts:config` instead of `output:crd:dir`

### Issue: Validation errors when applying CRD

**Symptom:**
```
* metadata.name: Invalid value: "vmwarecloudfoundationmigrations.": a lowercase RFC 1123 subdomain must consist...
* spec.group: Required value
```

**Cause:** CRD was generated without proper group/version metadata

**Solution:** Regenerate the CRD with `make manifests`

## File Reference

### Required Files for CRD Generation

1. **`pkg/apis/migration/v1alpha1/groupversion_info.go`** - Defines API group and version
2. **`pkg/apis/migration/v1alpha1/types.go`** - Defines the CRD types with kubebuilder markers
3. **`pkg/apis/migration/v1alpha1/register.go`** - Registers types with the Kubernetes scheme
4. **`pkg/apis/migration/v1alpha1/zz_generated.deepcopy.go`** - Auto-generated deepcopy methods

### Generated Files

1. **`deploy/crds/migration.crd.yaml`** - The generated CRD manifest (or `migration.openshift.io_vmwarecloudfoundationmigrations.yaml`)

## Controller-gen Version

This project uses controller-gen v0.20.0:

```bash
$ controller-gen --version
Version: v0.20.0
```

## Additional Resources

- [Kubebuilder Book - CRD Generation](https://book.kubebuilder.io/reference/generating-crd.html)
- [Controller-gen Documentation](https://github.com/kubernetes-sigs/controller-tools)
- [Kubebuilder Markers Reference](https://book.kubebuilder.io/reference/markers.html)
