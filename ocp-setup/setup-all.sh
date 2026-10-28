#!/bin/bash
# Master setup script: runs all steps sequentially
# Usage: ./setup-all.sh [--skip-e2e] [--e2e-target TARGET]
#
# Each step includes its own wait logic for the next step's prerequisites.
# The full setup takes approximately 60-90 minutes before e2e tests start.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

SKIP_E2E=false
# Must be one of the targets accepted by step6-run-e2e.sh; "e2e-fast" skips the
# slow stored-diffs tests and still exercises the suite end to end.
export E2E_TARGET="${E2E_TARGET:-e2e-fast}"

for arg in "$@"; do
    case "$arg" in
        --skip-e2e) SKIP_E2E=true ;;
        --e2e-target=*) export E2E_TARGET="${arg#*=}" ;;
    esac
done

# Validate before the 60-90 minute setup, not after it.
if ! (cd "$PROJECT_DIR" && make -n "$E2E_TARGET" >/dev/null 2>&1); then
    echo "ERROR: E2E_TARGET '$E2E_TARGET' is not a make target in $PROJECT_DIR"
    exit 1
fi

log() {
    echo ""
    echo "================================================================"
    echo "  $1"
    echo "  $(date '+%Y-%m-%d %H:%M:%S')"
    echo "================================================================"
    echo ""
}

run_step() {
    local script="$1"
    local name="$2"
    log "Running: $name"
    if bash "$SCRIPT_DIR/$script"; then
        echo ""
        echo ">>> $name completed successfully."
    else
        echo ""
        echo ">>> ERROR: $name failed. Aborting."
        exit 1
    fi
}

START_TIME=$(date +%s)

# Step 0: Preflight
run_step step0-preflight.sh "Step 0: Preflight Checks"
# Preflight may show warnings (e.g. RAM < 24Gi on Standard clusters) - that's OK
# Only hard failures (connectivity, node count) will exit non-zero

# Step 1: Feature Gate
run_step step1-featuregate.sh "Step 1: Enable Feature Gate"

# Step 2: Install ODF Operator
run_step step2-install-odf.sh "Step 2: Install ODF Operator"

# Step 3: Create StorageCluster
run_step step3-create-storagecluster.sh "Step 3: Create StorageCluster"

# Step 4: Configure CBT Sidecar
run_step step4-cbt-sidecar.sh "Step 4: Configure CBT Sidecar"

# Step 5: Verify
run_step step5-verify.sh "Step 5: Verify Prerequisites"

END_SETUP=$(date +%s)
SETUP_DURATION=$(( (END_SETUP - START_TIME) / 60 ))
echo ""
echo "Setup completed in ${SETUP_DURATION} minutes."

# Step 6: Run E2E (optional)
if [ "$SKIP_E2E" = "true" ]; then
    echo ""
    echo "Skipping e2e tests (--skip-e2e). Run manually with:"
    echo "  cd $(dirname "$SCRIPT_DIR") && make $E2E_TARGET"
else
    run_step step6-run-e2e.sh "Step 6: Run E2E Tests ($E2E_TARGET)"
fi

END_TIME=$(date +%s)
TOTAL_DURATION=$(( (END_TIME - START_TIME) / 60 ))
echo ""
echo "Total time: ${TOTAL_DURATION} minutes."
