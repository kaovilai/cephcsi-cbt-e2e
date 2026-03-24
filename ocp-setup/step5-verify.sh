#!/bin/bash
# Step 5: Verify all CBT e2e prerequisites are met
set -euo pipefail

echo "=== Step 5: Verify CBT E2E Prerequisites ==="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

CTRLPLUGIN_LABEL=$(detect_ctrlplugin_label)
echo "Detected ctrlplugin pod label: $CTRLPLUGIN_LABEL"

PASS=0
FAIL=0
WARN=0

check() {
    local desc="$1"
    shift
    if "$@" &>/dev/null; then
        echo "  PASS: $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL: $desc"
        FAIL=$((FAIL + 1))
    fi
}

echo ""
echo "--- Kubernetes Version ---"
K8S_VERSION=$(oc version -o json 2>/dev/null | jq -r '.serverVersion.minor' | tr -d '+')
echo "  Server version: $(oc version -o json 2>/dev/null | jq -r '.serverVersion.gitVersion')"
if [ "$K8S_VERSION" -ge 33 ] 2>/dev/null; then
    echo "  PASS: Kubernetes >= 1.33"
    PASS=$((PASS + 1))
else
    echo "  FAIL: Kubernetes >= 1.33 (got 1.$K8S_VERSION)"
    FAIL=$((FAIL + 1))
fi

echo ""
echo "--- CRDs ---"
check "VolumeSnapshot CRDs" oc get crd volumesnapshots.snapshot.storage.k8s.io
check "SnapshotMetadataService CRD" oc get crd snapshotmetadataservices.cbt.storage.k8s.io

echo ""
echo "--- Storage ---"
check "StorageClass ocs-storagecluster-ceph-rbd" oc get sc ocs-storagecluster-ceph-rbd
check "VolumeSnapshotClass ocs-storagecluster-rbdplugin-snapclass" oc get volumesnapshotclass ocs-storagecluster-rbdplugin-snapclass

echo ""
echo "--- CephCSI ---"
check "CephCSI RBD ctrlplugin deployment" oc get deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE"

# Check sidecar container
CONTAINERS=$(oc get deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[*].name}' 2>/dev/null || echo "")
if echo "$CONTAINERS" | grep -q "csi-snapshot-metadata"; then
    echo "  PASS: csi-snapshot-metadata sidecar container present"
    PASS=$((PASS + 1))
else
    echo "  FAIL: csi-snapshot-metadata sidecar container not found"
    FAIL=$((FAIL + 1))
fi

# Check TLS volume mount
VOLUMES=$(oc get deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" -o json 2>/dev/null | \
  jq -r '.spec.template.spec.volumes[]?.name' || echo "")
if echo "$VOLUMES" | grep -q "csi-snapshot-metadata-server-certs"; then
    echo "  PASS: TLS cert volume mounted"
    PASS=$((PASS + 1))
else
    echo "  FAIL: TLS cert volume not mounted"
    FAIL=$((FAIL + 1))
fi

echo ""
echo "--- Sidecar Pods ---"
PODS=$(oc get pods -n "$NAMESPACE" -l "$CTRLPLUGIN_LABEL" -o name 2>/dev/null)
if [ -n "$PODS" ]; then
    for pod in $PODS; do
        POD_NAME=$(basename "$pod")
        READY=$(oc get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.status.containerStatuses[?(@.name=="csi-snapshot-metadata")].ready}' 2>/dev/null || echo "false")
        if [ "$READY" = "true" ]; then
            echo "  PASS: $POD_NAME csi-snapshot-metadata container is ready"
            PASS=$((PASS + 1))
        else
            echo "  FAIL: $POD_NAME csi-snapshot-metadata container not ready"
            FAIL=$((FAIL + 1))
        fi
    done
else
    echo "  FAIL: No RBD ctrlplugin pods found"
    FAIL=$((FAIL + 1))
fi

echo ""
echo "--- Ceph Toolbox ---"
if oc get pods -n "$NAMESPACE" -l app=rook-ceph-tools --no-headers 2>/dev/null | grep -q "Running"; then
    echo "  PASS: Ceph toolbox pod is running"
    PASS=$((PASS + 1))
    CEPH_VERSION=$(oc exec -n "$NAMESPACE" $(oc get pods -n "$NAMESPACE" -l app=rook-ceph-tools -o jsonpath='{.items[0].metadata.name}') -- ceph version 2>/dev/null | head -1 || echo "unknown")
    echo "  Ceph version: $CEPH_VERSION"
else
    echo "  FAIL: Ceph toolbox pod not running (required for e2e Ceph version check and RBD introspection)"
    echo "    Fix: oc patch storagecluster ocs-storagecluster -n $NAMESPACE --type merge -p '{\"spec\":{\"enableCephTools\": true}}'"
    FAIL=$((FAIL + 1))
fi

echo ""
echo "--- SnapshotMetadataService CR ---"
check "SnapshotMetadataService CR exists" oc get snapshotmetadataservice "${CSI_DRIVER_NAME}"

echo ""
echo "--- Sidecar Logs (last pod) ---"
LAST_POD=$(oc get pods -n "$NAMESPACE" -l "$CTRLPLUGIN_LABEL" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [ -n "$LAST_POD" ]; then
    echo "  Logs from $LAST_POD:"
    oc logs -n "$NAMESPACE" "$LAST_POD" -c csi-snapshot-metadata --tail=10 2>/dev/null | sed 's/^/    /' || echo "    (no logs available)"
fi

echo ""
echo "============================="
echo "Results: $PASS passed, $FAIL failed, $WARN warnings"
if [ "$FAIL" -eq 0 ]; then
    echo "All checks passed! Ready to run e2e tests."
else
    echo "Some checks failed. Review and fix before running e2e tests."
fi
echo "============================="
