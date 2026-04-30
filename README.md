# CephCSI CBT E2E Test Suite

End-to-end tests for validating Ceph RBD **Changed Block Tracking (CBT)** via the Kubernetes CSI SnapshotMetadata API.

This suite verifies that incremental block-level backups can accurately track changed blocks between snapshots, supporting efficient backup workflows (e.g., [Velero Block Data Mover](https://github.com/vmware-tanzu/velero/pull/9528)) that only transfer changed data.

For a getting-started overview of Kubernetes CBT (using csi-driver-host-path), see [k8s-cbt-s3mover-demo](https://github.com/kaovilai/k8s-cbt-s3mover-demo).

## Prerequisites

| Component | Minimum Version |
|-----------|----------------|
| Kubernetes | v1.33 (alpha), v1.36 (beta target per KEP-3314) |
| CSI spec | v1.10.0 (SnapshotMetadata service) |
| external-snapshot-metadata | v1.0.0 (SnapshotMetadataService CRD v1beta1) |
| external-snapshotter | v8.0.0 |
| Ceph | v17.0 (Quincy) |
| Go | 1.24+ |

Additionally:

- **VolumeSnapshot CRD** must be installed (external-snapshotter)
- **SnapshotMetadataService CRD** must be installed (`cbt.storage.k8s.io/v1beta1`)
- **external-snapshot-metadata sidecar** must be deployed in the CephCSI provisioner pod
- CephCSI RBD provisioner pods must be running (tested with ODF 4.18+)

## Project Structure

```
cephcsi-cbt-e2e/
├── cmd/
│   ├── cbt-check/              # CLI to call GetMetadataAllocated on an existing VolumeSnapshot
│   └── go-ceph-repro/          # Standalone go-ceph DiffIterateByID reproducer (needs CGo)
├── config/                     # StorageClass and VolumeSnapshotClass YAML manifests
├── demo/                       # Slidev presentation (GitHub Pages deployment)
├── ocp-setup/                  # OpenShift cluster setup scripts for ODF + CBT sidecar
├── pkg/
│   ├── cbt/                    # gRPC client wrapping external-snapshot-metadata iterator API
│   ├── data/                   # Block device data operations: writes, hashes, verification
│   ├── k8s/                    # Kubernetes resource lifecycle helpers (PVC, Pod, Snapshot, Namespace)
│   └── rbd/                    # Ceph RBD introspection via toolbox pod exec
├── tests/e2e/                  # Ginkgo v2 BDD test suite (Ordered test containers)
├── debug-cbt.sh                # Quick CBT debugging script
├── deploy-sidecar.sh           # Deploy CBT sidecar alongside CephCSI RBD controller
├── run-in-cluster.sh           # Run tests from inside the cluster (cross-compile + oc cp + oc exec)
├── Makefile
├── velero-feedback.md          # Design accommodations for Velero PR #9528
├── velero-coverage.md          # Velero CBT coverage tracking
└── k8s-coverage.md             # Kubernetes upstream coverage tracking
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

> **Note:** The local `make e2e-*` targets cannot reach the in-cluster gRPC service
> (`csi-snapshot-metadata.openshift-storage.svc`). Use `cluster-*` targets or
> `./run-in-cluster.sh` to run tests from inside the cluster.

```bash
# Build
make build

# Lint
make lint
make lint-fix

# Full suite (all categories, 5h timeout)
make e2e

# Fast suite (skips stored-diffs tests, 2h timeout)
make e2e-fast
```

#### Individual Test Categories

```bash
make e2e-rox            # ROX PVC (30m)
make e2e-rox-deletion   # Counter-based Deletion (30m)
make e2e-flattening     # Flattening Prevention (30m)
make e2e-stored-diffs   # Stored Diffs fallback (1h, slow)
make e2e-errors         # Error Handling (30m)
make e2e-backup         # Backup Workflow (1h)
make e2e-compliance     # Velero Compliance, Block Metadata Properties, Error Compliance, Volume Resize (1h)
make e2e-resize         # Volume Resize (30m)
```

#### In-Cluster Execution

```bash
# Run full suite inside the cluster
make cluster-e2e

# Run compliance tests inside the cluster
make cluster-compliance

# Run specific tests
./run-in-cluster.sh -ginkgo.focus='Basic CBT'

# Clean up runner pod
make cluster-clean
```

#### Custom Configuration

```bash
make e2e-backup \
  STORAGE_CLASS=my-rbd-sc \
  SNAPSHOT_CLASS=my-snap-sc \
  CEPHCSI_NAMESPACE=ceph-csi
```

#### Running a Single Test

```bash
GINKGO="go run github.com/onsi/ginkgo/v2/ginkgo" \
  $GINKGO -v --focus='should return allocated blocks' ./tests/e2e/... -- \
  --storage-class=ocs-storagecluster-ceph-rbd \
  --snapshot-class=ocs-storagecluster-rbdplugin-snapclass
```

## Test Categories

| Category | Description |
|----------|-------------|
| **ROX PVC** | ReadOnlyMany PVC binding from snapshots, verifying no RBD flattening occurs |
| **Counter-based Deletion** | Counter-based RBD snapshot deletion behavior |
| **Flattening Prevention** | Snapshot chain preservation, conditions that trigger flattening |
| **Stored Diffs** | Omap-stored diffs as fallback when snapshots are flattened (slow) |
| **Error Handling** | Graceful handling of flattened snapshots, nonexistent snapshots |
| **Backup Workflow** | Simulated end-to-end backup workflow with snapshot retention |
| **Velero Compliance** | Validates CBT behavior matches Velero integration requirements |
| **Block Metadata Properties** | Validates metadata block size, alignment, and structural properties |
| **Volume Resize** | CBT behavior across volume resize operations |
| **Volume Mode Rebind** | Filesystem data helpers and volume mode rebind scenarios |

## OpenShift Cluster Setup

See [`ocp-setup/README.md`](ocp-setup/README.md) for the full ODF + CBT sidecar setup walkthrough. The scripts must be run with **bash** (not zsh) from the `ocp-setup/` directory:

```bash
cd ocp-setup
bash step0-preflight.sh    # Validate cluster prerequisites
bash step2-install-odf.sh  # Install ODF operator
bash step3-create-storagecluster.sh
bash step4-cbt-sidecar.sh  # Configure CBT sidecar
bash step5-verify.sh       # Verify setup
bash step6-run-e2e.sh      # Run e2e tests
```

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

When a snapshot's intermediate RBD image is flattened (clone chain broken), `rbd snap diff` across images fails and `GetMetadataDelta` cannot compute deltas. There is a [design proposal](CLAUDE.md#key-domain-concepts) ("Combined solution") to store diffs in Ceph omap before flattening as a fallback, but this is **not yet implemented** in CephCSI.

## Velero CBT Integration Status (as of 2026-04-30)

Velero's block data mover is being built incrementally. Current state of the PR chain:

| Step | PR/Issue | Status | Description |
|------|----------|--------|-------------|
| Design doc | [PR #9528](https://github.com/velero-io/velero/pull/9528) | **Merged** | Block data mover design |
| CBT interfaces | [PR #9716](https://github.com/velero-io/velero/pull/9716) | **Merged** | `cbtservice.Service` and bitmap interfaces |
| Unified repo extension | [PR #9724](https://github.com/velero-io/velero/pull/9724) | **Merged** | Block uploader support in unified repository |
| CBT bitmap impl | [PR #9736](https://github.com/velero-io/velero/pull/9736) | **Open** (approved by kaovilai) | RoaringBitmap-based block tracking |
| gRPC client | [Issue #9710](https://github.com/velero-io/velero/issues/9710) | **No PR yet** | `cbtservice.Service` impl talking to external-snapshot-metadata sidecar |
| Service-to-bitmap | [Issue #9715](https://github.com/velero-io/velero/issues/9715) | **No PR yet** | Glue between gRPC client and bitmap |
| Block data mover | [Issue #9556](https://github.com/velero-io/velero/issues/9556) | **No PR yet** | End-to-end incremental backup using bitmap |

**What this test suite validates today:** The CSI-level CBT layer (gRPC calls directly to the external-snapshot-metadata sidecar via CephCSI). This covers the same protocol that Velero's gRPC client (issue #9710) will use.

**Next contribution opportunity:** Implement the `cbtservice.Service` gRPC client (issue #9710). Our `pkg/cbt/` already wraps the same external-snapshot-metadata iterator API — the Velero implementation would be structurally similar.

### Upstream Dependency Versions

| Dependency | This repo | Latest upstream |
|---|---|---|
| external-snapshot-metadata | v1.0.0 | v1.0.0 |
| external-snapshot-metadata/client | v1.0.0 | v1.0.0 |
| external-snapshotter client/v8 | v8.4.0 | v8.4.0 |
| k8s.io/{api,apimachinery,client-go} | v0.35.4 | v0.35.4 (v0.36.0 available) |
| CephCSI CBT | [PR #5347](https://github.com/ceph/ceph-csi/pull/5347) merged (devel) | Latest release: v3.16.2 |

## References

- [KEP-3314: CSI Changed Block Tracking](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md)
- [CephCSI CBT PR #5347](https://github.com/ceph/ceph-csi/pull/5347)
- [external-snapshot-metadata deployment](https://github.com/kubernetes-csi/external-snapshot-metadata/blob/main/deploy/README.md)
- [SnapshotMetadataService API types](https://github.com/kubernetes-csi/external-snapshot-metadata/blob/main/client/apis/snapshotmetadataservice/v1beta1/types.go)
- [CBT sidecar setup for ODF](https://access.redhat.com/articles/7130698)
- [Velero CBT Integration Plan](https://hackmd.io/@velero/r1U1EVKdgl)
