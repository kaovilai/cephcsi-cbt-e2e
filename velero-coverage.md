# Velero PR #9528 - E2E Test Coverage Mapping

This document maps the Velero Block Data Mover design (PR #9528) requirements —
specifically the **Volume Snapshot retention** section — to E2E tests in this suite
that validate Ceph RBD CBT compliance.

Reference: [Velero PR #9528](https://github.com/vmware-tanzu/velero/pull/9528) |
[velero-feedback.md](./velero-feedback.md)

---

## Velero Design: Two Cases for Snapshot Retention

PR #9528 defines two cases based on storage backend capabilities:

| Case | Behavior | Ceph RBD | Config |
|------|----------|----------|--------|
| **Case 1** (Default) | Delete snapshot after backup; storage can compute deltas without parent snapshot | Not natively supported | `retainSnapshot: "false"` (default) |
| **Case 2** (Opt-in) | Retain snapshot with `deletionPolicy: Retain`; parent must exist for delta | **Required for Ceph RBD** | `retainSnapshot: "true"` |

Ceph RBD requires **Case 2** because `rbd snap diff` needs both snapshots in the
same clone chain. The CephCSI Combined solution (stored diffs in omap) can
partially enable Case 1 behavior as a fallback.

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

### Fallback-to-Full on Flattening (Stored Diffs)

| Velero Requirement | Test File | Lines | Test Description | What's Validated |
|----|----|----|----|----|
| Stored diffs in omap after flattening | `stored_diffs_test.go` | 90-108 | "should have stored diffs in omap when a snapshot is flattened" | Omap keys exist for flattened image via `ListOmapKeys()` |
| `GetMetadataAllocated` works on flattened snaps | `stored_diffs_test.go` | 110-118 | "should return correct data via GetMetadataAllocated on all snapshots" | Allocated blocks returned even for flattened snapshots |
| `GetMetadataDelta` works despite flattening | `stored_diffs_test.go` | 120-146 | "should return correct delta between snapshots regardless of flattening state" | Deltas snap1->snap2, snap2->snap3, and cumulative snap1->snap3 all correct |
| Oldest snapshot = full allocated blocks | `stored_diffs_test.go` | 148-155 | "should handle oldest snapshot's diff as full allocated blocks" | No previous snapshot to diff against; stored as full allocation |

### 250-Snapshot Limit and Priority Flattening

| Velero Requirement | Test File | Lines | Test Description | What's Validated |
|----|----|----|----|----|
| Snapshot count monitoring | `priority_flattening_test.go` | 89-102 | "should report snapshot count approaching limit" | `GetSnapshotCount()` tracks accumulation toward 250 limit |
| Deleted snapshots flattened first | `priority_flattening_test.go` | 104-120 | "should prioritize flattening deleted snapshots first" | After deleting early snaps, latest snap still has working CBT |
| Recent snapshots preserved for CBT | `priority_flattening_test.go` | 122-131 | "should preserve latest snapshots for CBT even after flattening" | Delta between recent snaps works after older ones flattened |

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
- **Priority flattening** order: deleted > PVC-PVC clone > alive snapshots
- **Stored diffs in omap** provide fallback when clone chain breaks
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
# All retention-relevant tests (excludes slow priority flattening)
make e2e-fast

# Specific test suites
make e2e-basic          # Basic CBT (30m)
make e2e-rox-deletion   # Counter-based deletion tests (30m)
make e2e-flattening     # Flattening prevention tests (30m)
make e2e-stored-diffs   # Stored diffs fallback (1h)
make e2e-priority       # Priority flattening - slow (3h)
make e2e-backup         # Backup workflow tests (1h)

# Single test by description
go run github.com/onsi/ginkgo/v2/ginkgo -v \
  --focus='should support delta via handle after VolumeSnapshot object is deleted' \
  ./tests/e2e/... -- \
  --storage-class=ocs-storagecluster-ceph-rbd \
  --snapshot-class=ocs-storagecluster-rbdplugin-snapclass
```
