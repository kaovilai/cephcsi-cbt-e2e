# CephCSI CBT E2E Test Suite

End-to-end tests for validating Ceph RBD **Changed Block Tracking (CBT)** via the Kubernetes CSI SnapshotMetadata API.

This suite verifies that incremental block-level backups can accurately track changed blocks between snapshots, supporting efficient backup workflows (e.g., [Velero Block Data Mover](https://github.com/vmware-tanzu/velero/pull/9528)) that only transfer changed data.

For a getting-started overview of Kubernetes CBT (using csi-driver-host-path), see [k8s-cbt-s3mover-demo](https://github.com/kaovilai/k8s-cbt-s3mover-demo).

## Prerequisites

| Component | Minimum Version |
|-----------|----------------|
| Kubernetes | v1.33 (alpha), v1.36 (beta target per KEP-3314) |
| CSI spec | v1.10.0 (SnapshotMetadata service) |
| external-snapshot-metadata | v0.2.0 |
| external-snapshotter | v8.0.0 |
| Ceph | v17.0 (Quincy) |
| Go | 1.24+ |

Additionally:

- **VolumeSnapshot CRD** must be installed (external-snapshotter)
- **SnapshotMetadataService CRD** must be installed (`cbt.storage.k8s.io/v1alpha1`)
- **external-snapshot-metadata sidecar** must be deployed in the CephCSI provisioner pod
- CephCSI RBD provisioner pods must be running (tested with ODF 4.18+)

## Project Structure

```
cephcsi-cbt-e2e/
├── config/
│   ├── storageclass-rbd.yaml          # RBD StorageClass
│   └── snapshotclass-rbd.yaml         # VolumeSnapshotClass
├── pkg/
│   ├── cbt/cbt.go                     # CBT client (GetMetadataAllocated/Delta)
│   ├── k8s/k8s.go                     # Kubernetes resource helpers (PVC, Snapshot, Pod)
│   ├── rbd/rbd.go                     # RBD inspection (image state, omap, clone depth)
│   └── data/data.go                   # Block-level data writing and verification
├── tests/e2e/
│   ├── e2e_suite_test.go              # Suite setup and precondition checks
│   ├── basic_cbt_test.go              # Category A: Basic CBT operations
│   ├── rox_pvc_test.go                # Category B: ReadOnlyMany PVC from snapshots
│   ├── counter_deletion_test.go       # Category C: Counter-based RBD snapshot deletion
│   ├── flattening_prevention_test.go  # Category D: Flattening prevention
│   ├── priority_flattening_test.go    # Category E: Priority flattening (slow)
│   ├── stored_diffs_test.go           # Category F: Stored diffs fallback (slow)
│   ├── error_handling_test.go         # Category G: Error handling
│   └── backup_workflow_test.go        # Category H: Backup workflow simulation
├── velero-feedback.md                 # Design accommodations for Velero PR #9528
├── Makefile
├── go.mod
└── go.sum
```

## Usage

### Configuration

All make targets accept the following overrides:

| Variable | Default | Description |
|----------|---------|-------------|
| `STORAGE_CLASS` | `ocs-storagecluster-ceph-rbd` | RBD StorageClass name |
| `SNAPSHOT_CLASS` | `ocs-storagecluster-rbdplugin-snapclass` | VolumeSnapshotClass name |
| `CEPHCSI_NAMESPACE` | `openshift-storage` | Namespace where CephCSI is deployed |
| `TEST_NAMESPACE` | `cbt-e2e-test` | Namespace for test resources |

### Running Tests

```bash
# Build
make build

# Full suite (all categories)
make e2e

# Fast suite (skips slow tests)
make e2e-fast

# Individual categories
make e2e-basic          # Category A: Basic CBT
make e2e-rox            # Category B: ROX PVC
make e2e-rox-deletion   # Category C: Counter-based Deletion
make e2e-flattening     # Category D: Flattening Prevention
make e2e-priority       # Category E: Priority Flattening (slow)
make e2e-stored-diffs   # Category F: Stored Diffs (slow)
make e2e-errors         # Category G: Error Handling
make e2e-backup         # Category H: Backup Workflow

# Lint
make lint
```

With custom configuration:

```bash
make e2e-basic \
  STORAGE_CLASS=my-rbd-sc \
  SNAPSHOT_CLASS=my-snap-sc \
  CEPHCSI_NAMESPACE=ceph-csi
```

With an explicit kubeconfig:

```bash
GINKGO="go run github.com/onsi/ginkgo/v2/ginkgo" \
  $GINKGO -v ./tests/e2e/... -- \
  --kubeconfig=/path/to/kubeconfig \
  --storage-class=ocs-storagecluster-ceph-rbd \
  --snapshot-class=ocs-storagecluster-rbdplugin-snapclass
```

## Test Categories

| Category | Description |
|----------|-------------|
| **A - Basic CBT** | `GetMetadataAllocated` and `GetMetadataDelta` correctness, block-level accuracy verification |
| **B - ROX PVC** | ReadOnlyMany PVC binding from snapshots, verifying no RBD flattening occurs |
| **C - Counter-based Deletion** | Counter-based RBD snapshot deletion behavior |
| **D - Flattening Prevention** | Snapshot chain preservation, conditions that trigger flattening |
| **E - Priority Flattening** | Priority-based flattening eviction at the 250-snapshot limit (slow) |
| **F - Stored Diffs** | Omap-stored diffs as fallback when snapshots are flattened (slow) |
| **G - Error Handling** | Graceful handling of flattened snapshots, nonexistent snapshots |
| **H - Backup Workflow** | Simulated end-to-end backup workflow with snapshot retention |

## Key Concepts

### Changed Block Tracking Flow

1. Create a base snapshot of the volume
2. Write data to the volume
3. Create a target snapshot
4. Call `GetMetadataDelta(baseHandle, targetName)` to get changed blocks
5. Transfer only changed blocks to backup storage
6. Retain the base snapshot for future deltas

### Why Snapshot Retention Matters

Ceph RBD computes deltas using `rbd snap diff`, which requires both snapshots to exist in the same clone chain. Deleting the base snapshot after upload breaks the incremental chain, causing all subsequent backups to fall back to full.

### ROX PVC Pattern

ReadOnlyMany PVCs created from snapshots do not trigger RBD flattening, preserving the snapshot chain for CBT. This is important for backup workflows that expose snapshot data for reading.

### Flattening Impact on CBT

When a snapshot's intermediate RBD image is flattened (clone chain broken), `rbd snap diff` across images fails and `GetMetadataDelta` cannot compute deltas. There is a [design proposal](CLAUDE.md#key-domain-concepts) ("Combined solution") to store diffs in Ceph omap before flattening as a fallback, but this is **not yet implemented** in CephCSI. The `stored_diffs_test.go` validates this behavior by force-flattening intermediate images via `rbd flatten` and confirming that delta computation fails without stored diffs.
