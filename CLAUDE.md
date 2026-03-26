# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

E2E test suite for **Ceph RBD Changed Block Tracking (CBT)** via the Kubernetes CSI external-snapshot-metadata API. Tests validate incremental backup capabilities (GetMetadataAllocated, GetMetadataDelta) needed by Velero and similar backup tools.

Requires a live Kubernetes 1.33+ cluster with CephCSI, ODF/Rook, and the external-snapshot-metadata sidecar deployed.

## Build and Test Commands

```bash
make build              # Compile all packages
make lint               # golangci-lint run ./...
make lint-fix           # golangci-lint run --fix ./...
make e2e                # Full suite (~17m observed, 5h timeout)
make e2e-fast           # Skip stored-diffs tests (2h timeout)
make e2e-rox            # ReadOnlyMany PVC tests (30m)
make e2e-rox-deletion   # Counter-based deletion tests (30m)
make e2e-flattening     # Flattening prevention tests (30m)
make e2e-stored-diffs   # Stored diffs fallback (1h)
make e2e-errors         # Error handling tests (30m)
make e2e-backup         # Backup workflow tests (1h)
make e2e-compliance     # Velero/block metadata/error compliance + volume resize (1h)
make e2e-resize         # Volume resize tests (30m)
```

In-cluster execution (tests must run inside the cluster to reach the gRPC service):
```bash
make cluster-e2e            # Full suite in-cluster
make cluster-compliance     # Compliance tests in-cluster
./run-in-cluster.sh -ginkgo.focus='Basic CBT'  # Specific tests
make cluster-clean          # Remove runner pod
```

Override cluster defaults via environment variables:
```bash
STORAGE_CLASS=my-sc SNAPSHOT_CLASS=my-snapclass CEPHCSI_NAMESPACE=rook-ceph make e2e-backup
```

Run a single test by description:
```bash
go run github.com/onsi/ginkgo/v2/ginkgo -v --focus='should return allocated blocks' ./tests/e2e/... -- --storage-class=ocs-storagecluster-ceph-rbd --snapshot-class=ocs-storagecluster-rbdplugin-snapclass
```

## Architecture

```
cmd/
├── cbt-check/      # CLI to call GetMetadataAllocated on an existing VolumeSnapshot
└── go-ceph-repro/  # Standalone go-ceph DiffIterateByID reproducer (needs CGo)
pkg/
├── cbt/    # gRPC client wrapping external-snapshot-metadata iterator API
├── data/   # Block device data operations: writes, hashes, verification
├── k8s/    # Kubernetes resource lifecycle helpers (PVC, Pod, Snapshot, Namespace)
└── rbd/    # Ceph RBD introspection via toolbox pod exec
tests/e2e/  # Ginkgo v2 BDD test suite (Ordered test containers)
config/     # StorageClass and VolumeSnapshotClass YAML manifests
demo/       # Slidev presentation (GitHub Pages deployment)
ocp-setup/  # OpenShift cluster setup scripts for ODF + CBT sidecar
```

## Test Patterns

- Tests use **Ginkgo v2** with `Ordered` containers: `BeforeAll` sets up resources, `It` blocks run assertions, `AfterAll` cleans up in reverse order.
- Gomega matchers are dot-imported (`. "github.com/onsi/gomega"`).
- Shared state (clients, config flags) lives in `e2e_suite_test.go` as package-level vars initialized in `init()` and `TestCephCSICBT`.
- `BeforeSuite` validates cluster preconditions (K8s version, CRDs, CephCSI pods, sidecar, Ceph version).
- Ginkgo labels `slow` and `stored-diffs` categorize long-running tests for filtering.

## Key Domain Concepts

- **CBT (Changed Block Tracking)**: Reports which blocks were allocated or changed between snapshots at the block-device level via `rbd snap diff`.
- **GetMetadataAllocated**: All allocated blocks in a single snapshot.
- **GetMetadataDelta**: Blocks changed between two snapshots (requires both snapshots in the RBD clone chain).
- **Flattening**: CephCSI flattens intermediate clone images (collapses clone chain) based on two triggers:
  - **Clone depth**: `softMaxCloneDepth` (default 4) triggers async flatten; `hardMaxCloneDepth` (default 8) blocks until flattened.
  - **Snapshot count**: `maxSnapshotsOnImage` (default 450) triggers flattening down to `minSnapshotsOnImage` (default 250).
  Only intermediate clones are flattened (not application-mapped volumes) to avoid I/O performance impact.
