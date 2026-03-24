#!/bin/bash
# Step 0: Preflight checks for ODF and CBT E2E requirements
# Validates cluster meets minimum requirements before proceeding
set -euo pipefail

echo "=== Step 0: Preflight Checks ==="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

PASS=0
FAIL=0
WARN=0
FAIL_MSGS=()
WARN_MSGS=()

pass() { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL + 1)); FAIL_MSGS+=("$1"); }
warn() { echo "  WARN: $1"; WARN=$((WARN + 1)); WARN_MSGS+=("$1"); }

# --- Cluster connectivity ---
echo ""
echo "--- Cluster Connectivity ---"
if oc whoami &>/dev/null; then
    USER=$(oc whoami)
    pass "Logged in as '$USER'"
else
    fail "Not logged in to cluster"
    echo "  Run: oc login -u kubeadmin -p <password> <api-url> --insecure-skip-tls-verify"
    exit 1
fi

if oc auth can-i '*' '*' --all-namespaces &>/dev/null; then
    pass "Has cluster-admin privileges"
else
    fail "Does not have cluster-admin privileges (required for ODF install)"
fi

# --- OCP Version ---
echo ""
echo "--- OCP Version ---"
OCP_VERSION=$(oc get clusterversion version -o jsonpath='{.status.desired.version}')
K8S_VERSION=$(oc version -o json | jq -r '.serverVersion.gitVersion')
echo "  OCP: $OCP_VERSION / K8s: $K8S_VERSION"

K8S_MINOR=$(echo "$K8S_VERSION" | sed 's/v1\.\([0-9]*\).*/\1/')
if [ "$K8S_MINOR" -ge 33 ] 2>/dev/null; then
    pass "Kubernetes >= 1.33 (required for CBT API)"
else
    fail "Kubernetes < 1.33 (got $K8S_VERSION). CBT API requires 1.33+"
fi

