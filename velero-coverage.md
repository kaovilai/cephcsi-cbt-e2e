# Velero Block Data Mover - E2E Test Coverage Mapping

This document maps the Velero Block Data Mover (BDM) design ([PR #9528](https://github.com/velero-io/velero/pull/9528))
and its implementation — specifically the **volume snapshot retention** behavior — to
E2E tests in this suite that validate Ceph RBD CBT compliance. Status is as of
October 2026.

Reference: [Velero PR #9528](https://github.com/velero-io/velero/pull/9528) |
[velero-feedback.md](./velero-feedback.md)

---

## Velero Implementation Status (as of Oct 2026)

The block data mover is merged on Velero `main` and targets **v1.19, which is not
yet released** (latest release: v1.18.4; no v1.19 tag or release candidate exists).
No GA date is implied here.

| Area | Status |
|------|--------|
| Design | Merged ([#9528](https://github.com/velero-io/velero/pull/9528)) |
| CBT interfaces / unified repo | Merged ([#9716](https://github.com/velero-io/velero/pull/9716), [#9724](https://github.com/velero-io/velero/pull/9724)) |
| CBT bitmap (`pkg/uploader/cbt`) | Merged ([#9736](https://github.com/velero-io/velero/pull/9736)) |
| SnapshotMetadataService client / service-to-bitmap glue | Closed as completed ([#9710](https://github.com/velero-io/velero/issues/9710), [#9715](https://github.com/velero-io/velero/issues/9715)); CBT service merged in [#9846](https://github.com/velero-io/velero/pull/9846) |
| End-to-end block data mover umbrella | Closed as completed ([#9556](https://github.com/velero-io/velero/issues/9556)) |
| Verify ChangeId for Ceph | Closed as completed ([#9714](https://github.com/velero-io/velero/issues/9714)) |
| Generic changeID retrieval + snapshot-class deletion policy for CBT retention | Merged ([#10307](https://github.com/velero-io/velero/pull/10307), fixes [#10294](https://github.com/velero-io/velero/issues/10294)); on `main`/v1.19, not in 1.18.x |
| In-place incremental restore | Design merged ([#10014](https://github.com/velero-io/velero/pull/10014)) |
| User docs | `csi-snapshot-data-movement.md` updated by [#10537](https://github.com/velero-io/velero/pull/10537) |

Enablement: there is no alpha feature flag. The CSI plugin must be enabled and the
backup selects the block data mover:

```bash
velero backup create NAME --snapshot-move-data --data-mover velero-block [--backup-type Incremental|Full]
```

The data mover can also be chosen per volume through a VolumePolicy `snapshot`
action with `parameters.dataMover: velero-block|velero-fs`.

---

## Velero Design: Two Cases for Snapshot Retention

The design in PR #9528 defines two cases based on storage backend capabilities
(the design text calls them "Way 1 / Way 2" in places):

| Case | Behavior | Ceph RBD |
|------|----------|----------|
| **Case 1** (Default) | Delete snapshot after backup; storage can compute deltas without the parent snapshot | Not supported: every backup after the first degrades to a full backup |
| **Case 2** (Opt-in) | Retain the snapshot (`deletionPolicy: Retain`); parent must exist for delta | **Required for Ceph RBD** |

Ceph RBD requires **Case 2** because `rbd snap diff` needs both snapshots in the
same clone chain. A stored-diffs fallback in CephCSI is still only a proposal (not
implemented), so Case 1 remains unsupported for Ceph today (see
[ceph/ceph-csi#1800](https://github.com/ceph/ceph-csi/issues/1800) and
[ceph/ceph-csi#5346](https://github.com/ceph/ceph-csi/issues/5346); #5346 was closed
`not_planned` by the stale bot in Dec 2025, not by a maintainer decision).

> "There is no way to automatically detect which way a specific volume supports"
> — PR #9528 design document

### How retention is configured in the implementation

The design describes a `RetainSnapshot` VolumePolicy parameter. That parameter is
**not implemented** in Velero code. The implemented mechanism
([#10307](https://github.com/velero-io/velero/pull/10307)) is that the backup's
`VolumeSnapshotContent` inherits `DeletionPolicy` from the source
VolumeSnapshotContent / VolumeSnapshotClass (releases without this fix always
use `Delete`). For Ceph RBD, set `deletionPolicy: Retain` on the VolumeSnapshotClass used
for backups. There is no auto-detection; users must configure it. (Upstream
[external-snapshot-metadata#258](https://github.com/kubernetes-csi/external-snapshot-metadata/pull/258),
unreleased, would let a driver advertise `requireBaseSnapshotForDelta`; CephCSI/ODF
support for it is not verified.)

Note that `config/snapshotclass-rbd.yaml` in this repo uses `deletionPolicy: Delete`,
so the suite exercises the Case 1 (non-retained) behavior by default.

---

## Test-to-Design Mapping

Tests are referenced by file and test description; descriptions can be used with
`--focus` to run a single test.

### Snapshot Retention (Case 1 / Case 2)

| Velero Requirement | Test File | Test Description | What's Validated |
|----|----|----|----|
| Case 1 (delete after backup) does not work with Ceph | `velero_compliance_test.go` | "should fail delta when parent snapshot is deleted (Case 1 no-retention does not work with Ceph)" | Deletes the parent VolumeSnapshot, waits for the VolumeSnapshotContent (and RBD snapshot) to be deleted, then asserts `GetChangedBlocksByID` fails |
| ChangeId = CSI snapshot handle | `velero_compliance_test.go` | "should return changed blocks using snapshot handle ID (GetChangedBlocksByID)" | `GetChangedBlocksByID(snap1Handle, snap2Name)` returns the correct delta using the CSI handle format `<clusterID>-<pool>-<imageID>-<snapID>` |
| Handle-based matches name-based | `velero_compliance_test.go` | "should return consistent results between name-based and handle-based delta" | Compares `GetChangedBlocks` and `GetChangedBlocksByID` results |
| Deferred deletion with references | `counter_deletion_test.go` | "should defer snapshot deletion while ROX PVCs exist" | Requests snapshot deletion while ROX PVCs exist and logs the resulting state (the assertion is informational: it does not fail if the VolumeSnapshot object is removed) |
| Deletion completes after cleanup | `counter_deletion_test.go` | "should complete deletion after all ROX PVCs are removed" | Deletes ROX PVCs one by one, then asserts the snapshot is fully deleted |

The suite does not currently test the Case 2 path (retained snapshot with
`deletionPolicy: Retain`); see [Candidate Test Ideas](#candidate-test-ideas-not-implemented).

### Incremental Backup Chain

| Velero Requirement | Test File | Test Description | What's Validated |
|----|----|----|----|
| Full backup via `GetMetadataAllocated` | `backup_workflow_test.go` | "should perform full backup via GetMetadataAllocated" | All written blocks reported as allocated |
| Incremental via `GetMetadataDelta` | `backup_workflow_test.go` | "should perform first incremental backup via GetMetadataDelta" | Only changed blocks in delta; unchanged blocks excluded |
| Chained incrementals (full->incr1->incr2) | `backup_workflow_test.go` | "should perform second incremental backup (chained full->incr1->incr2)" | Second delta captures only its own changes |
| Allocated metadata matches written data | `backup_workflow_test.go` | "should report accurate metadata matching all written data" | All five written blocks reported as allocated |
| Restore + data integrity verification | `backup_workflow_test.go` | "should restore from chain and verify data integrity" | SHA256 verification of blocks after restore from the last snapshot |
| Full Velero workflow simulation | `velero_compliance_test.go` | "should simulate Velero incremental backup chain with handle-based delta" | Full->Incr1->Incr2 using the handle-based API, then restore + verify |
| ROX PVC as backup data source | `backup_workflow_test.go` | "should support backup workflow with ROX PVCs as read source" | ROX PVC is readable, rejects writes; CBT keeps working while the ROX PVC exists |

### Fallback on Flattening (Current State)

Velero's `SetBitmapOrFull` (`pkg/uploader/cbt/set.go`, merged in
[#9736](https://github.com/velero-io/velero/pull/9736)) degrades **changed blocks ->
allocated blocks -> full**: if `GetChangedBlocks` errors, it falls through to
`GetAllocatedBlocks` (an allocated-blocks backup, still reported as a fallback), and
`SetFull()` is used only when the service is nil, the snapshot/changeID is empty, or
both calls fail. Any CBT service error, missing changeID, or missing parent triggers
the fallback; it is not limited to specific gRPC codes. The backup description shows
`Backup Type: Incremental (fallen back to Full)` and the reason is recorded in the
DataUpload `status.activities`.

| Velero Requirement | Test File | Test Description | What's Validated |
|----|----|----|----|
| Intact chains before flatten | `stored_diffs_test.go` | "should have intact parent chains on intermediate images", "should have CBT working on all snapshots with intact chains" | CBT works while clone chains are intact |
| Manual flatten | `stored_diffs_test.go` | "should force-flatten all intermediate images via rbd flatten" | Intermediate images are flattened via `rbd flatten` |
| No stored diffs after manual flatten | `stored_diffs_test.go` | "should have no stored diffs in omap (manual flatten bypasses CephCSI)" | No usable stored diffs found in omap after manual flatten |
| `GetMetadataAllocated` after flatten | `stored_diffs_test.go` | "should fail GetMetadataAllocated after flattening without stored diffs" | Allocated metadata calls fail once chain/snapshot references are broken |
| `GetMetadataDelta` after flatten | `stored_diffs_test.go` | "should fail GetMetadataDelta after flattening without stored diffs" | Delta calls fail for flattened pairs; there is no driver-level fallback |
| Velero handling path | [PR #9736](https://github.com/velero-io/velero/pull/9736) | N/A | Merged. Velero degrades to allocated-blocks / full backup; the suite validates only the driver-side failure that triggers it |

### Snapshot Count Limits and Flattening Priority

CephCSI flattens based on two thresholds: `maxSnapshotsOnImage` (default **450**)
triggers flattening, which reduces the image down to `minSnapshotsOnImage` (default
**250**). 250 is therefore the flatten-down target, not a hard per-image limit.

| Velero Requirement | Tracking Source | Current Status |
|----|----|----|
| Snapshot count pressure handling | [ceph/ceph-csi design](https://github.com/ceph/ceph-csi/blob/devel/docs/design/proposals/rbd-snap-clone.md) | Applies via CephCSI thresholds (`maxSnapshotsOnImage` 450, `minSnapshotsOnImage` 250) |
| Priority flattening (deleted snapshots first) | [ceph/ceph-csi#1800](https://github.com/ceph/ceph-csi/issues/1800) (closed, completed Dec 2020) | **Proposal only**; not implemented in CephCSI (current behavior is threshold-based) |
| Preserve recent snapshots for CBT under flatten pressure | [velero-io/velero#9556](https://github.com/velero-io/velero/issues/9556) (closed) | Not confirmed in Velero; retained snapshots accumulate on the image and count toward `maxSnapshotsOnImage` |

### Clone Chain Preservation

| Velero Requirement | Test File | Test Description | What's Validated |
|----|----|----|----|
| PVC->Snap->Restore->Snap chain intact | `flattening_prevention_test.go` | "should NOT flatten restored PVC after restore and re-snapshot" | `IsImageFlattened()` returns false for the restored PVC image |
| CBT works across restore chain | `flattening_prevention_test.go` | "should have CBT working across the chain" | `GetAllocatedBlocks` succeeds on the newest snapshot (the older one is tolerated if its intermediate image was flattened) |
| PVC->Clone->Snap chain intact | `flattening_prevention_test.go` | "should NOT flatten cloned PVC after clone and snapshot", "should have CBT working on the clone's snapshot" | Cloned image not flattened; CBT works on the clone's snapshot |
| ROX PVCs don't trigger flattening | `rox_pvc_test.go` | "should not flatten ROX PVC despite multiple ROX PVCs from same snapshot" | Multiple ROX PVCs leave the ROX image unflattened |

On ceph-csi >= v3.18.0, restores where all volume capabilities are read-only skip
flattening ([ceph-csi#6534](https://github.com/ceph/ceph-csi/pull/6534)), so
the ROX and flattening tests may behave differently there; this has not been
validated against a live cluster.

### Error Handling and Edge Cases

| Velero Requirement | Test File | Test Description | What's Validated |
|----|----|----|----|
| Error on non-existent snapshot | `error_handling_test.go` | "should return error for CBT on non-existent snapshot" | Clear error when the snapshot doesn't exist |
| Error on cross-PVC delta | `error_handling_test.go` | "should return error for GetMetadataDelta across different PVCs" | Snapshots must be from the same volume for delta |
| Error on reversed snapshot order | `error_handling_test.go` | "should return error for reversed snapshot order in GetMetadataDelta" | Temporal ordering enforced |
| Concurrent CBT operations | `error_handling_test.go` | "should handle concurrent snapshot creation and CBT operations" | Concurrent `GetAllocatedBlocks` calls succeed |
| Invalid handle | `error_handling_test.go` (Error Compliance) | "should return error for invalid snapshot handle in GetChangedBlocksByID" | Invalid `base_snapshot_id` is rejected |
| Handle from different volume | `error_handling_test.go` (Error Compliance) | "should return error for GetChangedBlocksByID with handle from different volume" | Cross-volume handle is rejected |
| Snapshot not ready | `error_handling_test.go` (Error Compliance) | "should return error when querying CBT on snapshot that is not ready" | Error before the snapshot is ReadyToUse |

---

## Candidate Test Ideas (Not Implemented)

These behaviors follow from the current Velero and CephCSI implementations. None is
covered by the suite today and no results are claimed for them.

| Candidate | Why it matters |
|-----------|----------------|
| Retained RBD snapshot after VolumeSnapshot deletion with `deletionPolicy: Retain` | Validates the actual Case 2 mechanism ([#10307](https://github.com/velero-io/velero/pull/10307)): use a VolumeSnapshotClass with `Retain`, delete the VolumeSnapshot, confirm the VolumeSnapshotContent and underlying RBD snapshot remain, and that `GetChangedBlocksByID(retainedHandle, newSnap)` still succeeds. This replaces the retired "Type 1 / Type 2" handle-after-deletion probe. |
| Leaked `csi-snap-*` images after backup deletion | If retained snapshots are not reclaimed when the backup is deleted (reclamation is **not confirmed implemented** in Velero; [#9835](https://github.com/velero-io/velero/issues/9835) closed with no linked PR), RBD snapshots and `csi-snap-*` images accumulate. A test could delete the retained VolumeSnapshotContent and verify the RBD snapshot and intermediate image are removed, and measure leakage otherwise. |
| In-place incremental restore | The design ([#10014](https://github.com/velero-io/velero/pull/10014)) restores incremental data onto an existing volume. A test could restore a delta onto the original PVC using CBT-reported blocks and verify block hashes. Depends on Velero implementation status. |
| Snapshot-count pressure | Create snapshots up to `maxSnapshotsOnImage` with retention enabled and observe when the chain breaks (needs a long-running cluster test). |

---

## Ceph RBD Retention Requirements Summary

For Velero with Ceph RBD, retain snapshots through the VolumeSnapshotClass used for
the backup (the `RetainSnapshot` VolumePolicy parameter from the design is not
implemented):

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: ocs-storagecluster-rbdplugin-snapclass-retain
  labels:
    velero.io/csi-volumesnapshot-class: "true"
driver: openshift-storage.rbd.csi.ceph.com
deletionPolicy: Retain
parameters:
  clusterID: openshift-storage
  csi.storage.k8s.io/snapshotter-secret-name: rook-csi-rbd-provisioner
  csi.storage.k8s.io/snapshotter-secret-namespace: openshift-storage
```

(Adjust `driver`, `clusterID`, and secret names to the cluster; see
`config/snapshotclass-rbd.yaml` for the base manifest.) The VolumePolicy is then used
only to select the data mover:

```yaml
version: v1
volumePolicies:
  - conditions:
      csi:
        driver: openshift-storage.rbd.csi.ceph.com
    action:
      type: snapshot
      parameters:
        dataMover: velero-block
```

### Why Retention is Required

1. **`rbd snap diff` needs both snapshots** in the same clone chain
2. **Deleting the base snapshot** breaks all future incremental backups
3. **Without retention**, every backup after the first degrades (changed -> allocated
   -> full)
4. **Handle-based deltas** (`GetChangedBlocksByID`) allow K8s VolumeSnapshot object
   cleanup while retaining the underlying RBD snapshot (retention path not yet
   covered by this suite; see candidates above)

### Retention Constraints

- **Snapshot count thresholds**: CephCSI starts flattening at `maxSnapshotsOnImage`
  (default 450) and flattens down to `minSnapshotsOnImage` (default 250). Retained
  snapshots count toward these thresholds, so retention can eventually trigger the
  flattening that breaks the chain.
- **Priority flattening** (deleted snapshots first) is a proposal, not implemented
  ([ceph/ceph-csi#1800](https://github.com/ceph/ceph-csi/issues/1800) is closed and no
  longer tracks it)
- **Stored diffs in omap** is a proposal; no PRs, issues or design docs exist in
  ceph/ceph-csi
- **Reclamation of retained snapshots at backup deletion** is not confirmed
  implemented in Velero ([#10304](https://github.com/velero-io/velero/issues/10304)
  was closed as a duplicate of [#9835](https://github.com/velero-io/velero/issues/9835);
  `backup_deletion_controller.go` has no retain/CBT handling as of this writing).
  Leaked snapshots increase flatten pressure.
- **changeID fix**: before [#10307](https://github.com/velero-io/velero/pull/10307)
  (`main`/v1.19 only), changeID retrieval returned empty for non-vSphere CSI drivers,
  so every "incremental" ran full.

### Fallback Strategy

When the CBT call fails, any error (not only `FAILED_PRECONDITION` /
`INVALID_ARGUMENT`) triggers the fallback:
1. Try `GetMetadataAllocated` (allocated-blocks backup); use a full backup only if
   that also fails or no snapshot/changeID is available
2. Report the fallback (`Backup Type: Incremental (fallen back to Full)`, reason in
   DataUpload `status.activities`)
3. The new snapshot becomes the base for future incrementals, so the chain is
   re-established

---

## Basic CBT Validation (Foundational)

| Test File | Test Description |
|----|----|
| `backup_workflow_test.go` | "should perform full backup via GetMetadataAllocated" (single-snapshot allocated blocks) |
| `backup_workflow_test.go` | "should perform first incremental backup via GetMetadataDelta" (changed blocks between consecutive snapshots) |
| `backup_workflow_test.go` | "should report accurate metadata matching all written data" |
| `block_metadata_properties_test.go` | Ordering, non-overlap, 1 MiB alignment, `VolumeCapacityBytes`, `StartingOffset`, `MaxResults`, non-aligned volume size |

---

## Running Retention-Relevant Tests

```bash
# All retention-relevant tests (excludes stored-diffs/manual-flatten behavior)
make e2e-fast

# Specific test suites
make e2e-rox-deletion   # Counter-based deletion tests (30m)
make e2e-flattening     # Flattening prevention tests (30m)
make e2e-stored-diffs   # Stored diffs gap/manual flatten behavior (1h)
make e2e-backup         # Backup workflow tests (1h)

# Single test by description
go run github.com/onsi/ginkgo/v2/ginkgo -v \
  --focus='should fail delta when parent snapshot is deleted' \
  ./tests/e2e/... -- \
  --storage-class=ocs-storagecluster-ceph-rbd \
  --snapshot-class=ocs-storagecluster-rbdplugin-snapclass
```
