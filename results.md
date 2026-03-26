# E2E Test Results

**Date**: 2026-03-26
**Cluster**: mig-tkaovila-mar26 (OCP 4.21.7, K8s v1.34.5, Ceph 20.1.0 Tentacle)
**Start**: 6:39 PM EST | **End**: 6:59 PM EST | **Duration**: 20m 19s (1219s)
**Sidecar**: `registry.k8s.io/sig-storage/csi-snapshot-metadata:v0.2.0` (upstream, overriding ODF 4.21)

## Summary

| | Count |
|---|---|
| **Passed** | 48 |
| **Failed** | 0 |
| **Skipped** | 0 |
| **Total** | 48 |

All 48 tests pass with the upstream sidecar image. The ODF-shipped sidecar (`ose-csi-external-snapshot-metadata-rhel9`) broke all `GetMetadataDelta` calls — see [Sidecar Workaround](#odf--421-sidecar-workaround) below.

## Detailed Results

### Volume Resize (3 passed)

| Test | Status | Duration |
|---|---|---|
| should report updated VolumeCapacityBytes after expansion | PASSED | 71.4s |
| should include blocks in expanded region in delta | PASSED | 0.3s |
| should return correct allocated blocks for expanded volume | PASSED | 0.3s |

### ROX PVC (4 passed)

| Test | Status | Duration |
|---|---|---|
| should create a ROX PVC from snapshot in Bound state with ReadOnlyMany | PASSED | 28.1s |
| should mount ROX PVC read-only with correct data | PASSED | 7.5s |
| should not flatten ROX PVC despite multiple ROX PVCs from same snapshot | PASSED | 5.4s |
| should support PVC-PVC clone from ROX PVC | PASSED | 5.1s |

### Flattening Prevention (4 passed)

| Test | Status | Duration |
|---|---|---|
| PVC->Snap->Restore->Snap: should NOT flatten restored PVC | PASSED | 41.3s |
| PVC->Snap->Restore->Snap: should have CBT working across the chain | PASSED | 0.5s |
| PVC->PVC clone->Snap: should NOT flatten cloned PVC | PASSED | 50.9s |
| PVC->PVC clone->Snap: should have CBT working on clone's snapshot | PASSED | 0.4s |

### Backup Workflow (6 passed)

| Test | Status | Duration |
|---|---|---|
| should perform full backup via GetMetadataAllocated | PASSED | 80.5s |
| should perform first incremental backup via GetMetadataDelta | PASSED | 0.4s |
| should perform second incremental backup (chained) | PASSED | 0.3s |
| should restore from chain and verify data integrity | PASSED | 27.0s |
| should report accurate metadata matching all written data | PASSED | 0.3s |
| should support backup workflow with ROX PVCs as read source | PASSED | 27.3s |

### Block Metadata Properties (8 passed)

| Test | Status | Duration |
|---|---|---|
| should return blocks in ascending order by ByteOffset | PASSED | 80.3s |
| should return non-overlapping block ranges | PASSED | 0.0s |
| should report consistent VolumeCapacityBytes across calls | PASSED | 0.0s |
| should return 1MiB-aligned block offsets and sizes | PASSED | 0.0s |
| should report FIXED_LENGTH BlockMetadataType | PASSED | 0.0s |
| should support StartingOffset for resumption | PASSED | 0.2s |
| should honor MaxResults parameter without error | PASSED | 0.2s |
| should handle volume not aligned to 1MB block size | PASSED | 28.3s |

### Error Handling (8 passed)

| Test | Status | Duration |
|---|---|---|
| should return error for CBT on non-existent snapshot | PASSED | 0.0s |
| should return error for GetMetadataDelta across different PVCs | PASSED | 51.3s |
| should return error for reversed snapshot order in GetMetadataDelta | PASSED | 36.0s |
| should handle concurrent snapshot creation and CBT operations | PASSED | 23.2s |
| should handle large volume with many blocks | PASSED | 165.1s |
| Error Compliance: should return error for invalid snapshot handle | PASSED | 28.1s |
| Error Compliance: should return error for handle from different volume | PASSED | 51.4s |
| Error Compliance: should return error when querying not-ready snapshot | PASSED | 23.1s |

### Stored Diffs (6 passed)

| Test | Status | Duration |
|---|---|---|
| should have intact parent chains on intermediate images | PASSED | 45.6s |
| should have CBT working on all snapshots with intact chains | PASSED | 1.3s |
| should force-flatten all intermediate images via rbd flatten | PASSED | 4.9s |
| should have no stored diffs in omap | PASSED | 1.1s |
| should fail GetMetadataAllocated after flattening without stored diffs | PASSED | 0.6s |
| should fail GetMetadataDelta after flattening without stored diffs | PASSED | 0.9s |

### Volume Mode Rebind (3 passed)

| Test | Status | Duration |
|---|---|---|
| should return allocated blocks for a Filesystem-mode snapshot via CBT | PASSED | 58.1s |
| should restore Filesystem snapshot to Block PVC and read block data via CBT | PASSED | 18.4s |
| should rebind volume as Filesystem and retain original file data | PASSED | 30.5s |

### Counter-based Deletion (2 passed)

| Test | Status | Duration |
|---|---|---|
| should defer snapshot deletion while ROX PVCs exist | PASSED | 53.1s |
| should complete deletion after all ROX PVCs are removed | PASSED | 15.1s |

### Velero Compliance (4 passed)

| Test | Status | Duration |
|---|---|---|
| should return changed blocks using snapshot handle ID (GetChangedBlocksByID) | PASSED | 45.8s |
| should fail delta when parent snapshot is deleted (Case 1) | PASSED | 35.7s |
| should return consistent results between name-based and handle-based delta | PASSED | 0.7s |
| should simulate Velero incremental backup chain with handle-based delta | PASSED | 30.1s |

## ODF <= 4.21 Sidecar Workaround

The first test run (with ODF's shipped sidecar) had 5 failures and 19 skipped tests. All failures were caused by the ODF-shipped sidecar image (`ose-csi-external-snapshot-metadata-rhel9`) using `BaseSnapshotName` (name-based VolumeSnapshot lookup) instead of `BaseSnapshotId` (CSI handle passthrough) in `GetMetadataDelta`.

**Fix**: [red-hat-storage/ceph-csi-operator commit 454e9130](https://github.com/red-hat-storage/ceph-csi-operator/commit/454e9130a39057b5a9ca98785df605ada195d62e) — expected in ODF 4.22+.

**Workaround**: Override the sidecar image to upstream `registry.k8s.io/sig-storage/csi-snapshot-metadata:v0.2.0` via driver-level ImageSet ConfigMap. Automated in `ocp-setup/step4-cbt-sidecar.sh` step 4j with ODF version detection (only applies on ODF <= 4.21).

After applying the override, all 48 tests pass — `GetMetadataAllocated`, `GetMetadataDelta`, backup workflows, Velero compliance, volume resize, flattening, stored diffs, error handling, and volume mode rebind all work correctly.
