#!/bin/bash
# Run e2e tests from inside the cluster via a runner pod.
# Cross-compiles the test binary, copies it into a pod, and executes it.
#
# Usage:
#   ./run-in-cluster.sh                                          # Full compliance suite
#   ./run-in-cluster.sh -ginkgo.focus='Basic CBT'                # Run specific tests
#   ./run-in-cluster.sh -ginkgo.label-filter='!slow'             # Filter by label
#   ./run-in-cluster.sh --clean                                  # Remove runner pod + namespace
#
# All flags before -- are passed to the test binary (ginkgo flags).
# Custom test flags (storage-class, etc.) are appended automatically.
set -euo pipefail

RUNNER_NS="${RUNNER_NS:-cbt-e2e-runner}"
RUNNER_SA="cbt-e2e-runner"
RUNNER_POD="cbt-e2e-runner"
RUNNER_IMAGE="${RUNNER_IMAGE:-registry.access.redhat.com/ubi9/ubi:latest}"

STORAGE_CLASS="${STORAGE_CLASS:-ocs-storagecluster-ceph-rbd}"
SNAPSHOT_CLASS="${SNAPSHOT_CLASS:-ocs-storagecluster-rbdplugin-snapclass}"
CEPHCSI_NAMESPACE="${CEPHCSI_NAMESPACE:-openshift-storage}"
TEST_NAMESPACE="${TEST_NAMESPACE:-cbt-e2e-test}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_BIN="$SCRIPT_DIR/e2e.test"

# Handle --clean
if [ "${1:-}" = "--clean" ]; then
    echo "Cleaning up runner resources..."
    oc delete pod "$RUNNER_POD" -n "$RUNNER_NS" --ignore-not-found 2>/dev/null || true
    oc delete clusterrolebinding "$RUNNER_SA-admin" --ignore-not-found 2>/dev/null || true
    oc delete ns "$RUNNER_NS" --ignore-not-found 2>/dev/null || true
    echo "Done."
    exit 0
fi

# Detect cluster architecture
CLUSTER_ARCH=$(oc get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}' 2>/dev/null || echo "amd64")
echo "Cluster architecture: $CLUSTER_ARCH"

# Cross-compile test binary
echo "Cross-compiling test binary for linux/$CLUSTER_ARCH..."
CGO_ENABLED=0 GOOS=linux GOARCH="$CLUSTER_ARCH" go test -c -o "$TEST_BIN" ./tests/e2e/...
echo "Binary: $TEST_BIN ($(du -h "$TEST_BIN" | cut -f1))"

# Create runner namespace + SA + RBAC
echo ""
echo "Setting up runner infrastructure in namespace $RUNNER_NS..."
oc create ns "$RUNNER_NS" --dry-run=client -o yaml | oc apply -f - 2>/dev/null
oc create sa "$RUNNER_SA" -n "$RUNNER_NS" --dry-run=client -o yaml | oc apply -f - 2>/dev/null

# Cluster-admin for test resource management (create namespaces, pods, PVCs, snapshots, RBAC)
oc apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ${RUNNER_SA}-admin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: ${RUNNER_SA}
  namespace: ${RUNNER_NS}
EOF

# Create or recreate runner pod
EXISTING=$(oc get pod "$RUNNER_POD" -n "$RUNNER_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
if [ "$EXISTING" = "Running" ]; then
    echo "Runner pod already running."
else
    echo "Creating runner pod..."
    oc delete pod "$RUNNER_POD" -n "$RUNNER_NS" --ignore-not-found 2>/dev/null || true
    # Wait for old pod to be fully gone
    for i in $(seq 1 30); do
        oc get pod "$RUNNER_POD" -n "$RUNNER_NS" &>/dev/null || break
        sleep 2
    done
    oc apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${RUNNER_POD}
  namespace: ${RUNNER_NS}
spec:
  serviceAccountName: ${RUNNER_SA}
  containers:
  - name: runner
    image: ${RUNNER_IMAGE}
    command: ["sleep", "infinity"]
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
  restartPolicy: Never
EOF
    echo "Waiting for runner pod to be ready..."
    oc wait -n "$RUNNER_NS" pod/"$RUNNER_POD" --for=condition=Ready --timeout=120s
fi

# Copy test binary into the pod
echo ""
echo "Copying test binary to runner pod..."
oc cp "$TEST_BIN" "$RUNNER_NS/$RUNNER_POD:/tmp/e2e.test"
oc exec -n "$RUNNER_NS" "$RUNNER_POD" -- chmod +x /tmp/e2e.test

# Build test command
# Ginkgo v2 flags go before --, custom test flags go after --
GINKGO_FLAGS=("-test.v" "-ginkgo.v")

# Add any user-provided flags (before --)
while [ $# -gt 0 ]; do
    case "$1" in
        --)
            shift
            break
            ;;
        *)
            GINKGO_FLAGS+=("$1")
            shift
            ;;
    esac
done

TEST_FLAGS=(
    "--storage-class=$STORAGE_CLASS"
    "--snapshot-class=$SNAPSHOT_CLASS"
    "--cephcsi-namespace=$CEPHCSI_NAMESPACE"
    "--test-namespace=$TEST_NAMESPACE"
)
# Append any user-provided custom test flags
TEST_FLAGS+=("$@")

echo ""
echo "Running tests..."
echo "  Ginkgo flags: ${GINKGO_FLAGS[*]}"
echo "  Test flags: ${TEST_FLAGS[*]}"
echo ""

# Execute tests - use || true to capture exit code without failing the script
set +e
oc exec -n "$RUNNER_NS" "$RUNNER_POD" -- /tmp/e2e.test \
    "${GINKGO_FLAGS[@]}" \
    -- \
    "${TEST_FLAGS[@]}"
EXIT_CODE=$?
set -e

echo ""
if [ "$EXIT_CODE" -eq 0 ]; then
    echo "Tests passed!"
else
    echo "Tests failed (exit code: $EXIT_CODE)"
fi

exit "$EXIT_CODE"
