#!/bin/bash
# Helper script to get vCenter datastore information

set -e

VCENTER="${VCENTER:-vcenter-120.ci.ibmc.devcluster.openshift.com}"
USERNAME="${USERNAME:-administrator@vsphere.local}"
PASSWORD="${PASSWORD}"
DATACENTER="${DATACENTER:-wldn-120-DC}"
CLUSTER="${CLUSTER:-wldn-120-cl01}"

echo "=== vCenter Information Retrieval ==="
echo "vCenter: $VCENTER"
echo "Datacenter: $DATACENTER"
echo "Cluster: $CLUSTER"
echo ""

# Check if govc is installed
if ! command -v govc &> /dev/null; then
    echo "Error: govc is not installed"
    echo "Install with: go install github.com/vmware/govmomi/govc@latest"
    exit 1
fi

# Set govc environment variables
export GOVC_URL="https://$VCENTER/sdk"
export GOVC_USERNAME="$USERNAME"
export GOVC_PASSWORD="$PASSWORD"
export GOVC_INSECURE=true

echo "Retrieving datastore information..."
echo ""
echo "=== Datastores in datacenter $DATACENTER ==="
govc find "/$DATACENTER" -type s | while read -r datastore; do
    echo "Datastore: $datastore"
    govc datastore.info -ds="$datastore" | grep -E "Name:|Free:|Capacity:" || true
    echo ""
done

echo ""
echo "=== Cluster $CLUSTER datastores ==="
govc cluster.info -cluster="/$DATACENTER/host/$CLUSTER" | grep -A 20 "Datastores:" || true

echo ""
echo "=== Networks ==="
govc find "/$DATACENTER" -type n | grep -v "dvportgroup"

echo ""
echo "=== To update the manifest ==="
echo "1. Choose a datastore from the list above"
echo "2. Update the datastore path in deploy/examples/vcenter-120-migration.yaml"
echo "3. Replace: /wldn-120-DC/datastore/DATASTORE_NAME"
echo "4. With the full path, e.g.: /wldn-120-DC/datastore/vsanDatastore"