# --- Nodes ---
echo ""
echo "--- Node Requirements (ODF needs >= 3 workers) ---"
TOTAL_NODES=$(oc get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')
WORKER_NODES=$(oc get nodes -l node-role.kubernetes.io/worker --no-headers 2>/dev/null | wc -l | tr -d ' ')
READY_WORKERS=$(oc get nodes -l node-role.kubernetes.io/worker --no-headers 2>/dev/null | grep -cE '\bReady\b' || echo "0")

echo "  Total nodes: $TOTAL_NODES"
echo "  Worker nodes: $WORKER_NODES"
echo "  Ready workers: $READY_WORKERS"

if [ "$WORKER_NODES" -ge 3 ]; then
    pass "At least 3 worker nodes available"
else
    fail "ODF requires at least 3 worker nodes (found $WORKER_NODES)"
fi

if [ "$READY_WORKERS" -ge 3 ]; then
    pass "At least 3 worker nodes are Ready"
else
    warn "Less than 3 worker nodes are Ready ($READY_WORKERS) - may be rolling from featuregate"
fi

# --- Worker Node Resources (using allocatable, not capacity) ---
echo ""
echo "--- Worker Node Resources (ODF minimums: 8 vCPU, 24Gi RAM per node) ---"
TOTAL_CPU=0
TOTAL_MEM_GI=0
WORKERS=$(oc get nodes -l node-role.kubernetes.io/worker -o jsonpath='{.items[*].metadata.name}')
for node in $WORKERS; do
    # Use allocatable (subtracts OS/kubelet/eviction reservations) instead of capacity
    CPU_RAW=$(oc get node "$node" -o jsonpath='{.status.allocatable.cpu}')
    MEM_KI=$(oc get node "$node" -o jsonpath='{.status.allocatable.memory}' | sed 's/Ki//')
    MEM_GI=$((MEM_KI / 1024 / 1024))

    # Normalize CPU: handle millicores (e.g., "7500m") and integer formats
    if echo "$CPU_RAW" | grep -q 'm$'; then
        CPU_MILLI=$(echo "$CPU_RAW" | sed 's/m$//')
        CPU=$((CPU_MILLI / 1000))
    else
        CPU="$CPU_RAW"
    fi

    echo "  $node: ${CPU_RAW} vCPU (allocatable), ${MEM_GI}Gi RAM (allocatable)"

    TOTAL_CPU=$((TOTAL_CPU + CPU))
    TOTAL_MEM_GI=$((TOTAL_MEM_GI + MEM_GI))

    if [ "$CPU" -ge 8 ] 2>/dev/null; then
        pass "$node has >= 8 vCPU"
    else
        warn "$node has < 8 vCPU ($CPU_RAW) - ODF recommends 8+"
    fi

    # Warn below 24Gi (recommended) or 16Gi (ODF minimum). ODF may still work on smaller nodes.
    if [ "$MEM_GI" -ge 24 ] 2>/dev/null; then
        pass "$node has >= 24Gi RAM"
    elif [ "$MEM_GI" -ge 16 ] 2>/dev/null; then
        warn "$node has < 24Gi RAM (${MEM_GI}Gi) - ODF recommends 24Gi+"
    else
        warn "$node has < 16Gi RAM (${MEM_GI}Gi) - below ODF recommended minimum of 16Gi"
    fi
done

# Aggregate cluster-wide check using ODF performance profiles
echo ""
echo "  Cluster totals (workers): ${TOTAL_CPU} vCPU, ${TOTAL_MEM_GI}Gi RAM"

SELECTED_PROFILE=$(detect_odf_profile "$TOTAL_CPU" "$TOTAL_MEM_GI")
read -r REQ_CPU REQ_MEM <<< "$(odf_profile_requirements "$SELECTED_PROFILE")"

echo ""
echo "  ODF performance profiles:"
echo "    lean:        24 CPU, 72 GiB"
echo "    balanced:    30 CPU, 72 GiB"
echo "    performance: 45 CPU, 96 GiB"
if [ -n "${ODF_PROFILE:-}" ]; then
    echo "  Selected profile: $SELECTED_PROFILE (override via ODF_PROFILE)"
else
    echo "  Auto-selected profile: $SELECTED_PROFILE (override with ODF_PROFILE env var)"
fi
echo "  Requirements: ${REQ_CPU} vCPU, ${REQ_MEM}Gi RAM"

if [ "$TOTAL_CPU" -ge "$REQ_CPU" ] 2>/dev/null; then
    pass "Cluster has >= ${REQ_CPU} vCPU for '$SELECTED_PROFILE' profile"
else
    warn "Cluster has < ${REQ_CPU} vCPU ($TOTAL_CPU) for '$SELECTED_PROFILE' profile"
fi
if [ "$TOTAL_MEM_GI" -ge "$REQ_MEM" ] 2>/dev/null; then
    pass "Cluster has >= ${REQ_MEM}Gi RAM for '$SELECTED_PROFILE' profile"
else
    warn "Cluster has < ${REQ_MEM}Gi RAM (${TOTAL_MEM_GI}Gi) for '$SELECTED_PROFILE' profile"
fi

# Export for downstream scripts (step3)
export ODF_PROFILE="$SELECTED_PROFILE"

# --- Storage for ODF OSDs ---
echo ""
echo "--- Storage Backend for ODF OSDs ---"
SC_COUNT=$(oc get sc --no-headers 2>/dev/null | wc -l | tr -d ' ')
echo "  Available StorageClasses: $SC_COUNT"
oc get sc --no-headers 2>/dev/null | while read -r line; do
    echo "    $line"
done

# Check for OSD storage class (standard-csi on OpenStack, gp3-csi on AWS, thin-csi on vSphere)
OSD_STORAGE_CLASS="${OSD_STORAGE_CLASS:-standard-csi}"
if oc get sc "$OSD_STORAGE_CLASS" &>/dev/null; then
    pass "$OSD_STORAGE_CLASS StorageClass available (for ODF OSD PVCs)"
else
    warn "$OSD_STORAGE_CLASS StorageClass not found - set OSD_STORAGE_CLASS to the correct SC for your platform"
fi

# Check if enough storage can be provisioned (3 x 50Gi = 150Gi for 3 replicas)
echo "  ODF will need 3 x 50Gi PVCs = 150Gi total storage for OSDs"

# --- OperatorHub / Marketplace ---
echo ""
echo "--- OperatorHub ---"
if oc get catalogsource redhat-operators -n openshift-marketplace &>/dev/null; then
    pass "redhat-operators CatalogSource available"
else
    fail "redhat-operators CatalogSource not found (needed to install ODF operator)"
fi

# Check if ODF package is available
if oc get packagemanifest odf-operator -n openshift-marketplace &>/dev/null; then
    ODF_CHANNEL=$(oc get packagemanifest odf-operator -n openshift-marketplace -o jsonpath='{.status.defaultChannel}')
    pass "odf-operator package available (default channel: $ODF_CHANNEL)"
else
    fail "odf-operator package not found in marketplace"
fi

# --- Existing ODF Installation ---
echo ""
echo "--- Existing Installation Check ---"
if oc get ns openshift-storage &>/dev/null; then
    warn "openshift-storage namespace already exists"
    if oc get csv -n openshift-storage 2>/dev/null | grep -q "odf-operator"; then
        warn "ODF operator already installed - step 2 may be skippable"
    fi
    if oc get storagecluster -n openshift-storage ocs-storagecluster &>/dev/null; then
        SC_PHASE=$(oc get storagecluster -n openshift-storage ocs-storagecluster -o jsonpath='{.status.phase}')
        warn "StorageCluster already exists (phase: $SC_PHASE) - step 3 may be skippable"
    fi

    echo ""
    echo "  An existing ODF installation was detected."
    echo "  You can uninstall it for a fresh start, or skip to reuse the existing install."
    UNINSTALL_ODF="n"
    if [ -t 0 ]; then
        read -rp "  Uninstall ODF for a fresh start? [y/N] " UNINSTALL_ODF
    fi

    if [ "$UNINSTALL_ODF" = "y" ] || [ "$UNINSTALL_ODF" = "Y" ]; then
        echo "  Running uninstall-odf.sh --force ..."
        if bash "$SCRIPT_DIR/uninstall-odf.sh" --force; then
            pass "ODF uninstalled successfully"
        else
            fail "ODF uninstall failed - check output above"
        fi
    else
        echo "  Keeping existing ODF installation."
    fi
else
    pass "openshift-storage namespace does not exist (clean state)"
fi

# --- Feature Gate / SnapshotMetadataService CRD ---
echo ""
echo "--- Feature Gate / SnapshotMetadataService CRD ---"
FEATURE_SET=$(oc get featuregate cluster -o jsonpath='{.spec.featureSet}' 2>/dev/null || echo "")
if [ "$FEATURE_SET" = "CustomNoUpgrade" ] || [ "$FEATURE_SET" = "DevPreviewNoUpgrade" ]; then
    pass "Feature set is '$FEATURE_SET'"
fi

if oc get crd snapshotmetadataservices.cbt.storage.k8s.io &>/dev/null; then
    pass "SnapshotMetadataService CRD exists"
else
    echo ""
    echo "  SnapshotMetadataService CRD is not installed."
    echo "  You can either:"
    echo "    a) Run step1 to enable the feature gate (triggers ~30 min node rollout)"
    echo "    b) Install the CRD directly (no node rollout, no feature gate needed)"
    echo ""
    # Interactive: prompt (default Y). Non-interactive (e.g. setup-all.sh): auto-install.
    INSTALL_CRD="y"
    if [ -t 0 ]; then
        read -rp "  Install CRD directly now? [Y/n] " INSTALL_CRD
        INSTALL_CRD="${INSTALL_CRD:-y}"
    else
        echo "  Non-interactive mode: installing CRD automatically."
    fi

    if [ "$INSTALL_CRD" = "y" ] || [ "$INSTALL_CRD" = "Y" ]; then
        echo "  Installing SnapshotMetadataService CRD..."
        if oc apply -f https://raw.githubusercontent.com/openshift/csi-external-snapshot-metadata/main/client/config/crd/cbt.storage.k8s.io_snapshotmetadataservices.yaml; then
            pass "SnapshotMetadataService CRD installed"
        else
            fail "Failed to install SnapshotMetadataService CRD"
        fi
    else
        warn "SnapshotMetadataService CRD not installed - run step1 or install manually"
    fi
fi

# --- Summary ---
echo ""
echo "============================="
echo "Results: $PASS passed, $FAIL failed, $WARN warnings"

if [ "${#FAIL_MSGS[@]}" -gt 0 ]; then
    echo ""
    echo "Failures:"
    for msg in "${FAIL_MSGS[@]}"; do
        echo "  - $msg"
    done
fi

if [ "${#WARN_MSGS[@]}" -gt 0 ]; then
    echo ""
    echo "Warnings:"
    for msg in "${WARN_MSGS[@]}"; do
        echo "  - $msg"
    done
fi

echo ""
if [ "$FAIL" -eq 0 ]; then
    echo "All preflight checks passed! Proceed with step 1."
else
    echo "Some checks failed. Address issues before proceeding."
    exit 1
fi
echo "============================="
