#!/bin/bash
# Step 1: Enable CustomNoUpgrade feature set with ExternalSnapshotMetadata
# WARNING: This makes the cluster non-upgradable
# Expected wait: ~30 minutes for node rollout
#
# Notes (as of Oct 2026):
#   - ExternalSnapshotMetadata has no upstream Kubernetes feature gate (KEP-3314 is out-of-tree);
#     it is an OpenShift gate that makes cluster-csi-snapshot-controller-operator install the
#     SnapshotMetadataService CRD, nothing else.
#   - The gate is enabled by DevPreviewNoUpgrade on OCP 4.20-4.22, and by TechPreviewNoUpgrade or
#     DevPreviewNoUpgrade on OCP 4.23/5.0. CustomNoUpgrade (used here) works on every version.
#     If the cluster already uses TechPreviewNoUpgrade or DevPreviewNoUpgrade, this step does not
#     change the feature set.
#   - The CRD it installs is v1alpha1 on OCP 4.21/4.22 and v1beta1 on OCP 5.0. The e2e tests and
#     step4 use v1beta1, so on OCP <= 4.22 the v1beta1 CRD must be applied manually (step0
#     does this when no CRD exists; see README.md).
#   - The gate is optional: step0 can apply the CRD directly with no node rollout.
set -euo pipefail

echo "=== Step 1: Enable Feature Gate ==="

# Check current state
CURRENT=$(oc get featuregate cluster -o jsonpath='{.spec.featureSet}' 2>/dev/null || echo "")
if [ "$CURRENT" = "TechPreviewNoUpgrade" ] || [ "$CURRENT" = "DevPreviewNoUpgrade" ]; then
    echo "Feature set already set to $CURRENT."
    echo "(ExternalSnapshotMetadata is enabled by DevPreviewNoUpgrade on OCP 4.20+ and by"
    echo " TechPreviewNoUpgrade only on OCP 4.23/5.0+; if the CRD never appears, apply it directly - see step0.)"
    echo "Leaving the feature set unchanged."
    # No rollout is triggered by this script in this case, so a missing CRD will not appear
    # by waiting. Fail fast instead of polling for 30 minutes.
    if ! oc get crd snapshotmetadataservices.cbt.storage.k8s.io &>/dev/null; then
        echo "ERROR: SnapshotMetadataService CRD is not installed, and feature set $CURRENT may not enable"
        echo "ExternalSnapshotMetadata on this cluster version. Install the v1beta1 CRD directly instead"
        echo "(no node rollout needed): run step0-preflight.sh, which prints the oc apply command."
        exit 1
    fi
elif [ "$CURRENT" = "CustomNoUpgrade" ]; then
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
for i in {1..60}; do
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
