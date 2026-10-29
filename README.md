# CephCSI CBT E2E Test Suite

End-to-end tests for validating Ceph RBD **Changed Block Tracking (CBT)** via the Kubernetes CSI SnapshotMetadata API.

This suite verifies that incremental block-level backups can accurately track changed blocks between snapshots, supporting efficient backup workflows (e.g., [Velero Block Data Mover](https://github.com/velero-io/velero/pull/9528)) that only transfer changed data.

For a getting-started overview of Kubernetes CBT (using csi-driver-host-path), see [k8s-cbt-s3mover-demo](https://github.com/kaovilai/k8s-cbt-s3mover-demo).

## Prerequisites

| Component | Requirement |
|-----------|-------------|
| Kubernetes | v1.33+ (CBT alpha in v1.33, beta since v1.36; not GA as of Oct 2026) |
| CSI spec | v1.10.0 (SnapshotMetadata service) |
| external-snapshot-metadata | v1.0.0+ (SnapshotMetadataService CRD `v1beta1`); this repo builds against v1.1.0 |
| external-snapshotter | v8.0.0+ (this repo builds against client/v8 v8.7.0) |
| Ceph | Pacific v16.2.0+ per the ceph-csi README (no CBT-specific minimum is published); validated on Ceph 20.1.0 (Tentacle) |
| Go | 1.26.1+ (matches `go.mod`) |

CBT is an out-of-tree Kubernetes API: [KEP-3314](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md) has no feature gate. On OpenShift, CBT is Developer Preview on OCP/ODF 4.20-4.22 and Technology Preview on OCP 5.0 (the [ODF setup article](https://access.redhat.com/articles/7130698), Red Hat login required, is written for ODF 4.20; the same status for 4.21/4.22 is assumed, not confirmed in its public text), and requires the `ExternalSnapshotMetadata` cluster feature gate.

Additionally:

- **VolumeSnapshot CRD** must be installed (external-snapshotter)
- **SnapshotMetadataService CRD** must be installed (`cbt.storage.k8s.io/v1beta1`)
- **external-snapshot-metadata sidecar** must be deployed in the CephCSI provisioner pod
- CephCSI RBD provisioner pods must be running

**Validated environment:** ODF 4.21 on OCP 4.21.7 (Kubernetes v1.34.5, Ceph 20.1.0) with the upstream sidecar image `registry.k8s.io/sig-storage/csi-snapshot-metadata:v0.2.0`; see [`results.md`](results.md). ODF 4.22, ODF 5.0 and CephCSI v3.18.x have not been run with this suite (ODF 4.22 ships the same v0.2.0 sidecar; ODF 5.0 ships v1.0.0). The ROX and flattening tests in particular may behave differently on CephCSI v3.18.0+, where ReadOnlyMany snapshot restores no longer flatten (see [Flattening Impact on CBT](#flattening-impact-on-cbt)).

## Project Structure

```
cephcsi-cbt-e2e/
├── cmd/
│   ├── cbt-check/              # CLI to call GetMetadataAllocated on an existing VolumeSnapshot
│   └── go-ceph-repro/          # Standalone go-ceph DiffIterateByID reproducer (needs CGo)
├── config/                     # StorageClass and VolumeSnapshotClass YAML manifests
├── demo/                       # Slidev presentation (GitHub Pages deployment); see demo/README.md
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

# Unit tests, coverage report, and race detector
make test
make test-coverage
make test-race

# Lint
make lint
make lint-fix
make shellcheck

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
make e2e-stored-diffs   # Stored Diffs gap/flatten behavior (1h, slow)
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
| **Stored Diffs** | Post-flatten behavior validation (no stored diffs fallback in CephCSI today) (slow) |
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

ReadOnlyMany PVCs created from snapshots are intended to avoid RBD flattening, preserving the snapshot chain for CBT. This is important for backup workflows that expose snapshot data for reading. CephCSI [PR #6534](https://github.com/ceph/ceph-csi/pull/6534) (v3.18.0) implements this for RBD by skipping the flatten step when all requested volume capabilities are read-only. Older CephCSI releases (and ODF builds that ship them) still flatten on restore. ReadWrite restores always create a real clone.

### Flattening Impact on CBT

When a snapshot's intermediate RBD image is flattened (clone chain broken), `rbd snap diff` across images fails and `GetMetadataDelta` cannot compute deltas. There is a [design proposal](CLAUDE.md#key-domain-concepts) ("Combined solution") to store diffs in Ceph omap before flattening as a fallback, but as of Oct 2026 no PR, issue, or design document for it was found in CephCSI.

## Velero CBT Integration Status (as of 2026-10-27)

Velero's block data mover (the Velero component that copies volume data block by block, which is what makes incremental backups possible) is merged into Velero's `main` branch but not released yet. The latest release is v1.18.4, and there is no v1.19 tag yet.

**Bottom line for Ceph users when Velero 1.19.0 ships:** backups will still succeed and the data will be correct, but they will no longer be incremental. Once the earlier backup's snapshot (the base snapshot) is deleted, Velero falls back to copying allocated blocks (every block in use, which is more than the changed blocks but can be less than a full copy), and to a full backup if that also fails. A fix is being designed for a 1.19.x patch.

**What changed at the 2026-10-27 community meeting:**

- **Velero 1.19.0 does not support Case 2.** Case 2 means storage, such as Ceph RBD, that can list changed blocks only while the earlier base snapshot still exists.
- **One half of [#10307](https://github.com/velero-io/velero/pull/10307) is rolled back.** That half copied the `deletionPolicy` setting (`Delete` means the snapshot is removed after the backup) from the user's VolumeSnapshotClass onto the snapshot record Velero creates for the backup (the backup `VolumeSnapshotContent`, or VSC). That record is now always `Delete`, as in 1.18.
- **The other half stays.** Velero still sends the correct snapshot handle (the snapshot's ID, also called the changeID) to the driver (Ceph's storage plug-in, CephCSI) ([#10294](https://github.com/velero-io/velero/issues/10294)).
- **What happens to a Ceph backup.** If the base snapshot is already gone, the driver call that lists changed blocks (`GetMetadataDelta`) fails. Velero then tries allocated blocks and, if that also fails, a full backup. Velero labels the backup `fallbackFull` whenever this fallback path runs, whether it ended up copying allocated blocks or a full copy; the data is correct, but the incremental saving is lost.
- **The file system data mover never keeps snapshots** after a backup.

**The 1.19.x design (still a proposal):** Case 2 is a separate design aimed at a 1.19.x patch: tracking issue [#10743](https://github.com/velero-io/velero/issues/10743), design [#10744](https://github.com/velero-io/velero/issues/10744), and a draft design document ([base-snapshot-retention.md](https://github.com/kaovilai/velero/blob/design-base-snapshot-retention/design/block-data-mover/base-snapshot-retention.md)).

- A per-volume Velero setting (VolumePolicy parameter, working name `retainBaseSnapshot`) would win over a flag the driver can publish (`requireBaseSnapshotForDelta` on the SnapshotMetadataService), which in turn would win over the default of not retaining.
- That flag says what the driver needs: `true` means keep the old snapshot for deltas (Ceph RBD), `false` means it does not need it, and unset means the driver gives no guidance. When it is `false`, KEP-3314 requires the backup tool to save the CSI snapshot handle before deleting the snapshot, so a later delta can still be computed from the handle alone. The field is merged in [external-snapshot-metadata#258](https://github.com/kubernetes-csi/external-snapshot-metadata/pull/258) but not in a release yet, and [the KEP text that documents it](https://github.com/kubernetes/enhancements/pull/6479) is still an open pull request.
- Velero would manage kept snapshots itself instead of reading the VolumeSnapshotClass: keep only the latest snapshot per volume chain (the backups of one PVC to one storage location), delete the previous one once the next backup succeeds, and delete it when the backup is deleted.
- KEP-3314, the Kubernetes CBT design document, leaves the decision of whether a driver needs the base snapshot to the backup tool, which here is Velero.
- Ceph CBT is deferred, not dropped: it stays in the 1.19 stream (1.19.x) and is targeted for OADP 1.7 (Red Hat's OpenShift backup product), which is a target and not a commitment.

Progress table. Completed = merged or closed as done; Partly kept = the changeID half shipped and the class-inherit half withdrawn; Open = being designed; Draft = unfinished PR.

| Step | PR/Issue | Status | Description |
|------|----------|--------|-------------|
| Design doc | [PR #9528](https://github.com/velero-io/velero/pull/9528) | **Completed** (merged) | Block data mover design |
| CBT interfaces | [PR #9716](https://github.com/velero-io/velero/pull/9716) | **Completed** (merged) | `cbtservice.Service` and bitmap interfaces |
| Unified repo extension | [PR #9724](https://github.com/velero-io/velero/pull/9724) | **Completed** (merged) | Block uploader support in unified repository |
| CBT bitmap impl | [PR #9736](https://github.com/velero-io/velero/pull/9736) | **Completed** (merged) | RoaringBitmap-based block tracking |
| CBT service (gRPC client) | [PR #9846](https://github.com/velero-io/velero/pull/9846), [Issue #9710](https://github.com/velero-io/velero/issues/9710) | **Completed** (merged) | `cbtservice.Service` impl talking to the external-snapshot-metadata sidecar |
| Service-to-bitmap | [Issue #9715](https://github.com/velero-io/velero/issues/9715) | **Completed** (closed) | Glue between gRPC client and bitmap |
| Block uploader and workflow | [PR #9979](https://github.com/velero-io/velero/pull/9979), [PR #10067](https://github.com/velero-io/velero/pull/10067), [PR #10071](https://github.com/velero-io/velero/pull/10071) | **Completed** (merged) | Block uploader backup/restore and backup workflow |
| Block data mover umbrella | [Issue #9556](https://github.com/velero-io/velero/issues/9556) | **Completed** (closed) | End-to-end block-level backup/restore |
| Ceph changeID fix | [PR #10307](https://github.com/velero-io/velero/pull/10307) (fixes [#10294](https://github.com/velero-io/velero/issues/10294)) | **Partly kept** | ChangeID retrieval fixed (was empty on non-vSphere drivers, so every "incremental" ran full). The `deletionPolicy` inheritance from the VolumeSnapshotClass is withdrawn for 1.19.0 |
| Case 2 retention tracking | [Issue #10743](https://github.com/velero-io/velero/issues/10743), [design #10744](https://github.com/velero-io/velero/issues/10744) | **Open** | Design work for retention on storage that needs the base snapshot (Ceph RBD); target 1.19.x; see above |
| Retention deletion (candidate) | [PR #10691](https://github.com/velero-io/velero/pull/10691) | **Draft** | Records retained snapshots and deletes them with the backup; to be reworked against #10744 |
| Docs | [PR #10537](https://github.com/velero-io/velero/pull/10537) | **Completed** (merged) | Block data mover section in `csi-snapshot-data-movement.md` |

Enablement: no alpha flag; the CSI feature (`EnableCSI`) is required, and the data mover is selected per backup (`velero backup create ... --snapshot-move-data --data-mover velero-block [--backup-type Incremental|Full]`) or per volume via VolumePolicy snapshot action parameters (`dataMover: velero-block|velero-fs`).

Snapshot retention for Ceph with 1.19.0:

- `deletionPolicy: Retain` (keep the snapshot after the backup) is not a workaround. That path is being withdrawn.
- Keeping snapshots alone does not help, because nothing tracks or cleans them up for CBT; that is what the Case 2 design adds.
- Until #10743/#10744 land in a 1.19.x patch, expect Ceph block data mover backups to fall back to allocated blocks or full when the base snapshot is gone.
- Keeping snapshots also has a cost: the earlier path left snapshots behind ([#9835](https://github.com/velero-io/velero/issues/9835), [#10304](https://github.com/velero-io/velero/issues/10304)), and `maxSnapshotsOnImage` (Ceph's limit on snapshots per image) can make Ceph flatten (merge) images, which breaks CBT.

**What this test suite validates today:** The CSI-level CBT layer (gRPC calls directly to the external-snapshot-metadata sidecar via CephCSI). This is the same protocol that Velero's CBT service uses. The end-to-end Velero results in this repo's tracking docs were obtained with a retained base snapshot on pre-release builds, before the rollback.

**Candidate coverage:** The in-place incremental restore design ([PR #10014](https://github.com/velero-io/velero/pull/10014)) and retained-snapshot behavior under `maxSnapshotsOnImage` pressure are not covered here yet.

### Upstream Dependency Versions

The status above is from the 2026-10-27 meeting; these versions were checked on 2026-10-28. This repo's versions come from `go.mod` and `golangci-lint.mod`.

| Go module | This repo | Latest upstream |
|---|---|---|
| external-snapshot-metadata | v1.1.0 | v1.1.0 |
| external-snapshot-metadata/client | v1.1.0 | v1.1.0 |
| external-snapshotter client/v8 | v8.7.0 | v8.7.0 |
| k8s.io/{api,apimachinery,client-go} | v0.37.2 | v0.37.2 (Kubernetes v1.37) |
| ginkgo / gomega | v2.33.1 / v1.44.0 | v2.33.1 / v1.44.0 |
| golangci-lint | v2.14.0 | v2.14.0 |

CephCSI (not a Go module of this repo):

- CBT: [PR #5347](https://github.com/ceph/ceph-csi/pull/5347) merged July 2025; latest release [v3.18.1](https://github.com/ceph/ceph-csi/releases/tag/v3.18.1) (2026-10-01).
- ReadOnlyMany (ROX) restore without flatten: [PR #6534](https://github.com/ceph/ceph-csi/pull/6534) (v3.18.0) restores a snapshot read-only and keeps the parent chain intact, which CBT needs.

The sidecar image (the helper container that answers block-list requests) is a separate choice from the Go module version. ODF (OpenShift Data Foundation, Red Hat's Ceph storage product) 4.22 ships snapshot-metadata v0.2.0 and ODF 5.0 ships v1.0.0. For the configuration this repo has validated, use v0.2.0; v1.0.0 is untested here (see [`results.md`](results.md)).

## References

- [KEP-3314: CSI Changed Block Tracking](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md)
- [CephCSI CBT PR #5347](https://github.com/ceph/ceph-csi/pull/5347)
- [external-snapshot-metadata deployment](https://github.com/kubernetes-csi/external-snapshot-metadata/blob/main/deploy/README.md)
- [SnapshotMetadataService API types](https://github.com/kubernetes-csi/external-snapshot-metadata/blob/main/client/apis/snapshotmetadataservice/v1beta1/types.go)
- [CBT sidecar setup for ODF](https://access.redhat.com/articles/7130698)
- [Velero CBT Integration Plan](https://hackmd.io/@velero/r1U1EVKdgl)
- [Contributing](CONTRIBUTING.md) and [Security Policy](SECURITY.md)
