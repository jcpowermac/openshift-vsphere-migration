#!/bin/bash
# Deploy vCenter-120 migration to OpenShift cluster

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "=== vSphere Migration Controller Deployment ==="
echo ""
echo "This script will deploy the migration controller and create a migration resource"
echo "for vCenter: vcenter-120.ci.ibmc.devcluster.openshift.com"
echo ""

# Check if oc is available
if ! command -v oc &> /dev/null; then
    echo "Error: oc command not found. Please install OpenShift CLI."
    exit 1
fi

# Check cluster connectivity
if ! oc whoami &> /dev/null; then
    echo "Error: Not logged into OpenShift cluster"
    echo "Please login with: oc login <cluster-url>"
    exit 1
fi

echo "✓ Connected to cluster: $(oc whoami --show-server)"
echo "✓ Logged in as: $(oc whoami)"
echo ""

# Step 1: Create/verify openshift-config namespace
echo "Step 1: Ensuring openshift-config namespace exists..."
oc get namespace openshift-config &> /dev/null || oc create namespace openshift-config
echo "✓ Namespace openshift-config ready"
echo ""

# Step 2: Install CRD
echo "Step 2: Installing VSphereMigration CRD..."
if oc get crd vspheremigrations.migration.openshift.io &> /dev/null; then
    echo "  CRD already exists, updating..."
    oc apply -f "$PROJECT_ROOT/deploy/crds/migration.crd.yaml"
else
    oc create -f "$PROJECT_ROOT/deploy/crds/migration.crd.yaml"
fi
echo "✓ CRD installed"
echo ""

# Step 3: Create credentials secret
echo "Step 3: Creating vCenter credentials secret..."
if oc get secret vcenter-120-creds -n openshift-config &> /dev/null; then
    echo "  Secret already exists, deleting old version..."
    oc delete secret vcenter-120-creds -n openshift-config
fi
oc create -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: vcenter-120-creds
  namespace: openshift-config
type: Opaque
stringData:
  vcenter-120.ci.ibmc.devcluster.openshift.com.username: "administrator@vsphere.local"
  vcenter-120.ci.ibmc.devcluster.openshift.com.password: ""
EOF
echo "✓ Credentials secret created"
echo ""

# Step 4: Create migration resource
echo "Step 4: Creating VSphereMigration resource..."
if oc get vspheremigration vcenter-120-migration -n openshift-config &> /dev/null; then
    echo "  Migration resource already exists"
    echo "  To recreate, run: oc delete vspheremigration vcenter-120-migration -n openshift-config"
    echo "  Then run this script again"
else
    oc create -f "$PROJECT_ROOT/deploy/examples/vcenter-120-migration.yaml"
    echo "✓ Migration resource created"
fi
echo ""

echo "=== Deployment Complete ==="
echo ""
echo "Migration resource: vcenter-120-migration"
echo "Namespace: openshift-config"
echo "Status: Pending (not started)"
echo ""
echo "Next steps:"
echo "1. Verify the migration configuration:"
echo "   oc get vspheremigration vcenter-120-migration -n openshift-config -o yaml"
echo ""
echo "2. When ready to start the migration, set state to Running:"
echo "   oc patch vspheremigration vcenter-120-migration -n openshift-config --type merge -p '{\"spec\":{\"state\":\"Running\"}}'"
echo ""
echo "3. Monitor migration progress:"
echo "   oc get vspheremigration vcenter-120-migration -n openshift-config -w"
echo ""
echo "4. View detailed status:"
echo "   oc describe vspheremigration vcenter-120-migration -n openshift-config"
echo ""
