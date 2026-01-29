#!/bin/bash
# Test the controller locally

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "=== Testing vSphere Migration Controller Locally ==="
echo ""

# Check if controller binary exists
if [ ! -f "$PROJECT_ROOT/bin/vmware-cloud-foundation-migration" ]; then
    echo "Building controller..."
    cd "$PROJECT_ROOT"
    go build -o bin/vmware-cloud-foundation-migration cmd/vmware-cloud-foundation-migration/main.go
    echo "✓ Controller built"
fi

# Check kubeconfig
if [ -z "$KUBECONFIG" ]; then
    KUBECONFIG="$HOME/.kube/config"
fi

if [ ! -f "$KUBECONFIG" ]; then
    echo "Error: kubeconfig not found at $KUBECONFIG"
    echo "Set KUBECONFIG environment variable or ensure ~/.kube/config exists"
    exit 1
fi

echo "✓ Using kubeconfig: $KUBECONFIG"
echo ""

# Check cluster connectivity
if ! oc whoami &> /dev/null; then
    echo "Error: Not connected to OpenShift cluster"
    echo "Please login with: oc login <cluster-url>"
    exit 1
fi

echo "✓ Connected to: $(oc whoami --show-server)"
echo "✓ Logged in as: $(oc whoami)"
echo ""

# Check if CRD is installed
echo "Checking if CRD is installed..."
if ! oc get crd vmwarecloudfoundationmigrations.migration.openshift.io &> /dev/null; then
    echo "CRD not found. Installing..."
    oc apply -f "$PROJECT_ROOT/deploy/crds/migration.crd.yaml"
    echo "✓ CRD installed"
else
    echo "✓ CRD already installed"
fi
echo ""

echo "=== Starting Controller ==="
echo ""
echo "The controller will:"
echo "1. Watch for VmwareCloudFoundationMigration resources in all namespaces"
echo "2. Reconcile migrations when created/updated"
echo "3. Log all activity to stdout"
echo ""
echo "To test:"
echo "1. In another terminal, create a migration:"
echo "   oc create -f deploy/examples/vcenter-120-migration.yaml"
echo ""
echo "2. Watch the controller logs below for activity"
echo ""
echo "3. To stop the controller, press Ctrl+C"
echo ""
echo "========================================"
echo ""

# Run the controller with verbose logging
exec "$PROJECT_ROOT/bin/vmware-cloud-foundation-migration" \
    --kubeconfig="$KUBECONFIG" \
    --v=2
