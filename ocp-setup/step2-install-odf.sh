#!/bin/bash
# Step 2: Install ODF (OpenShift Data Foundation) Operator
# Waits for node rollout from step 1 to complete first
set -euo pipefail

echo "=== Step 2: Install ODF Operator ==="

# Wait for machineconfigpool to finish updating
echo "Waiting for machineconfigpool rollout to complete..."
for i in $(seq 1 90); do
    MASTER_UPDATING=$(oc get mcp master -o jsonpath='{.status.conditions[?(@.type=="Updating")].status}')
    WORKER_UPDATING=$(oc get mcp worker -o jsonpath='{.status.conditions[?(@.type=="Updating")].status}')
    if [ "$MASTER_UPDATING" = "False" ] && [ "$WORKER_UPDATING" = "False" ]; then
        echo "All machineconfigpools are up to date."
        break
    fi
    echo "  Attempt $i/90: master=$MASTER_UPDATING worker=$WORKER_UPDATING, waiting 30s..."
    sleep 30
done

MASTER_UPDATING=$(oc get mcp master -o jsonpath='{.status.conditions[?(@.type=="Updating")].status}')
WORKER_UPDATING=$(oc get mcp worker -o jsonpath='{.status.conditions[?(@.type=="Updating")].status}')
if [ "$MASTER_UPDATING" != "False" ] || [ "$WORKER_UPDATING" != "False" ]; then
    echo "WARNING: MCP still updating after 45 minutes. Proceeding anyway..."
fi

echo ""
echo "Node status:"
oc get nodes
echo ""

# Create openshift-storage namespace
echo "Creating openshift-storage namespace..."
oc create ns openshift-storage 2>/dev/null || echo "Namespace already exists"

# Label the namespace for monitoring
oc label namespace openshift-storage openshift.io/cluster-monitoring=true --overwrite

# Create OperatorGroup
echo "Creating OperatorGroup..."
oc apply -f - <<'EOF'
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: openshift-storage-operatorgroup
  namespace: openshift-storage
spec:
  targetNamespaces:
  - openshift-storage
EOF

# Create Subscription for ODF operator
echo "Creating ODF Subscription..."
# Derive channel dynamically from packagemanifest to avoid hardcoding OCP version
ODF_CHANNEL="${ODF_CHANNEL:-$(oc get packagemanifest odf-operator -n openshift-marketplace -o jsonpath='{.status.defaultChannel}')}"
echo "  Using channel: $ODF_CHANNEL"
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: odf-operator
  namespace: openshift-storage
spec:
  channel: ${ODF_CHANNEL}
  installPlanApproval: Automatic
  name: odf-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
EOF

echo ""
echo "Waiting for ODF operator CSV to succeed..."
for i in $(seq 1 60); do
    # Find any odf-operator CSV regardless of version
    CSV_STATE=$(oc get csv -n openshift-storage 2>/dev/null | grep -i "odf-operator" | awk '{print $NF}' || echo "")
    if [ "$CSV_STATE" = "Succeeded" ]; then
        echo "ODF operator installed successfully!"
        break
    fi
    echo "  Attempt $i/60: CSV state='$CSV_STATE', waiting 15s..."
    sleep 15
done

# Verify CSV reached Succeeded
CSV_STATE=$(oc get csv -n openshift-storage 2>/dev/null | grep -i "odf-operator" | awk '{print $NF}' || echo "")
if [ "$CSV_STATE" != "Succeeded" ]; then
    echo "ERROR: ODF operator CSV did not reach Succeeded (state='$CSV_STATE') after 15 minutes."
    echo "Check: oc get csv -n openshift-storage"
    exit 1
fi

echo ""
echo "Installed CSVs:"
oc get csv -n openshift-storage
echo ""

echo "=== Step 2 Complete ==="
echo "ODF operator is installed. Proceed to step 3 to create StorageCluster."
