#!/bin/bash
# Step 1: Enable CustomNoUpgrade feature set with ExternalSnapshotMetadata
# WARNING: This makes the cluster non-upgradable
# Expected wait: ~30 minutes for node rollout
set -euo pipefail

echo "=== Step 1: Enable Feature Gate ==="

# Check current state
CURRENT=$(oc get featuregate cluster -o jsonpath='{.spec.featureSet}' 2>/dev/null || echo "")
if [ "$CURRENT" = "CustomNoUpgrade" ]; then
    echo "Feature set already set to CustomNoUpgrade"
    # Check if ExternalSnapshotMetadata is enabled
    if oc get featuregate cluster -o json | grep -q ExternalSnapshotMetadata; then
        echo "ExternalSnapshotMetadata already in feature gate config"
    fi
else
    echo "Patching featuregate cluster to CustomNoUpgrade with ExternalSnapshotMetadata..."
    oc patch featuregate cluster --type=merge -p '{
      "spec": {
        "customNoUpgrade": {
          "enabled": [
            "ExternalSnapshotMetadata"
          ]
        },
        "featureSet": "CustomNoUpgrade"
      }
    }'
    echo "Feature gate patched. Nodes will begin rolling restarts."
fi

echo ""
echo "Waiting for SnapshotMetadataService CRD to appear..."
for i in $(seq 1 60); do
    if oc get crd snapshotmetadataservices.cbt.storage.k8s.io &>/dev/null; then
        echo "SnapshotMetadataService CRD is available!"
        oc get crd snapshotmetadataservices.cbt.storage.k8s.io
        break
    fi
    echo "  Attempt $i/60: CRD not yet available, waiting 30s..."
    sleep 30
done

if ! oc get crd snapshotmetadataservices.cbt.storage.k8s.io &>/dev/null; then
    echo "ERROR: CRD did not appear after 30 minutes. Check node rollout status:"
    echo "  oc get nodes"
    echo "  oc get machineconfigpool"
    exit 1
fi

echo ""
echo "Checking node rollout status..."
oc get nodes
echo ""
oc get machineconfigpool

echo ""
echo "=== Step 1 Complete ==="
echo "If nodes are still updating, wait for all to be Ready before proceeding."
echo "Monitor with: oc get nodes -w"