- **Combined solution** (design proposal for CBT + flattening coexistence — **NOT YET IMPLEMENTED** in ceph-csi as of March 2026):
  1. **ROX restored PVCs**: Proposed as RBD shallow volumes (like CephFS [PR #3651](https://github.com/ceph/ceph-csi/pull/3651)), preventing 3+ clone depth. CephFS has this; RBD does not.
  2. **Counter-based deletion**: Proposed for RBD VolumeSnapshots. CephFS has reference tracking ([PR #2893](https://github.com/ceph/ceph-csi/pull/2893)); RBD uses trash-based deletion instead.
  3. **Flattening prevention**: Partially implemented — [PR #2900](https://github.com/ceph/ceph-csi/pull/2900) moved flattening to flatten parent/datasource images before volume creation (not the resulting PVC). Addresses [Issue #2190](https://github.com/ceph/ceph-csi/issues/2190).
  4. **Priority-based flattening** (proposed: flatten deleted snapshots first, then clones, then alive snapshots): Not implemented. Current behavior is threshold-based only (`minSnapshotsOnImage`/`maxSnapshotsOnImage`).
  5. **Stored diffs in omap** (proposed: store diffs as doubly-linked list when flattening, to extend CBT beyond 250 snapshots): Not implemented. No PRs, issues, or design docs exist for this in ceph/ceph-csi.

  **Current CBT implementation** ([PR #5347](https://github.com/ceph/ceph-csi/pull/5347), merged July 2025): Uses `rbd DiffIterateByID` directly. Requires intact clone chains — if an intermediate image is flattened, GetMetadataDelta will fail with no fallback. See also: [Issue #5346](https://github.com/ceph/ceph-csi/issues/5346), [KEP-3314](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md).

  **Key references**:
  - [Design: rbd-snap-clone.md](https://github.com/ceph/ceph-csi/blob/devel/docs/design/proposals/rbd-snap-clone.md) — snap-clone architecture, depth/snapshot limits
  - [PR #1160](https://github.com/ceph/ceph-csi/pull/1160) — original snapshot and clone from snapshot implementation
  - [PR #1678](https://github.com/ceph/ceph-csi/pull/1678) — added `minSnapshotsOnImage` flag
  - [Issue #1800](https://github.com/ceph/ceph-csi/issues/1800) — request to support snapshots without flattening (open)
  - [Velero CBT Integration Plan](https://hackmd.io/@velero/r1U1EVKdgl)
- **SnapshotMetadataService CRD**: Stays at `v1alpha1` (out-of-tree API), does not graduate with the K8s beta milestone.

## ODF Version Compatibility

The test suite handles multiple ODF versions for pod/label discovery:
- ODF < 4.18: label `app=csi-rbdplugin-provisioner`
- ODF 4.18+: label `app.kubernetes.io/name=csi-rbdplugin,app.kubernetes.io/component=ctrlplugin`
- ODF 4.21+: fallback to pod name pattern matching (`rbd` + `ctrlplugin`)

## OpenShift Cluster Setup (ODF + CephCSI CBT)

See [`ocp-setup/README.md`](ocp-setup/README.md) for the full walkthrough. The setup scripts use `BASH_SOURCE` and must be run with **bash** from the `ocp-setup/` directory:

```bash
cd ocp-setup
bash step0-preflight.sh    # Validate cluster prerequisites
bash step2-install-odf.sh  # Install ODF operator (step1 is optional if CRD was auto-installed)
bash step3-create-storagecluster.sh  # Create StorageCluster
bash step4-cbt-sidecar.sh  # Configure CBT sidecar (SMS CR, SCC patches, deployment patches)
bash step5-verify.sh       # Verify setup
bash step6-run-e2e.sh      # Run e2e tests
```

Do NOT use `zsh -c 'source ...'` — the scripts use `BASH_SOURCE` which is bash-specific.

For a getting-started overview of Kubernetes CBT (with csi-driver-host-path), see [k8s-cbt-s3mover-demo](https://github.com/kaovilai/k8s-cbt-s3mover-demo).

References:
- [KEP-3314: CSI Changed Block Tracking](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md)
- [external-snapshot-metadata deployment](https://github.com/kubernetes-csi/external-snapshot-metadata/blob/main/deploy/README.md)
- [SnapshotMetadataService API types](https://github.com/kubernetes-csi/external-snapshot-metadata/blob/main/client/apis/snapshotmetadataservice/v1alpha1/types.go)
- [CBT sidecar setup for ODF](https://access.redhat.com/articles/7130698)
