#!/bin/bash
# Step 6: Run CBT E2E tests
# Requires: All previous steps completed successfully
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

echo "=== Step 6: Run CBT E2E Tests ==="

cd "$PROJECT_DIR"

# Accept target from: $1, E2E_TARGET env var, or default to e2e-basic
TARGET="${1:-${E2E_TARGET:-e2e-basic}}"

# Validate target against known make targets
VALID_TARGETS="e2e e2e-fast e2e-basic e2e-rox e2e-rox-deletion e2e-flattening e2e-priority e2e-stored-diffs e2e-errors e2e-backup build lint"
if ! echo " $VALID_TARGETS " | grep -q " $TARGET "; then
    echo "ERROR: Unknown make target '$TARGET'"
    echo "Valid targets: $VALID_TARGETS"
    exit 1
fi

echo "Running: make $TARGET"
echo "Project dir: $PROJECT_DIR"
echo ""
echo "Override defaults with env vars:"
echo "  STORAGE_CLASS=${STORAGE_CLASS:-ocs-storagecluster-ceph-rbd}"
echo "  SNAPSHOT_CLASS=${SNAPSHOT_CLASS:-ocs-storagecluster-rbdplugin-snapclass}"
echo "  CEPHCSI_NAMESPACE=${CEPHCSI_NAMESPACE:-openshift-storage}"
echo ""
echo "Command: cd $PROJECT_DIR && make $TARGET"
echo ""

make "$TARGET"
