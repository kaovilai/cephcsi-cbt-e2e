# CephCSI CBT E2E Test Suite
#
# NOTE: The "make e2e-*" targets run tests LOCALLY and cannot reach the in-cluster
# gRPC service (csi-snapshot-metadata.openshift-storage.svc). They will fail with
# "name resolver error" or "connection refused".
#
# Use "cluster-*" targets or ./run-in-cluster.sh to run tests FROM INSIDE the cluster:
#   make cluster-e2e                            # Full suite in-cluster
#   ./run-in-cluster.sh -ginkgo.focus='Basic CBT'  # Specific tests in-cluster
#   make cluster-clean                          # Remove runner pod
#
# The local "make e2e-*" targets are only useful with port-forwarding or a local cluster.

# Configuration with defaults
STORAGE_CLASS ?= ocs-storagecluster-ceph-rbd
SNAPSHOT_CLASS ?= ocs-storagecluster-rbdplugin-snapclass
CEPHCSI_NAMESPACE ?= openshift-storage
TEST_NAMESPACE ?= cbt-e2e-test
GINKGO ?= go run github.com/onsi/ginkgo/v2/ginkgo

COMMON_FLAGS = \
	--storage-class=$(STORAGE_CLASS) \
	--snapshot-class=$(SNAPSHOT_CLASS) \
	--cephcsi-namespace=$(CEPHCSI_NAMESPACE) \
	--test-namespace=$(TEST_NAMESPACE)

.PHONY: build e2e e2e-fast e2e-rox e2e-rox-deletion e2e-flattening e2e-stored-diffs e2e-errors e2e-backup e2e-compliance e2e-resize lint lint-fix cluster-compliance cluster-e2e cluster-clean

build:
	go build ./...

# Full suite
e2e:
	$(GINKGO) -v --timeout=5h ./tests/e2e/... -- $(COMMON_FLAGS)

# Skip slow tests (priority flattening + stored diffs)
e2e-fast:
	$(GINKGO) -v --timeout=2h --label-filter='!stored-diffs' ./tests/e2e/... -- $(COMMON_FLAGS)

# Category B: ROX PVC
e2e-rox:
	$(GINKGO) -v --timeout=30m --focus='ROX PVC' ./tests/e2e/... -- $(COMMON_FLAGS)

# Category C: Counter-based Deletion
e2e-rox-deletion:
	$(GINKGO) -v --timeout=30m --focus='Counter-based Deletion' ./tests/e2e/... -- $(COMMON_FLAGS)

# Category D: Flattening Prevention
e2e-flattening:
	$(GINKGO) -v --timeout=30m --focus='Flattening Prevention' ./tests/e2e/... -- $(COMMON_FLAGS)

# Category E: Stored Diffs
e2e-stored-diffs:
	$(GINKGO) -v --timeout=1h --label-filter='stored-diffs' ./tests/e2e/... -- $(COMMON_FLAGS)

# Category G: Error Handling
e2e-errors:
	$(GINKGO) -v --timeout=30m --focus='Error Handling' ./tests/e2e/... -- $(COMMON_FLAGS)

# Category H: Backup Workflow
e2e-backup:
	$(GINKGO) -v --timeout=1h --focus='Backup Workflow' ./tests/e2e/... -- $(COMMON_FLAGS)

# Category I: Compliance (Velero, Block Metadata Properties, Error Compliance, Volume Resize)
e2e-compliance:
	$(GINKGO) -v --timeout=1h --focus='Velero Compliance|Block Metadata Properties|Error Compliance|Volume Resize' ./tests/e2e/... -- $(COMMON_FLAGS)

# Category J: Volume Resize
e2e-resize:
	$(GINKGO) -v --timeout=30m --focus='Volume Resize' ./tests/e2e/... -- $(COMMON_FLAGS)

# In-cluster execution (cross-compile + oc cp + oc exec)
cluster-compliance:
	./run-in-cluster.sh -ginkgo.focus='Velero Compliance|Block Metadata Properties|Error Compliance|Volume Resize' -ginkgo.timeout=1h

cluster-e2e:
	./run-in-cluster.sh -ginkgo.timeout=5h

cluster-clean:
	./run-in-cluster.sh --clean

GOLANGCI_LINT = go tool -modfile=golangci-lint.mod golangci-lint

lint:
	$(GOLANGCI_LINT) run ./...

lint-fix:
	$(GOLANGCI_LINT) run --fix ./...
