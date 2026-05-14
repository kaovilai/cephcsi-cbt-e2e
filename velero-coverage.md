# Velero PR #9528 - E2E Test Coverage Mapping

This document maps the Velero Block Data Mover design (PR #9528) requirements —
specifically the **Volume Snapshot retention** section — to E2E tests in this suite
that validate Ceph RBD CBT compliance.

Reference: [Velero PR #9528](https://github.com/velero-io/velero/pull/9528) |
[velero-feedback.md](./velero-feedback.md)

---

## Velero Design: Two Cases for Snapshot Retention

PR #9528 defines two cases based on storage backend capabilities:

| Case | Behavior | Ceph RBD | Config |
|------|----------|----------|--------|
| **Case 1** (Default) | Delete snapshot after backup; storage can compute deltas without parent snapshot | Not natively supported | `retainSnapshot: "false"` (default) |
| **Case 2** (Opt-in) | Retain snapshot with `deletionPolicy: Retain`; parent must exist for delta | **Required for Ceph RBD** | `retainSnapshot: "true"` |

Ceph RBD requires **Case 2** because `rbd snap diff` needs both snapshots in the
same clone chain. A stored-diffs fallback in CephCSI is still a proposal (not
implemented), so Case 1 remains unsupported for Ceph today (tracking:
[ceph/ceph-csi#1800](https://github.com/ceph/ceph-csi/issues/1800),
[ceph/ceph-csi#5346](https://github.com/ceph/ceph-csi/issues/5346)).

> "There is no way to automatically detect which way a specific volume supports"
> — PR #9528 design document

---

## Test-to-Design Mapping

### Snapshot Retention (Case 2 Validation)

| Velero Requirement | Test File | Lines | Test Description | What's Validated |
|----|----|----|----|----|
| `RetainSnapshot: true` needed for Ceph | `velero_compliance_test.go` | 135-198 | "should support delta via handle after VolumeSnapshot object is deleted" | Deletes VolumeSnapshot K8s object, attempts handle-based delta. Classifies result as Type 1 (needs retention, error) or Type 2 (handles work after deletion). |
| ChangeId = CSI snapshot handle | `velero_compliance_test.go` | 70-73, 115-133 | "should return changed blocks using snapshot handle ID" | `GetChangedBlocksByID(snap1Handle, snap2Name)` returns correct delta using CSI handle format `<clusterID>-<pool>-<imageID>-<snapID>` |
| Handle-based matches name-based | `velero_compliance_test.go` | 201-228 | "should return consistent results between name-based and handle-based delta" | Byte-for-byte comparison of `GetChangedBlocks` vs `GetChangedBlocksByID` |
| Deferred deletion with references | `counter_deletion_test.go` | 82-98 | "should defer snapshot deletion while ROX PVCs exist" | Snapshot deletion blocked while ROX PVCs hold references (counter-based) |
| Deletion completes after cleanup | `counter_deletion_test.go` | 100-118 | "should complete deletion after all ROX PVCs are removed" | Counter 3->2->1->0, then snapshot fully deleted |

### Incremental Backup Chain

| Velero Requirement | Test File | Lines | Test Description | What's Validated |
|----|----|----|----|----|
| Full backup via `GetMetadataAllocated` | `backup_workflow_test.go` | 128-142 | "should perform full backup via GetMetadataAllocated" | All written blocks reported as allocated |
| Incremental via `GetMetadataDelta` | `backup_workflow_test.go` | 144-164 | "should perform first incremental backup via GetMetadataDelta" | Only changed blocks (1,3) in delta; unchanged blocks (0,2) excluded |
| Chained incrementals (full->incr1->incr2) | `backup_workflow_test.go` | 166-181 | "should perform second incremental backup" | Second delta captures only its changes (blocks 2,4) |
| Restore + data integrity verification | `backup_workflow_test.go` | 183-218 | "should restore from chain and verify data integrity" | SHA256 verification of all 5 blocks after restore from snap3 |
| Full Velero workflow simulation | `velero_compliance_test.go` | 230-313 | "should simulate Velero incremental backup chain with handle-based delta" | Full->Incr1->Incr2 using handle-based API, then restore + verify |
| ROX PVC as backup data source | `backup_workflow_test.go` | 221-256 | "should support backup workflow with ROX PVCs as read source" | ROX PVC reads match snap3 state; CBT works while ROX active |

### Fallback-to-Full on Flattening (Current State)

| Velero Requirement | Test File | Lines | Test Description | What's Validated |
|----|----|----|----|----|
| No stored diffs present after manual flatten | `stored_diffs_test.go` | 177-188 | "should have no stored diffs in omap (manual flatten bypasses CephCSI)" | No usable CephCSI-stored diffs are found in omap after manual flatten |
| `GetMetadataAllocated` after flatten | `stored_diffs_test.go` | 190-201 | "should fail GetMetadataAllocated after flattening without stored diffs" | Allocated metadata calls fail once chain/snapshot references are broken |
| `GetMetadataDelta` after flatten | `stored_diffs_test.go` | 203-219 | "should fail GetMetadataDelta after flattening without stored diffs" | Delta calls fail for flattened pairs without stored diffs fallback |
| Velero handling path | [PR #9736](https://github.com/velero-io/velero/pull/9736) | N/A | Bitmap fallback (`bitmap.SetFull()`) | Fallback-to-full implementation is **in progress** in Velero (open PR) |

### 250-Snapshot Limit and Priority Flattening

| Velero Requirement | Tracking Source | Current Status |
|----|----|----|
| Snapshot count pressure handling | [ceph/ceph-csi design](https://github.com/ceph/ceph-csi/blob/devel/docs/design/proposals/rbd-snap-clone.md) | Applies via CephCSI thresholds (`maxSnapshotsOnImage`, `minSnapshotsOnImage`) |
| Priority flattening (deleted first) | [ceph/ceph-csi#1800](https://github.com/ceph/ceph-csi/issues/1800) | **Not implemented** in CephCSI today |
| Preserve recent snapshots for CBT under flatten pressure | [velero-io/velero#9556](https://github.com/velero-io/velero/issues/9556) | **In progress** as part of overall BDM CBT work |

### Clone Chain Preservation

| Velero Requirement | Test File | Lines | Test Description | What's Validated |
|----|----|----|----|----|
| PVC->Snap->Restore->Snap chain intact | `flattening_prevention_test.go` | 107-121 | "should NOT flatten snap1 after restore and re-snapshot" | `IsImageFlattened()` returns false; parent chain intact |
| CBT works across restore chain | `flattening_prevention_test.go` | 123-133 | "should have CBT working across the chain" | `GetAllocatedBlocks` succeeds on both snap1 and snap2 |
| PVC->Clone->Snap chain intact | `flattening_prevention_test.go` | 216-229 | "should NOT flatten original PVC after clone and snapshot" | Original image not flattened after PVC clone + snapshot |
| ROX PVCs don't trigger flattening | `rox_pvc_test.go` | 113-135 | "should not flatten parent snapshot despite multiple ROX PVCs" | Multiple ROX PVCs preserve parent chain |

### Error Handling and Edge Cases

| Velero Requirement | Test File | Lines | Test Description | What's Validated |
|----|----|----|----|----|
| Error on non-existent snapshot | `error_handling_test.go` | 24-28 | "should return error for CBT on non-existent snapshot" | Clear error when snapshot doesn't exist |
| Error on cross-PVC delta | `error_handling_test.go` | 30-92 | "should return error for GetMetadataDelta across different PVCs" | Snapshots must be from same volume for delta |
| Error on reversed snapshot order | `error_handling_test.go` | 94-137 | "should return error for reversed snapshot order" | Temporal ordering enforced |
| Concurrent CBT operations | `error_handling_test.go` | 139-187 | "should handle concurrent snapshot creation and CBT operations" | 5 concurrent `GetAllocatedBlocks` calls succeed |

---

## Ceph RBD Retention Requirements Summary

For Velero's Volume Policy configuration with Ceph RBD:

```yaml
volumePolicies:
  - conditions:
      csi:
        driver: openshift-storage.rbd.csi.ceph.com
    action:
      type: block-data-mover
      parameters:
        retainSnapshot: "true"
```

### Why Retention is Required

1. **`rbd snap diff` needs both snapshots** in the same clone chain
2. **Deleting the base snapshot** breaks all future incremental backups
3. **Without retention**, every backup after the first falls back to full
4. **Handle-based deltas** (`GetChangedBlocksByID`) allow K8s VolumeSnapshot
   object cleanup while retaining the underlying RBD snapshot

### Retention Constraints

- **250-snapshot hard limit** per RBD image enforced by CephCSI
- **Priority flattening** is a desired behavior but not implemented (tracked in [ceph/ceph-csi#1800](https://github.com/ceph/ceph-csi/issues/1800))
- **Stored diffs in omap** fallback is not implemented in CephCSI today; Velero fallback-to-full is in progress in [velero-io/velero#9736](https://github.com/velero-io/velero/pull/9736)
- **Velero GC** must clean up `VolumeSnapshotContent` with `DeletionPolicy: Retain`
  when backup chain is pruned

### Fallback Strategy

When `GetMetadataDelta` returns gRPC error (`FAILED_PRECONDITION`, `INVALID_ARGUMENT`):
1. Fall back to `GetMetadataAllocated` (full backup)
2. Log the fallback for operator investigation
3. New snapshot becomes base for future incrementals — chain re-established

---

## Basic CBT Validation (Foundational)

| Test File | Lines | Test Description |
|----|----|----|
| `basic_cbt_test.go` | 93-102 | `GetMetadataAllocated` returns allocated blocks for single snapshot |
| `basic_cbt_test.go` | 104-117 | `GetMetadataDelta` returns changed blocks between consecutive snapshots |
| `basic_cbt_test.go` | 119-131 | Cumulative changes between non-consecutive snapshots |
| `basic_cbt_test.go` | 133-143 | Metadata accuracy matches written data |

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
  --focus='should support delta via handle after VolumeSnapshot object is deleted' \
  ./tests/e2e/... -- \
  --storage-class=ocs-storagecluster-ceph-rbd \
  --snapshot-class=ocs-storagecluster-rbdplugin-snapclass
```
