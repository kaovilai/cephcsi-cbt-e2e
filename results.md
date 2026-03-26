# E2E Test Results

**Date**: 2026-03-26
**Cluster**: mig-tkaovila-mar26 (OCP 4.21.7, K8s v1.34.5, Ceph 20.1.0 Tentacle)
**Start**: 5:51 PM EST | **End**: 6:08 PM EST | **Duration**: 17m 8s (1028s)

## Summary

| | Count |
|---|---|
| **Passed** | 24 |
| **Failed** | 5 |
| **Skipped** | 19 |
| **Total** | 48 |

All 5 failures share the same root cause: the external-snapshot-metadata sidecar treats the CSI snapshot handle passed to `GetMetadataDelta` as a VolumeSnapshot name lookup, which fails with "not found". This is a **sidecar bug**, not a CephCSI or test bug. `GetMetadataAllocated` (single snapshot) works fine; only handle-based delta (`GetChangedBlocksByID`) is affected.

## Detailed Results

### Volume Resize (1 passed, 1 failed, 1 skipped)

| Test | Status | Duration |
|---|---|---|
| should report updated VolumeCapacityBytes after expansion | PASSED | 61.2s |
| should include blocks in expanded region in delta | FAILED | 0.2s |
| should return correct allocated blocks for expanded volume | SKIPPED | - |

### ROX PVC (4 passed)

| Test | Status | Duration |
|---|---|---|
| should create a ROX PVC from snapshot in Bound state with ReadOnlyMany | PASSED | 22.9s |
| should mount ROX PVC read-only with correct data | PASSED | 7.4s |
| should not flatten ROX PVC despite multiple ROX PVCs from same snapshot | PASSED | 5.4s |
| should support PVC-PVC clone from ROX PVC | PASSED | 5.1s |

### Flattening Prevention (4 passed)

| Test | Status | Duration |
|---|---|---|
| PVC->Snap->Restore->Snap: should NOT flatten restored PVC | PASSED | 36.4s |
| PVC->Snap->Restore->Snap: should have CBT working across the chain | PASSED | 0.5s |
| PVC->PVC clone->Snap: should NOT flatten cloned PVC | PASSED | 40.6s |
| PVC->PVC clone->Snap: should have CBT working on clone's snapshot | PASSED | 0.3s |

### Backup Workflow (1 passed, 1 failed, 4 skipped)

| Test | Status | Duration |
|---|---|---|
| should perform full backup via GetMetadataAllocated | PASSED | 85.6s |
| should perform first incremental backup via GetMetadataDelta | FAILED | 0.1s |
| should perform second incremental backup (chained) | SKIPPED | - |
| should restore from chain and verify data integrity | SKIPPED | - |
| should report accurate metadata matching all written data | SKIPPED | - |
| should support backup workflow with ROX PVCs as read source | SKIPPED | - |

### Block Metadata Properties (0 passed, 1 failed, 7 skipped)

| Test | Status | Duration |
|---|---|---|
| should return blocks in ascending order by ByteOffset | FAILED (BeforeAll) | 95.3s |
| should return non-overlapping block ranges | SKIPPED | - |
| should report consistent VolumeCapacityBytes across calls | SKIPPED | - |
| should return 1MiB-aligned block offsets and sizes | SKIPPED | - |
| should report FIXED_LENGTH BlockMetadataType | SKIPPED | - |
| should support StartingOffset for resumption | SKIPPED | - |
| should honor MaxResults parameter without error | SKIPPED | - |
| should handle volume not aligned to 1MB block size | SKIPPED | - |

### Error Handling (8 passed)

| Test | Status | Duration |
|---|---|---|
| should return error for CBT on non-existent snapshot | PASSED | 0.0s |
| should return error for GetMetadataDelta across different PVCs | PASSED | 45.3s |
| should return error for reversed snapshot order in GetMetadataDelta | PASSED | 31.2s |
| should handle concurrent snapshot creation and CBT operations | PASSED | 17.9s |
| should handle large volume with many blocks | PASSED | 165.0s |
| Error Compliance: should return error for invalid snapshot handle | PASSED | 23.2s |
| Error Compliance: should return error for handle from different volume | PASSED | 50.9s |
| Error Compliance: should return error when querying not-ready snapshot | PASSED | 28.1s |

### Stored Diffs (1 passed, 1 failed, 4 skipped)

| Test | Status | Duration |
|---|---|---|
| should have intact parent chains on intermediate images | PASSED | 41.3s |
| should have CBT working on all snapshots with intact chains | FAILED | 0.9s |
| should force-flatten all intermediate images via rbd flatten | SKIPPED | - |
| should have no stored diffs in omap | SKIPPED | - |
| should fail GetMetadataAllocated after flattening without stored diffs | SKIPPED | - |
| should fail GetMetadataDelta after flattening without stored diffs | SKIPPED | - |

### Volume Mode Rebind (3 passed)

| Test | Status | Duration |
|---|---|---|
| should return allocated blocks for a Filesystem-mode snapshot via CBT | PASSED | 48.1s |
| should restore Filesystem snapshot to Block PVC and read block data via CBT | PASSED | 23.2s |
| should rebind volume as Filesystem and retain original file data | PASSED | 25.4s |

### Counter-based Deletion (2 passed)

| Test | Status | Duration |
|---|---|---|
| should defer snapshot deletion while ROX PVCs exist | PASSED | 48.0s |
| should complete deletion after all ROX PVCs are removed | PASSED | 15.1s |

### Velero Compliance (0 passed, 1 failed, 3 skipped)

| Test | Status | Duration |
|---|---|---|
| should return changed blocks using snapshot handle ID (GetChangedBlocksByID) | FAILED | 45.9s |
| should fail delta when parent snapshot is deleted (Case 1) | SKIPPED | - |
| should return consistent results between name-based and handle-based delta | SKIPPED | - |
| should simulate Velero incremental backup chain with handle-based delta | SKIPPED | - |

## Failure Analysis

**Root Cause**: All 5 failures are caused by the same sidecar bug in `external-snapshot-metadata`. When `GetMetadataDelta` receives a CSI snapshot handle (e.g., `0001-0011-openshift-storage-...-<snapID>`) as the base snapshot identifier, the sidecar incorrectly tries to look it up as a VolumeSnapshot name:

```
failed to get VolumeSnapshot 'cbt-e2e-test/<handle>': volumesnapshots.snapshot.storage.k8s.io "<handle>" not found
```

The sidecar should resolve the handle to the corresponding VolumeSnapshot via its VolumeSnapshotContent, but instead does a direct name-based lookup which fails.

**Impact**: `GetMetadataAllocated` (single snapshot, name-based) works perfectly. Only `GetMetadataDelta` with handle-based base snapshot identification (`GetChangedBlocksByID`) is broken. This blocks Velero's incremental backup workflow which relies on handle-based delta computation.

**Workaround**: Tests using name-based `GetMetadataDelta` (e.g., `GetChangedBlocks(ctx, snap1Name, snap2Name)`) would work if CephCSI supported it, but CephCSI's snap-clone architecture means snapshots from the same PVC live on different intermediate images, so name-based delta also fails for a different reason ("different volume" error).
