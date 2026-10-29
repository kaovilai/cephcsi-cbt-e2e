# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

E2E test suite for **Ceph RBD Changed Block Tracking (CBT)** via the Kubernetes CSI external-snapshot-metadata API. Tests validate incremental backup capabilities (GetMetadataAllocated, GetMetadataDelta) needed by Velero and similar backup tools.

Requires a live Kubernetes 1.33+ cluster with CephCSI, ODF/Rook, and the external-snapshot-metadata sidecar deployed. No cluster is available in some development environments; without one, only `make build`, `make test`, `make test-race`, `make lint`, and `make shellcheck` can be run, and e2e results must not be claimed.

## Build and Test Commands

```bash
make build              # Compile all packages
make test               # Unit tests (pkg/..., cmd/cbt-check)
make test-coverage      # Unit tests plus coverage.html
make test-race          # Unit tests with the race detector
make lint               # golangci-lint run ./...
make lint-fix           # golangci-lint run --fix ./...
make shellcheck         # shellcheck on the shell scripts (requires shellcheck)
make e2e                # Full suite (~17m observed, 5h timeout)
make e2e-fast           # Skip stored-diffs tests (2h timeout)
make e2e-rox            # ReadOnlyMany PVC tests (30m)
make e2e-rox-deletion   # Counter-based deletion tests (30m)
make e2e-flattening     # Flattening prevention tests (30m)
make e2e-stored-diffs   # Stored diffs gap/manual flatten behavior (1h)
make e2e-errors         # Error handling tests (30m)
make e2e-backup         # Backup workflow tests (1h)
make e2e-compliance     # Velero/block metadata/error compliance + volume resize (1h)
make e2e-resize         # Volume resize tests (30m)
make e2e-rebind         # Volume mode rebind tests
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
- **Combined solution** (design proposal for CBT + flattening coexistence — only item 1 is partially implemented in ceph-csi as of Oct 2026):
  1. **ROX restored PVCs**: **Partially implemented** — not as RBD shallow volumes (like CephFS [PR #3651](https://github.com/ceph/ceph-csi/pull/3651)), but via [PR #6534](https://github.com/ceph/ceph-csi/pull/6534) (merged 2026-09-09, ceph-csi v3.18.0): when all requested volume capabilities are read-only, the snapshot restore skips flattening so the parent chain survives for `rbd diff`. [PR #6500](https://github.com/ceph/ceph-csi/pull/6500) counts trashed parents in clone depth. Caveats: only ceph-csi >= v3.18.0 (ODF builds with older ceph-csi still flatten), and RW restores still create a real clone. This repo's ROX/flattening e2e tests (`rox_pvc_test.go`, `flattening_prevention_test.go`) may behave differently on ceph-csi >= 3.18; that has not been validated here.
  2. **Counter-based deletion**: Proposed for RBD VolumeSnapshots. No PR/issue/design found for RBD as of Oct 2026. CephFS has reference tracking ([PR #2893](https://github.com/ceph/ceph-csi/pull/2893)); RBD uses trash-based deletion instead.
  3. **Flattening prevention**: Partially implemented — [PR #2900](https://github.com/ceph/ceph-csi/pull/2900) moved flattening to flatten parent/datasource images before volume creation (not the resulting PVC). Addresses [Issue #2190](https://github.com/ceph/ceph-csi/issues/2190). Default thresholds are unchanged (soft 4 / hard 8 clone depth, 450/250 snapshots).
  4. **Priority-based flattening** (proposed: flatten deleted snapshots first, then clones, then alive snapshots): No PR/issue/design found as of Oct 2026. Current behavior is threshold-based only (`minSnapshotsOnImage`/`maxSnapshotsOnImage`).
  5. **Stored diffs in omap** (proposed: store diffs as doubly-linked list when flattening, to extend CBT beyond 250 snapshots): No PR/issue/design found in ceph/ceph-csi as of Oct 2026.

  **Current CBT implementation** ([PR #5347](https://github.com/ceph/ceph-csi/pull/5347), merged July 2025): Uses `rbd DiffIterateByID` directly. Requires intact clone chains — if an intermediate image is flattened, GetMetadataDelta will fail with no driver-level fallback. [Issue #5346](https://github.com/ceph/ceph-csi/issues/5346) was closed as not_planned in Dec 2025 by the stale bot, not by a maintainer decision. CBT remains actively maintained: [#6200](https://github.com/ceph/ceph-csi/pull/6200) (`DiffIterateByID` length fix), [#6203](https://github.com/ceph/ceph-csi/pull/6203) (`ProcessMetadata` debug logging), [#6401](https://github.com/ceph/ceph-csi/pull/6401) and [#6536](https://github.com/ceph/ceph-csi/pull/6536) (read-only image opens), and [#6459](https://github.com/ceph/ceph-csi/pull/6459) (upstream RBD snapshot-metadata e2e tests). Latest release: [v3.18.1](https://github.com/ceph/ceph-csi/releases/tag/v3.18.1) (2026-10-01); v3.18.0 (2026-09-22) contains the fixes above. Helm charts are deprecated in 3.18 (removed in 3.19) in favor of ceph-csi-operator.

  **Kubernetes status**: [KEP-3314](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md) is alpha in v1.33 and beta in v1.36; not GA and no GA target as of Oct 2026. It is an out-of-tree API with no feature gate. The alpha-to-beta change is the SnapshotMetadataService CRD `v1alpha1` -> `v1beta1` (v1alpha1 removed, no conversion, schema unchanged), delivered by external-snapshot-metadata v1.0.0.

  **Key references**:
  - [Design: rbd-snap-clone.md](https://github.com/ceph/ceph-csi/blob/devel/docs/design/proposals/rbd-snap-clone.md) — snap-clone architecture, depth/snapshot limits
  - [Design: cephfs-snapshot-shallow-ro-vol.md](https://github.com/ceph/ceph-csi/blob/devel/docs/design/proposals/cephfs-snapshot-shallow-ro-vol.md) — CephFS shallow ROX volumes
  - [PR #1160](https://github.com/ceph/ceph-csi/pull/1160) — original snapshot and clone from snapshot implementation
  - [PR #1678](https://github.com/ceph/ceph-csi/pull/1678) — added `minSnapshotsOnImage` flag
  - [Issue #1800](https://github.com/ceph/ceph-csi/issues/1800) — request to support snapshots without flattening (closed, completed Dec 2020; addressed by PR #2900)
  - [Velero CBT Integration Plan](https://hackmd.io/@velero/r1U1EVKdgl)
- **SnapshotMetadataService CRD**: Graduated to `v1beta1` in external-snapshot-metadata v1.0.0 (out-of-tree API). The latest release is v1.1.0 (2026-07-07, no API change). The optional `spec.requireBaseSnapshotForDelta` field ([PR #258](https://github.com/kubernetes-csi/external-snapshot-metadata/pull/258), merged 2026-07-28) lets a driver advertise whether base snapshots must be retained: `true` retain, `false` safe to delete, unset no guidance. It is **not in a release yet** (v1.1.0 was tagged before the merge), so Velero cannot read it until it bumps to a newer external-snapshot-metadata. Ceph RBD setting the field is tracked in [ceph-csi#6680](https://github.com/ceph/ceph-csi/issues/6680); reading it in Velero is [velero#10722](https://github.com/velero-io/velero/issues/10722). When the field is `false`, KEP-3314 requires the backup application to save the CSI snapshot handle before deleting the `VolumeSnapshot`, which is why Velero must send the handle (changeID) rather than a snapshot name. The KEP text for the field is [kubernetes/enhancements#6479](https://github.com/kubernetes/enhancements/pull/6479) (open, not merged).
- **Velero Block Data Mover (BDM) CBT Integration** (design merged April 2026, [PR #9528](https://github.com/velero-io/velero/pull/9528); implementation merged on `main` and unreleased — latest Velero release is v1.18.4 and no v1.19 tag exists as of 2026-10-27, so no release date should be assumed):
  - **1.19.0 does not support Case 2** (storage that needs the base snapshot to exist when `GetMetadataDelta` is called; Ceph RBD is such a storage). Per the 2026-10-27 community meeting, the class-inherit half of [PR #10307](https://github.com/velero-io/velero/pull/10307) is being rolled back (Lyndon-Li's rollback PR), so the backup `VolumeSnapshotContent` is always `Delete`, as in 1.18. Velero does not read the VolumeSnapshotClass `deletionPolicy` for data movement. The fs data mover never retains snapshots. Case 2 is a separate design targeting a 1.19.x patch: umbrella [#10743](https://github.com/velero-io/velero/issues/10743), design [#10744](https://github.com/velero-io/velero/issues/10744), draft design document ([base-snapshot-retention.md](https://github.com/kaovilai/velero/blob/design-base-snapshot-retention/design/block-data-mover/base-snapshot-retention.md), proposals, no design PR yet). Ceph CBT is deferred, not dropped; it remains in the 1.19 stream and is targeted for OADP 1.7 (target, not commitment).
  - **Enablement**: no alpha flag; requires `EnableCSI`. `velero backup create X --snapshot-move-data --data-mover velero-block [--backup-type Incremental|Full]`, or per volume via VolumePolicy snapshot action `parameters.dataMover: velero-block|velero-fs`.
  - **CSI handle mapping**: The CBT API is entirely snapshot-oriented — no `volume_id` parameter exists. `VSC.status.snapshotHandle` serves as the `changeID` (passed as `snapshot_id` / `base_snapshot_id` in CBT RPCs). PV `volumeHandle` is NOT used in CBT APIs — volume identity is implicit from snapshot references. CephCSI resolves handles internally via RADOS OMAP journal → RBD image/snap names → `rbd DiffIterateByID`. See [Velero #9714](https://github.com/velero-io/velero/issues/9714) (closed; verified for Ceph).
  - **Ceph changeID bug**: [Velero #10294](https://github.com/velero-io/velero/issues/10294) — changeID retrieval returned empty on all non-vSphere CSI drivers (including Ceph RBD), so every "incremental" silently ran full. Fixed by [PR #10307](https://github.com/velero-io/velero/pull/10307) (merged 2026-09-09; reads `backupVSC.Spec.Source.SnapshotHandle`), which is on `main`/v1.19 and not in 1.18.x. Only the changeID half stays; the same PR's `deletionPolicy` inheritance is being rolled back.
  - **Automatic fallback**: On any CBT service error, missing changeID, or missing parent snapshot, Velero falls back instead of failing the backup. `pkg/uploader/cbt/set.go` (`SetBitmapOrFull`) degrades changed blocks → allocated blocks → full: a `GetMetadataDelta` error falls through to `GetMetadataAllocated` (an allocated-blocks backup, still reported as a fallback), and `SetFull()` is used only when the service is nil, the snapshot/changeID is empty, or both calls fail. `velero backup describe` shows `Backup Type: Incremental (fallen back to Full)` and the reason is recorded in the DataUpload `status.activities`. Fallback is error-code agnostic. Backups never fail due to CBT errors; they degrade.
  - **Snapshot retention (why Ceph loses incrementality in 1.19.0)**: Ceph RBD needs the base snapshot to still exist when `GetMetadataDelta` is called. With the class-inherit rollback, the backup VSC is always `Delete` and Velero retains nothing, so once the earlier backup's snapshot is deleted the delta call fails and the uploader falls back to a non-CBT backup (`fallbackFull`). Backup data stays correct; incrementality is lost until Case 2 lands in a 1.19.x patch. The withdrawn path (`deletionPolicy: Retain` on the VolumeSnapshotClass, inherited by the backup VSC via [#10307](https://github.com/velero-io/velero/pull/10307)) was also incomplete: nothing reclaimed those snapshots when a backup was deleted ([#9835](https://github.com/velero-io/velero/issues/9835) closed without a linked PR; [#10304](https://github.com/velero-io/velero/issues/10304) closed as a duplicate), so they leaked. The new design's proposals are a VolumePolicy parameter (`retainBaseSnapshot`, working name), then SnapshotMetadataService `requireBaseSnapshotForDelta` autodetection, then default no-retain; Velero would own Retain and keep one snapshot per volume chain.
  - **Validated CBT behavior (pre-rollback builds, base snapshot retained)**: On OCP with Rook and on ODF, end-to-end CBT produced real incrementals (for example a delta of exactly 150994944 bytes), whole-device sha256 matched at every restore point, VMs booted, and multi-disk VMs including a hotplugged disk matched checksums. In-place incremental restore (`existingVolumeDataPolicy: incremental`) moved 270532608 bytes for a 30 GiB volume in 22 s. Counter-example: with the base snapshot deleted (a `Delete` class), ceph-csi failed with `no snap source in omap` and the backup fell back to full. Combined with the rollback, this shows 1.19.0's default behavior on Ceph, not a regression in the CSI layer.
  - **Backup safety after flattening**: Once backup data is written to the Kopia repository (object storage), it is **completely independent** of the source RBD images. Flattening, snapshot deletion, or even cluster destruction does not affect existing backups. Kopia uses content-addressed storage (1MB chunks) — every backup snapshot is self-contained and independently restorable. **No user action is needed for existing backups when flattening occurs.** Restores create fresh PVCs from repository data, never touching original snapshots.
  - **Post-flattening recovery cycle**: After flattening breaks the clone chain, the next backup automatically falls back (to allocated-blocks or full, per the degrade order above). Subsequent backups resume incremental from the new full backup. Pattern: `incremental → incremental → [flatten] → full (auto) → incremental → incremental`.
  - **Self-defeating accumulation risk**: Retaining snapshots for CBT increases snapshot count, which can trigger flattening (`maxSnapshotsOnImage`), which breaks CBT. The proposed one-snapshot-per-chain design keeps this bounded: at most two Velero snapshots per chain exist during a backup, and the older one is deleted after the next backup succeeds. The original BDM design's suggestion of periodic full backups (weekly/monthly) remains the fallback mitigation for chains that already flattened.
  - **Implementation status** (as of 2026-10-27): Merged on `main`: design ([#9528](https://github.com/velero-io/velero/pull/9528)), CBT interfaces ([#9716](https://github.com/velero-io/velero/pull/9716)), unified repo extension ([#9724](https://github.com/velero-io/velero/pull/9724)), CBT bitmap ([#9736](https://github.com/velero-io/velero/pull/9736), merged 2026-05-21), CBT service ([#9846](https://github.com/velero-io/velero/pull/9846)), change-id/volume-id in backup ([#9863](https://github.com/velero-io/velero/pull/9863)), CBT parameters in node-agent-config ([#9864](https://github.com/velero-io/velero/pull/9864)), `BackupType` in the backup spec ([#9954](https://github.com/velero-io/velero/pull/9954)), block uploader backup ([#9979](https://github.com/velero-io/velero/pull/9979)) and restore ([#10071](https://github.com/velero-io/velero/pull/10071)), backup workflow ([#10067](https://github.com/velero-io/velero/pull/10067)), docs ([#10537](https://github.com/velero-io/velero/pull/10537)), `BLKZEROOUT` ioctl fix ([#10698](https://github.com/velero-io/velero/pull/10698), restore zero ranges), design case-order fix ([#10696](https://github.com/velero-io/velero/pull/10696)), and kopia snapshot-not-found after an interrupted deletion ([#10700](https://github.com/velero-io/velero/pull/10700)). Closed as completed: [#9710](https://github.com/velero-io/velero/issues/9710), [#9715](https://github.com/velero-io/velero/issues/9715), and the umbrella [#9556](https://github.com/velero-io/velero/issues/9556). The in-place (incremental) restore design ([#10014](https://github.com/velero-io/velero/pull/10014), merged 2026-08-10) is candidate coverage for this suite. Open related PRs: [#10699](https://github.com/velero-io/velero/pull/10699) (in-place restore baseline snapshot leak), [#10708](https://github.com/velero-io/velero/pull/10708) (draft, source VSC left by an interrupted expose), [#10710](https://github.com/velero-io/velero/pull/10710) (VolumeSnapshots of failed backups outside the Velero namespace), [#10718](https://github.com/velero-io/velero/pull/10718) (docs), and [#10740](https://github.com/velero-io/velero/pull/10740) (parallel upload/download control for the fs and block data movers; see the OOM note below). [#10691](https://github.com/velero-io/velero/pull/10691) is a draft, the candidate implementation for [#10744](https://github.com/velero-io/velero/issues/10744); [#10716](https://github.com/velero-io/velero/pull/10716) is closed, replaced by the rollback. Closed as not planned (not fixed): [#10300](https://github.com/velero-io/velero/issues/10300) (`/udmrepo` unwritable on OpenShift non-root), [#10302](https://github.com/velero-io/velero/issues/10302) (silent fs-uploader fallback without a volume policy), [#10303](https://github.com/velero-io/velero/issues/10303) (DataUpload InProgress ~4h after pod loss).
  - **Memory and restore limits observed in the field**: on a 96 vCPU node, the block data mover still OOMs with default settings ([#10724](https://github.com/velero-io/velero/issues/10724), fix in [#10740](https://github.com/velero-io/velero/pull/10740)). Setting the data mover pod `cpuLimit` to 2, 8, or 16 kept peak memory at 384 MiB, 690 MiB, and 928 MiB respectively, all completing; the limits are set through the node-agent ConfigMap `podResources`. Separately, a restore that mixes a `WaitForFirstConsumer` PVC with an `Immediate` PVC can deadlock the single-worker DataDownload controller ([#10730](https://github.com/velero-io/velero/issues/10730); re-confirmed 2026-10-27), with `restorePVC.ignoreDelayBinding: true` as the workaround.

## ODF Version Compatibility

The test suite handles multiple ODF versions for pod/label discovery:
- ODF < 4.18: label `app=csi-rbdplugin-provisioner`
- ODF 4.18+: label `app.kubernetes.io/name=csi-rbdplugin,app.kubernetes.io/component=ctrlplugin`
- ODF 4.21+: fallback to pod name pattern matching (`rbd` + `ctrlplugin`)

The next ODF release after 4.22 is named ODF 5.0 (there is no 4.23; OCP 4.23 was renamed OCP 5.0). Scripts that parse the ODF version must treat major >= 5 as newer than 4.22. Pod labels did not change in 4.22 or 5.0. OCP 5.0 is not GA as of Oct 2026 (release candidates only).

CBT status on OpenShift: Developer Preview on OCP/ODF 4.20-4.22 ([KCS 7130698](https://access.redhat.com/articles/7130698), login required, is written for ODF 4.20; the same status for 4.21/4.22 is assumed, not confirmed in its public text), Technology Preview on OCP 5.0; not GA anywhere. The `ExternalSnapshotMetadata` OCP feature gate is `DevPreviewNoUpgrade` on 4.20-4.22, `TechPreviewNoUpgrade`/`DevPreviewNoUpgrade` on 4.23/5.0, and `CustomNoUpgrade` works everywhere.

### SELinux host mount for RBD Filesystem volumes on OpenShift

When Rook deploys ceph-csi through ceph-csi-operator on OpenShift, the operator sets `seLinuxMount: true` on the `CSIDriver`, so kubelet passes a `context=` mount option and skips relabeling. The node plugin must then see the host's SELinux configuration. With `nodePlugin.enableSeLinuxHostMount: false` (the operator default), libselinux inside the plugin container reports SELinux disabled, `mount` silently drops `context=`, an RBD `volumeMode: Filesystem` volume stays `unlabeled_t`, and confined or non-root pods get `EACCES` ([ceph-csi#6679](https://github.com/ceph/ceph-csi/issues/6679)). Set `nodePlugin.enableSeLinuxHostMount: true` in the OperatorConfig or the Driver CR ([ceph-csi-operator#649](https://github.com/ceph/ceph-csi-operator/pull/649), merged; [rook#18636](https://github.com/rook/rook/pull/18636) updates the OpenShift example). Existing volumes pick up `context=` only after they are unstaged and remounted. Block-mode volumes are unaffected, which is why the CBT tests here mostly do not hit it.

### Sidecar image versions per ODF

The sidecar is injected by ceph-csi-operator when the RBD Driver CR `controllerPlugin.volumes` contains a volume named `tls-key` (mounted at `/tmp/certificates`); the presence of a SnapshotMetadataService CR is not the trigger. The operator's feature docs are in [rbd-snapshot-metadata.md](https://github.com/red-hat-storage/ceph-csi-operator/blob/main/docs/features/rbd-snapshot-metadata.md) (Beta). Default snapshot-metadata sidecar versions in ocs-client-operator: ODF 4.20 = v0.1.0, 4.21 = v0.1.0, 4.22 = v0.2.0, 5.0 = v1.0.0. upstream [ceph-csi-operator v1.1.0](https://github.com/ceph/ceph-csi-operator/releases/tag/v1.1.0) (2026-10-01) defaults to snapshot-metadata v1.1.0, cephcsi v3.18.1, and snapshotter v8.6.0.

### Early ODF 4.21 Sidecar Image Workaround

Early ODF 4.21 builds ship an older `external-snapshot-metadata` sidecar image (`ose-csi-external-snapshot-metadata-rhel9`, snapshot-metadata v0.1.0) that uses `BaseSnapshotName` (name-based VolumeSnapshot lookup) instead of `BaseSnapshotId` (CSI handle passthrough) in `GetMetadataDelta`. This breaks delta/incremental CBT operations because the sidecar tries to look up the CSI snapshot handle as a VolumeSnapshot name, which fails with "not found". Later 4.21.z builds may already contain the fix (unverified; check the sidecar image of the build under test).

**Fix**: [red-hat-storage/ceph-csi-operator commit 454e9130](https://github.com/red-hat-storage/ceph-csi-operator/commit/454e9130a39057b5a9ca98785df605ada195d62e) ("csi: update K8s CSI sidecar images", bumps the snapshot-metadata default from v0.1.0 to v0.2.0). It is in release-4.22, release-5.0, and main, and not in release-4.21.

**Workaround** (automated in `step4-cbt-sidecar.sh` step 4g): Override the sidecar image to upstream `registry.k8s.io/sig-storage/csi-snapshot-metadata:v0.2.0` via a driver-level ImageSet ConfigMap on the Driver CR. The ceph-csi-operator reconciles both the ImageSet and TLS volume from the Driver CR — no need to scale it down. This is the only configuration validated by this repo (48/48 tests on OCP 4.21.7, see `results.md`). Upstream v1.1.0 is a plausible alternative (Velero's live Ceph/ODF validation for [PR #10307](https://github.com/velero-io/velero/pull/10307) used it: OCP 5.0 + ODF 4.21.10 per [velero-io/velero#10294](https://github.com/velero-io/velero/issues/10294), with a hand-applied v1beta1 CRD and the upstream sidecar rather than ODF's shipped build) but is untested here. ceph-csi-operator v1.1.0 skips empty ImageSet values when merging defaults ([PR #622](https://github.com/ceph/ceph-csi-operator/pull/622)), so re-validate the driver-level override on that operator version.

## OpenShift Cluster Setup (ODF + CephCSI CBT)

See [`ocp-setup/README.md`](ocp-setup/README.md) for the full walkthrough. The setup scripts use `BASH_SOURCE` and must be run with **bash** from the `ocp-setup/` directory:

```bash
cd ocp-setup
bash step0-preflight.sh    # Validate cluster prerequisites
bash step2-install-odf.sh  # Install ODF operator (step1 is optional if CRD was auto-installed)
bash step3-create-storagecluster.sh  # Create StorageCluster
bash step4-cbt-sidecar.sh  # Configure CBT sidecar (SMS CR, Driver CR patches)
bash step5-verify.sh       # Verify setup
bash step6-run-e2e.sh      # Run e2e tests
```

Do NOT use `zsh -c 'source ...'` — the scripts use `BASH_SOURCE` which is bash-specific.

For a getting-started overview of Kubernetes CBT (with csi-driver-host-path), see [k8s-cbt-s3mover-demo](https://github.com/kaovilai/k8s-cbt-s3mover-demo).

References:
- [KEP-3314: CSI Changed Block Tracking](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md)
- [external-snapshot-metadata deployment](https://github.com/kubernetes-csi/external-snapshot-metadata/blob/main/deploy/README.md)
- [SnapshotMetadataService API types](https://github.com/kubernetes-csi/external-snapshot-metadata/blob/main/client/apis/snapshotmetadataservice/v1beta1/types.go)
- [CBT sidecar setup for ODF](https://access.redhat.com/articles/7130698) (Red Hat login required)
- [ceph-csi-operator: RBD snapshot metadata](https://github.com/red-hat-storage/ceph-csi-operator/blob/main/docs/features/rbd-snapshot-metadata.md)
