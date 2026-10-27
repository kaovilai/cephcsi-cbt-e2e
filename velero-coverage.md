# Velero Block Data Mover - E2E Test Coverage Mapping

This document maps the Velero Block Data Mover (BDM) design ([PR #9528](https://github.com/velero-io/velero/pull/9528))
and its implementation — specifically the **volume snapshot retention** behavior — to
E2E tests in this suite that validate Ceph RBD CBT compliance. Status is as of
2026-10-27.

Reference: [Velero PR #9528](https://github.com/velero-io/velero/pull/9528) |
[velero-feedback.md](./velero-feedback.md)

---

## Velero Implementation Status (as of 2026-10-27)

The block data mover is merged on Velero `main`. **v1.19.0 is in development and not
released** (latest release: v1.18.4; no v1.19 tag exists). No GA date is implied here.

| Area | Status |
|------|--------|
| Design | Merged ([#9528](https://github.com/velero-io/velero/pull/9528)) |
| CBT interfaces / unified repo | Merged ([#9716](https://github.com/velero-io/velero/pull/9716), [#9724](https://github.com/velero-io/velero/pull/9724)) |
| CBT bitmap (`pkg/uploader/cbt`) | Merged ([#9736](https://github.com/velero-io/velero/pull/9736)) |
| SnapshotMetadataService client / service-to-bitmap glue | Closed as completed ([#9710](https://github.com/velero-io/velero/issues/9710), [#9715](https://github.com/velero-io/velero/issues/9715)); CBT service merged in [#9846](https://github.com/velero-io/velero/pull/9846) |
| End-to-end block data mover umbrella | Closed as completed ([#9556](https://github.com/velero-io/velero/issues/9556)) |
| Verify ChangeId for Ceph | Closed as completed ([#9714](https://github.com/velero-io/velero/issues/9714)) |
| Generic changeID retrieval | Merged ([#10307](https://github.com/velero-io/velero/pull/10307), fixes [#10294](https://github.com/velero-io/velero/issues/10294)); on `main`/v1.19, not in 1.18.x |
| Snapshot-class `deletionPolicy` inheritance for CBT retention | **Being rolled back** for 1.19.0 (class-inherit half of [#10307](https://github.com/velero-io/velero/pull/10307)). The backup VSC is always `Delete`, as in 1.18 |
| Case 2 retention (storage needing the base snapshot) | Separate design, target 1.19.x: umbrella [#10743](https://github.com/velero-io/velero/issues/10743), design [#10744](https://github.com/velero-io/velero/issues/10744); candidate implementation [#10691](https://github.com/velero-io/velero/pull/10691) (draft) |
| Block uploader zero-range restore | Merged ([#10698](https://github.com/velero-io/velero/pull/10698), `BLKZEROOUT` ioctl) |
| Deletion and cleanup fixes | Merged [#10700](https://github.com/velero-io/velero/pull/10700) (kopia snapshot not found after an interrupted deletion); open [#10699](https://github.com/velero-io/velero/pull/10699) (in-place restore baseline snapshot), [#10708](https://github.com/velero-io/velero/pull/10708) (source VSC after interrupted expose, draft), [#10710](https://github.com/velero-io/velero/pull/10710) (failed-backup VolumeSnapshots) |
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
| **Case 1** (Default) | Delete snapshot after backup; storage can compute deltas without the parent snapshot | Not supported: every backup after the first falls back to allocated blocks or full |
| **Case 2** (Opt-in) | Retain the base snapshot; parent must exist for delta | **Required for Ceph RBD, not supported in 1.19.0**; design [#10744](https://github.com/velero-io/velero/issues/10744) targets 1.19.x |

Ceph RBD requires **Case 2** because `rbd snap diff` needs both snapshots in the
same clone chain. A stored-diffs fallback in CephCSI is still only a proposal (not
implemented), so Case 1 remains unsupported for Ceph today (see
[ceph/ceph-csi#1800](https://github.com/ceph/ceph-csi/issues/1800) and
[ceph/ceph-csi#5346](https://github.com/ceph/ceph-csi/issues/5346); #5346 was closed
`not_planned` by the stale bot in Dec 2025, not by a maintainer decision).

> "There is no way to automatically detect which way a specific volume supports"
> — PR #9528 design document

That remains true for 1.19.0. The SnapshotMetadataService
`spec.requireBaseSnapshotForDelta` field
([external-snapshot-metadata#258](https://github.com/kubernetes-csi/external-snapshot-metadata/pull/258),
merged 2026-07-28, not in a release yet) is the intended discovery mechanism once a
driver sets it and Velero reads it ([velero#10722](https://github.com/velero-io/velero/issues/10722),
[ceph-csi#6680](https://github.com/ceph/ceph-csi/issues/6680)).

### How retention is configured (1.19.0 and the pending design)

**In 1.19.0 there is no retention configuration.** The class-inherit half of
[#10307](https://github.com/velero-io/velero/pull/10307) is being rolled back, so the
backup `VolumeSnapshotContent` is always `Delete` and Velero does not read the
VolumeSnapshotClass `deletionPolicy` for data movement. The file system data mover never
retains snapshots.

Builds between #10307 and the rollback did inherit `DeletionPolicy` from the source
VolumeSnapshotContent / VolumeSnapshotClass; that is the **pre-rollback** mechanism the
results below were produced with. Nothing tracked or deleted those snapshots, so they
leaked.

The design for a 1.19.x patch ([#10744](https://github.com/velero-io/velero/issues/10744))
proposes: a VolumePolicy parameter (working name `retainBaseSnapshot`) over
SnapshotMetadataService autodetection over default no-retain; Velero owns the Retain
lifecycle; one retained snapshot per volume chain, rotated after the next backup
succeeds and deleted with its backup.

Note that `config/snapshotclass-rbd.yaml` in this repo uses `deletionPolicy: Delete`,
which now matches 1.19.0 behavior. The suite exercises Case 1 (non-retained) by default.

---

## Test-to-Design Mapping

Tests are referenced by file and test description; descriptions can be used with
`--focus` to run a single test.

### Snapshot Retention (Case 1 / Case 2)

| Velero Requirement | Test File | Test Description | What's Validated |
|----|----|----|----|
| Case 1 (delete after backup) does not work with Ceph | `velero_compliance_test.go` | "should fail delta when parent snapshot is deleted (Case 1 no-retention does not work with Ceph)" | Deletes the parent VolumeSnapshot, waits for the VolumeSnapshotContent (and RBD snapshot) to be deleted, then asserts `GetChangedBlocksByID` fails. This is the 1.19.0 default path: Velero sees the error and falls back to a non-CBT backup |
| ChangeId = CSI snapshot handle | `velero_compliance_test.go` | "should return changed blocks using snapshot handle ID (GetChangedBlocksByID)" | `GetChangedBlocksByID(snap1Handle, snap2Name)` returns the correct delta using the CSI handle format `<clusterID>-<pool>-<imageID>-<snapID>` |
| Handle-based matches name-based | `velero_compliance_test.go` | "should return consistent results between name-based and handle-based delta" | Compares `GetChangedBlocks` and `GetChangedBlocksByID` results |
| Deferred deletion with references | `counter_deletion_test.go` | "should defer snapshot deletion while ROX PVCs exist" | Requests snapshot deletion while ROX PVCs exist and logs the resulting state (the assertion is informational: it does not fail if the VolumeSnapshot object is removed) |
| Deletion completes after cleanup | `counter_deletion_test.go` | "should complete deletion after all ROX PVCs are removed" | Deletes ROX PVCs one by one, then asserts the snapshot is fully deleted |

The suite does not currently test the Case 2 path. A retained-snapshot delta test
written against the pre-rollback `deletionPolicy: Retain` mechanism is listed under
[Candidate Test Ideas](#candidate-test-ideas-not-implemented) and is marked pre-rollback;
the new design's parameter is still a proposal, so a test should not hard-code a name.

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
| Retained RBD snapshot after VolumeSnapshot deletion with `deletionPolicy: Retain` (**pre-rollback behavior**) | Validates the pre-rollback mechanism ([#10307](https://github.com/velero-io/velero/pull/10307)): use a VolumeSnapshotClass with `Retain`, delete the VolumeSnapshot, confirm the VolumeSnapshotContent and underlying RBD snapshot remain, and that `GetChangedBlocksByID(retainedHandle, newSnap)` still succeeds. This is not 1.19.0 behavior; keep it only as a driver-level check or rework it once the new design provides a retention parameter. |
| One retained snapshot per volume chain (pending design) | Validates the proposed rotation: after several block data mover backups, the storage holds one retained snapshot per chain; deleting the latest backup leaves no retained snapshot and the next backup falls back once. Depends on [#10744](https://github.com/velero-io/velero/issues/10744); do not hard-code the parameter name. |
| Leaked `csi-snap-*` images after backup deletion (**pre-rollback builds**) | Builds between #10307 and the rollback retained snapshots with no tracking, so RBD snapshots and `csi-snap-*` images accumulated ([#9835](https://github.com/velero-io/velero/issues/9835), [#10304](https://github.com/velero-io/velero/issues/10304)). Not applicable to 1.19.0, which retains nothing. |
| In-place incremental restore | The design ([#10014](https://github.com/velero-io/velero/pull/10014)) restores incremental data onto an existing volume. A test could restore a delta onto the original PVC using CBT-reported blocks and verify block hashes. Depends on Velero implementation status. |
| Snapshot-count pressure | Create snapshots up to `maxSnapshotsOnImage` with retention enabled and observe when the chain breaks (needs a long-running cluster test). |

---

## Ceph RBD Retention Requirements Summary

**Velero 1.19.0 retains nothing for data movement**, so a Ceph RBD block data mover
backup falls back to allocated blocks or full whenever the base snapshot is gone. There
is no supported configuration in 1.19.0 that restores incrementality. Case 2 retention
is proposed for a 1.19.x patch in
[#10744](https://github.com/velero-io/velero/issues/10744).

The YAML below is the **pre-rollback** mechanism, kept for reproducing historical
results and for driver-level testing. Velero 1.19.0 ignores it for data movement:

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
`config/snapshotclass-rbd.yaml` for the base manifest.) The VolumePolicy below selects
the data mover; the retention parameter in the pending design would be added to the same
`snapshot` action's `parameters`:

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
   cleanup while retaining the underlying RBD snapshot; this is what the pre-rollback
   results used (the retention path is not covered by this suite; see candidates above)

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
- **Reclamation** is part of the pending design, not implemented in 1.19.0. The
  pre-rollback builds did not reclaim retained snapshots
  ([#10304](https://github.com/velero-io/velero/issues/10304) was closed as a duplicate
  of [#9835](https://github.com/velero-io/velero/issues/9835)), so leaked snapshots
  increased flatten pressure. The proposal keeps one snapshot per chain and deletes it
  with its backup ([#10691](https://github.com/velero-io/velero/pull/10691) is the
  draft candidate).
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
