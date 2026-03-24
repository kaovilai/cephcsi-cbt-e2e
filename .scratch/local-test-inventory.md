# Local E2E Test Inventory - CBT Project

## Project Overview

- **Location**: `/Users/tkaovila/experiments/cephcsi-cbt-e2e/tests/e2e/`
- **Framework**: Ginkgo v2 (BDD) with Gomega matchers
- **Test Runner**: `go test` or `ginkgo` CLI
- **Total Test Files**: 12
- **Estimated Total Tests**: 50+ test cases across 12 suites

## BeforeSuite Prerequisites (e2e_suite_test.go:88-219)

Cluster preconditions validated before any test runs:

- **Kubernetes >= 1.33**: Required for SnapshotMetadata alpha API (KEP-3314)
- **VolumeSnapshot CRDs**: External-snapshotter must be installed
- **SnapshotMetadataService CRD**: Remains at `v1alpha1` (out-of-tree, not graduating with K8s beta)
- **CephCSI RBD provisioner pods**: Discovered via label selectors with ODF version fallback
  - ODF 4.18+: `app.kubernetes.io/name=csi-rbdplugin,app.kubernetes.io/component=ctrlplugin`
  - ODF < 4.18: `app=csi-rbdplugin-provisioner`
  - ODF 4.21+: Pod name pattern matching (contains "rbd" + "ctrlplugin")
- **Snapshot-metadata sidecar**: Container `csi-snapshot-metadata` in RBD provisioner pod (warns if absent)
- **StorageClass exists**: Default `ocs-storagecluster-ceph-rbd`
- **VolumeSnapshotClass exists**: Default `ocs-storagecluster-rbdplugin-snapclass`
- **Ceph >= 17 (Quincy)**: Required for `rbd snap diff` support
- **Test namespace creation**: `cbt-e2e-test` with pod-security labels (privileged)
- **ServiceAccount creation**: `cbt-e2e-client` with RBAC binding to `external-snapshot-metadata-client-runner`
- **CBT client initialization**: Uses in-cluster or kubeconfig authentication

## AfterSuite Cleanup (e2e_suite_test.go:221-234)

- Deletes ClusterRoleBinding `cbt-e2e-client-binding`
- Deletes test namespace with 5-minute wait timeout

---

## Test Suites

### 1. Basic CBT (basic_cbt_test.go)

**Container**: `Describe("Basic CBT", Ordered)` (lines 16-144)

Tests core CBT API functionality with single-PVC workflow.

#### Setup (BeforeAll, lines 26-81)
- Create 1Gi RWO PVC
- Create pod with block volume
- Write block 0 (0xAA), create snapshot 1
- Write block 1 (0xBB), create snapshot 2
- Write block 2 (0xCC), create snapshot 3
- Delete pod

#### Cleanup (AfterAll, lines 83-91)
- Delete pod, snapshots 3→1, PVC

#### Test Cases

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 93-102 | should return allocated blocks for a single snapshot via GetMetadataAllocated | VolumeSnapshot, external-snapshot-metadata gRPC | Non-existent snapshot | N/A | GetMetadataAllocated streaming | Service account token | N/A |
| 104-117 | should return changed blocks between consecutive snapshots via GetMetadataDelta | VolumeSnapshot, GetMetadataDelta gRPC | Mismatched snapshots | N/A | GetMetadataDelta streaming | Service account token | N/A |
| 119-131 | should return cumulative changes between non-consecutive snapshots | VolumeSnapshot chain | N/A | N/A | GetMetadataDelta | Service account token | N/A |
| 133-143 | should report accurate metadata matching written data | VolumeSnapshot, allocated block validation | N/A | N/A | GetMetadataAllocated | Service account token | N/A |

**Data Verification**: Validates block offsets via `ContainsOffset()` helper

**Tag**: None (runs in all test suites)

---

### 2. Counter-based Deletion (counter_deletion_test.go)

**Container**: `Describe("Counter-based Deletion", Ordered)` (lines 17-119)

Tests snapshot reference counting via ReadOnlyMany PVCs (deferred deletion mechanism).

#### Setup (BeforeAll, lines 27-71)
- Create 1Gi PVC, pod, write data, create snapshot
- Create 3 ROX PVCs cloned from snapshot (reference counting)

#### Cleanup (AfterAll, lines 73-80)
- Delete all ROX PVCs, snapshot, source PVC

#### Test Cases

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 82-98 | should defer snapshot deletion while ROX PVCs exist | VolumeSnapshot with finalizer, RefCount | Deletion race | N/A | N/A | Service account token | N/A |
| 100-118 | should complete deletion after all ROX PVCs are removed | RefCount decrement, async deletion | N/A | N/A | N/A | Service account token | N/A |

**Snapshot Lifecycle**: Tests deferred deletion via counter mechanism (CephCSI-specific implementation detail)

**Tag**: None

---

### 3. ROX PVC (rox_pvc_test.go)

**Container**: `Describe("ROX PVC", Ordered)` (lines 17-158)

Tests ReadOnlyMany access patterns for incremental backup use cases.

#### Setup (BeforeAll, lines 29-69)
- Create 1Gi RWO PVC, pod, write block 0, delete pod
- Create snapshot
- Create ROX PVC from snapshot

#### Cleanup (AfterAll, lines 71-79)
- Delete ROX pod, second ROX PVC, first ROX PVC, snapshot, source PVC

#### Test Cases

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 81-90 | should create a ROX PVC from snapshot in Bound state with ReadOnlyMany | VolumeSnapshot as data source, access mode | N/A | N/A | N/A | Service account token | N/A |
| 92-111 | should mount ROX PVC read-only with correct data | Pod mount, PersistentVolumeBlock, write-protection | Write should fail | N/A | N/A | Service account token | N/A |
| 113-135 | should not flatten parent snapshot despite multiple ROX PVCs | RBD parent/child chain inspection | N/A | N/A | GetMetadataAllocated | Service account token | N/A |
| 137-148 | should support PVC-PVC clone from ROX PVC | PVC clone from ROX source | N/A | N/A | N/A | Service account token | N/A |
| 150-157 | should have CBT working on snapshots with ROX PVC references | GetMetadataAllocated with active ROX PVC | N/A | N/A | GetMetadataAllocated | Service account token | N/A |

**Anti-Flattening**: Validates that ROX PVCs don't trigger unnecessary snapshot flattening

**Tag**: None

---

### 4. Flattening Prevention (flattening_prevention_test.go)

**Container**: Two contexts, Ordered

#### Context 1: PVC -> Snap -> Restore -> Snap chain (lines 18-134)

Tests parent/child chain preservation across restore operations.

**Setup (BeforeAll, lines 29-97)**:
- Create original PVC, write block 0 (0x11), snapshot 1
- Restore PVC from snapshot 1
- Write block 1 (0x22) to restored PVC
- Create snapshot 2 from restored PVC

**Test Cases**:

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 107-121 | should NOT flatten snap1 after restore and re-snapshot | RBD parent chain, GetSnapshotMetadata | N/A | N/A | GetMetadataAllocated | Service account token | N/A |
| 123-133 | should have CBT working across the chain | GetMetadataAllocated on both snapshots | N/A | N/A | GetMetadataAllocated | Service account token | N/A |

#### Context 2: PVC -> PVC clone -> Snap (lines 136-236)

Tests clone operations don't flatten original.

**Setup (BeforeAll, lines 146-207)**:
- Create original PVC, write block 0 (0x33)
- Create PVC-PVC clone, write block 1 (0x44)
- Create snapshot of clone

**Test Cases**:

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 216-229 | should NOT flatten original PVC after clone and snapshot | RBD parent chain, GetSnapshotMetadata | N/A | N/A | GetMetadataAllocated | Service account token | N/A |
| 231-235 | should have CBT working on the clone's snapshot | GetMetadataAllocated on clone | N/A | N/A | GetMetadataAllocated | Service account token | N/A |

**Tag**: None

---

### 5. Priority Flattening (priority_flattening_test.go)

**Container**: `Describe("Priority Flattening", Label("slow"), Ordered)` (lines 23-132)

Tests priority-based snapshot flattening when approaching 250-snapshot limit.

#### Setup (BeforeAll, lines 37-79)
- Create 1Gi PVC, pod
- Create 15 snapshots (testSnapshotLimit=10 + 5 extra)
- Write unique block per snapshot to trigger changes
- Delete pod

#### Cleanup (AfterAll, lines 81-87)
- Delete all snapshots, PVC

#### Test Cases

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 89-102 | should report snapshot count approaching limit | RBD snapshot enumeration | N/A | N/A | N/A | Service account token | N/A |
| 104-120 | should prioritize flattening deleted snapshots first | Deleted snapshot handling, GetMetadataAllocated | Stale metadata | N/A | GetMetadataAllocated | Service account token | N/A |
| 122-131 | should preserve latest snapshots for CBT even after flattening | Priority queue, GetMetadataDelta | Flattened snapshot delta | N/A | GetMetadataDelta | Service account token | N/A |

**Priority Order**: Deleted > PVC-PVC clone > User-visible snapshots

**Tag**: `slow` (excludes from `make e2e-fast`)

---

### 6. Stored Diffs (stored_diffs_test.go)

**Container**: `Describe("Stored Diffs", Label("stored-diffs"), Ordered)` (lines 20-156)

Tests Ceph omap-based fallback for CBT when snapshots are flattened.

#### Setup (BeforeAll, lines 30-80)
- Create 1Gi PVC, pod
- Write block 0 (0xA1), snapshot 1
- Write block 1 (0xB2), snapshot 2
- Write block 2 (0xC3), snapshot 3
- Delete pod

#### Cleanup (AfterAll, lines 82-88)
- Delete snapshots 3→1, PVC

#### Test Cases

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 90-108 | should have stored diffs in omap when a snapshot is flattened | Ceph omap, RBD introspection | Omap keys absent | N/A | N/A | Service account token | N/A |
| 110-118 | should return correct data via GetMetadataAllocated on all snapshots | GetMetadataAllocated fallback path | Omap reconstruction failure | N/A | GetMetadataAllocated | Service account token | N/A |
| 120-146 | should return correct delta between snapshots regardless of flattening state | GetMetadataDelta with omap-backed deltas | Omap chain broken | N/A | GetMetadataDelta | Service account token | N/A |
| 148-155 | should handle oldest snapshot's diff as full allocated blocks | GetMetadataAllocated (full diff) | N/A | N/A | GetMetadataAllocated | Service account token | N/A |

**Fallback Mechanism**: When snapshot is flattened, stored diffs in omap allow CBT to continue working

**Tag**: `stored-diffs` (excludes from `make e2e-fast`)

---

### 7. Error Handling (error_handling_test.go)

**Container**: `Describe("Error Handling")` (lines 17-230) - Non-ordered (independent tests)

Tests error paths and edge cases.

#### Test Cases (No shared setup/cleanup)

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 24-28 | should return error for CBT on non-existent snapshot | GetMetadataAllocated | Invalid snapshot name | N/A | GetMetadataAllocated error | Service account token | N/A |
| 30-92 | should return error for GetMetadataDelta across different PVCs | GetMetadataDelta | Cross-volume snapshot pair | N/A | GetMetadataDelta error | Service account token | N/A |
| 94-137 | should return error for reversed snapshot order in GetMetadataDelta | GetMetadataDelta | Invalid argument (base > target) | N/A | GetMetadataDelta error | Service account token | N/A |
| 139-187 | should handle concurrent snapshot creation and CBT operations | Concurrent GetAllocatedBlocks (5 goroutines) | Race condition recovery | N/A | GetMetadataAllocated (concurrent) | Service account token | N/A |
| 189-229 | should handle large volume with many blocks | Large 5Gi PVC, 50 blocks written | Streaming large result set | N/A | GetMetadataAllocated streaming | Service account token | N/A |

**Concurrency**: Tests concurrent gRPC calls without errors

**Large Volume**: Tests streaming completion with many blocks (pagination internally via gRPC)

**Tag**: None

---

### 8. Backup Workflow (backup_workflow_test.go)

**Container**: `Describe("Backup Workflow", Ordered)` (lines 21-257)

Simulates full Velero Block Data Mover workflow (KEP-3314 use case).

#### Setup (BeforeAll, lines 36-117)
- Create 1Gi PVC, pod
- Write blocks 0, 1, 2 (0x10, 0x11, 0x12) → snapshot 1 (full backup)
- Modify blocks 1, 3 → snapshot 2 (first incremental)
- Modify blocks 2, 4 → snapshot 3 (second incremental)
- Record hashes at each point

#### Cleanup (AfterAll, lines 120-126)
- Delete snapshots 3→1, PVC

#### Test Cases

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 128-142 | should perform full backup via GetMetadataAllocated | GetMetadataAllocated snapshot 1 | Snapshot not ready | N/A | GetMetadataAllocated | Service account token | N/A |
| 144-164 | should perform first incremental backup via GetMetadataDelta | GetMetadataDelta snap1 → snap2 | Mismatched snapshots | N/A | GetMetadataDelta | Service account token | N/A |
| 166-181 | should perform second incremental backup (chained full->incr1->incr2) | GetMetadataDelta snap2 → snap3 | N/A | N/A | GetMetadataDelta | Service account token | N/A |
| 183-219 | should restore from chain and verify data integrity | PVC from snap3, block hash comparison | Data corruption | N/A | N/A | Service account token | N/A |
| 221-256 | should support backup workflow with ROX PVCs as read source | ROX PVC from snap3, GetMetadataAllocated | ROX PVC data mismatch | N/A | GetMetadataAllocated | Service account token | N/A |

**Backup Workflow**: Full (GetAllocated) + Incremental (GetDelta) + Restore validation

**Tag**: None

---

### 9. Error Compliance (error_compliance_test.go)

**Container**: `Describe("Error Compliance")` (lines 15-175) - Non-ordered

Tests error handling per CSI spec for handle-based operations.

#### Test Cases

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 22-62 | should return error for invalid snapshot handle in GetChangedBlocksByID | GetChangedBlocksByID (name-based lookup) | Invalid snapshot handle | N/A | GetChangedBlocksByID error | Service account token | N/A |
| 64-131 | should return error for GetChangedBlocksByID with handle from different volume | Cross-volume snapshot handle | N/A | N/A | GetChangedBlocksByID error | Service account token | N/A |
| 133-174 | should return error when querying CBT on snapshot that is not ready | GetMetadataAllocated on not-ready snapshot | Race (not-ready snapshot) | N/A | GetMetadataAllocated error | Service account token | N/A |

**Handle Validation**: GetChangedBlocksByID requires valid CSI snapshot handle from same volume

**Tag**: None

---

### 10. Velero Compliance (velero_compliance_test.go)

**Container**: `Describe("Velero Compliance", Ordered)` (lines 19-314)

Tests handle-based delta operations that Velero Block Data Mover (PR #9528) actually uses.

#### Setup (BeforeAll, lines 33-104)
- Create 1Gi PVC, pod
- Write block 0 (0xAA), snapshot 1 → capture handle
- Write block 1 (0xBB), snapshot 2 → capture handle
- Write block 2 (0xCC), snapshot 3 → record hashes
- Delete pod

#### Cleanup (AfterAll, lines 107-113)
- Delete snapshots 3→1, PVC

#### Test Cases

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 115-133 | should return changed blocks using snapshot handle ID (GetChangedBlocksByID) | GetChangedBlocksByID(snap1Handle, snap2Name) | Invalid handle | N/A | GetChangedBlocksByID | Service account token | N/A |
| 135-199 | should support delta via handle after VolumeSnapshot object is deleted | GetChangedBlocksByID with deleted K8s object | Type 1 (requires retention) vs Type 2 (handle survives deletion) | N/A | GetChangedBlocksByID error or success | Service account token | N/A |
| 201-228 | should return consistent results between name-based and handle-based delta | GetChangedBlocks vs GetChangedBlocksByID equivalence | N/A | N/A | Both gRPC calls | Service account token | N/A |
| 230-313 | should simulate Velero incremental backup chain with handle-based delta | Full backup + Incr1 (snap1Handle) + Incr2 (snap2Handle) + Restore | Data integrity | N/A | GetMetadataAllocated + 2x GetChangedBlocksByID | Service account token | N/A |

**Velero Workflow**: Uses CSI handles (not VolumeSnapshot names) for durability across snapshot deletion

**Handle Types**: Tests both Type 1 (snapshot retention required) and Type 2 (handle survives deletion)

**Tag**: None

---

### 11. Volume Resize (volume_resize_test.go)

**Container**: `Describe("Volume Resize", Ordered)` (lines 16-137)

Tests CBT behavior on resized volumes.

#### Setup (BeforeAll, lines 26-94)
- Create 1Gi PVC, write block 0 (0xAA), snapshot 1
- Resize to 2Gi
- Write block 1024 in expanded region (0xBB), snapshot 2

#### Cleanup (AfterAll, lines 96-102)
- Delete snapshots 2→1, PVC

#### Test Cases

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 104-110 | should report updated VolumeCapacityBytes after expansion | GetMetadataAllocated snap2 | N/A | N/A | GetMetadataAllocated | Service account token | N/A |
| 112-123 | should include blocks in expanded region in delta | GetMetadataDelta snap1 → snap2 | Expanded region not reported | N/A | GetMetadataDelta | Service account token | N/A |
| 125-136 | should return correct allocated blocks for expanded volume | GetMetadataAllocated snap2 | N/A | N/A | GetMetadataAllocated | Service account token | N/A |

**Expansion Handling**: VolumeCapacityBytes updated, blocks in new region correctly reported

**Tag**: None

---

### 12. Block Metadata Properties (block_metadata_properties_test.go)

**Container**: `Describe("Block Metadata Properties", Ordered)` (lines 17-238)

Tests metadata result properties and pagination/streaming parameters.

#### Setup (BeforeAll, lines 28-102)
- Create 1Gi PVC, pod
- Write non-sequential blocks: 5 (0x55), 2 (0x22), 8 (0x88), 0 (0x00) → snapshot 1
- Write blocks 3 (0x33), 7 (0x77) → snapshot 2
- Pre-fetch allocated blocks for snap1 and delta

#### Cleanup (AfterAll, lines 104-109)
- Delete snapshots 2→1, PVC

#### Test Cases

| Line | Test | K8s/CSI API | Error Conditions | Security | gRPC | RBAC | Pagination |
|------|------|-------------|-----------------|----------|------|------|-----------|
| 111-117 | should return blocks in ascending order by ByteOffset | MetadataResult ordering | Unsorted blocks | N/A | GetMetadataAllocated, GetMetadataDelta | Service account token | N/A |
| 119-122 | should return non-overlapping block ranges | MetadataResult overlap detection | Overlapping ranges | N/A | GetMetadataAllocated, GetMetadataDelta | Service account token | N/A |
| 124-130 | should report consistent VolumeCapacityBytes across calls | VolumeCapacityBytes consistency | Inconsistent capacity | N/A | GetMetadataAllocated, GetMetadataDelta | Service account token | N/A |
| 132-140 | should return 1MiB-aligned block offsets and sizes | Alignment validation (DefaultBlockSize=1MiB) | Misaligned block | N/A | GetMetadataAllocated | Service account token | N/A |
| 142-145 | should report FIXED_LENGTH BlockMetadataType | BlockMetadataType constant | Wrong type | N/A | GetMetadataAllocated | Service account token | N/A |
| 147-171 | should support StartingOffset for resumption | GetAllocatedBlocksWithOptions(startingOffset) | Offset validation | N/A | GetMetadataAllocated streaming (partial) | Service account token | Pagination (StartingOffset) |
| 173-183 | should honor MaxResults parameter without error | GetAllocatedBlocksWithOptions(maxResults=1) | Incorrect batch size | N/A | GetMetadataAllocated streaming (batched) | Service account token | Pagination (MaxResults) |
| 185-237 | should handle volume not aligned to 1MB block size | 1500Mi PVC (unaligned) | Edge case handling | N/A | GetMetadataAllocated | Service account token | N/A |

**Pagination**: Supports StartingOffset and MaxResults for resumption in large result sets

**Alignment**: 1MiB (1048576 bytes) block size alignment enforced

**Tag**: None

---

## Support Packages

### pkg/cbt/cbt.go (226 lines)

**Purpose**: gRPC client wrapper for external-snapshot-metadata iterator API

**Key Types**:
- `Client`: Manages iterator authentication (ServiceAccount-based tokens)
- `MetadataResult`: Collects streaming blocks with helpers:
  - `ContainsOffset(offset)`: Checks if any block covers offset
  - `TotalChangedBytes()`: Sums block sizes
  - `BlocksAreSorted()`: Validates ascending ByteOffset order
  - `BlocksAreNonOverlapping()`: Validates no overlap

**Key Methods**:
- `GetAllocatedBlocks(ctx, snapshotName)`: GetMetadataAllocated (name-based)
- `GetChangedBlocks(ctx, prevSnapshotName, snapshotName)`: GetMetadataDelta (name-based)
- `GetChangedBlocksByID(ctx, prevSnapshotID, snapshotName)`: GetMetadataDelta (handle-based, Velero)
- `GetAllocatedBlocksWithOptions(ctx, snapshotName, startingOffset, maxResults)`: With pagination
- `GetChangedBlocksWithOptions(ctx, prevSnapshotName, snapshotName, startingOffset, maxResults)`: With pagination

**Authentication**: Uses external-snapshot-metadata's `iterator.BuildClients()` with ServiceAccount token

**gRPC Streaming**: Iterator collects all blocks via `SnapshotMetadataIteratorRecord()` emitter

### pkg/k8s/k8s.go (partial, 100+ lines)

**Purpose**: Kubernetes resource lifecycle helpers

**Key Functions**:
- `CreateNamespace()`: Sets pod-security labels (privileged) for block device access
- `DeleteNamespace()`: With wait timeout
- `CreatePVC()`: Supports snapshot/PVC clone sources
- `CreatePodWithPVC()`: Block/filesystem volumes, read-only mounting
- `CreateSnapshot()`: VolumeSnapshot wrapper
- `WaitForSnapshotReady()`, `WaitForSnapshotDeleted()`: Snapshot polling
- `WaitForPVCBound()`, `WaitForPVCResized()`: PVC state polling
- `CreateROXPVCFromSnapshot()`: ReadOnlyMany access mode
- `ExecInPod()`: Remote exec for data operations
- `GetSnapshotHandle()`: Extracts CSI snapshot handle from VolumeSnapshotContent
- `ResizePVC()`: Volume expansion via spec.resources.requests.storage
- `GetToolboxPod()`: Discovers Ceph toolbox pod with label/name fallback

### pkg/data/data.go (partial, 100+ lines)

**Purpose**: Block-level data operations for validation

**Key Functions**:
- `WriteBlockPattern(ctx, clientset, config, namespace, podName, blockIndex, pattern)`: Write 1MiB block via `dd`
- `ReadBlockHash(ctx, clientset, config, namespace, podName, offset, sizeBytes)`: SHA256 hash verification
- `VerifyAllocatedBlocks()`: Validates no zero-only blocks in allocated result
- `VerifyChangedBlocks()`: Compares block contents between snapshots

**Constants**:
- `DefaultBlockSize = 1048576` (1 MiB)
- `DefaultDevicePath = "/dev/xvda"`

### pkg/rbd/rbd.go (partial, 100+ lines)

**Purpose**: Ceph RBD introspection via toolbox pod

**Key Methods**:
- `IsImageFlattened(ctx, imageName)`: Checks if RBD image has no parent (flattened)
- `GetImageParent(ctx, imageName)`: Returns parent image/snapshot
- `GetSnapshotCount(ctx, imageName)`: Counts snapshots (for 250-limit monitoring)
- `ListOmapKeys(ctx, imageName)`: Inspects stored diffs in omap
- `GetCephMajorVersion(ctx)`: Validates Ceph >= 17 (Quincy)

**Toolbox Discovery**: Uses pod label matching with fallback

---

## Test Matrix Summary

| Suite | # Tests | Ordered | Labels | Focus Area |
|-------|---------|---------|--------|-----------|
| Basic CBT | 4 | ✓ | — | GetMetadataAllocated, GetMetadataDelta basics |
| Counter Deletion | 2 | ✓ | — | Reference counting, deferred deletion |
| ROX PVC | 5 | ✓ | — | ReadOnlyMany access, no flattening |
| Flattening Prevention | 4 | ✓ (2×) | — | Parent/child chain preservation |
| Priority Flattening | 3 | ✓ | `slow` | 250-snapshot limit priority queue |
| Stored Diffs | 4 | ✓ | `stored-diffs` | Omap fallback after flattening |
| Error Handling | 5 | ✗ | — | Error paths (concurrent, large volume) |
| Backup Workflow | 5 | ✓ | — | Full + incremental + restore cycle |
| Error Compliance | 3 | ✗ | — | Handle validation, spec compliance |
| Velero Compliance | 4 | ✓ | — | Handle-based delta (Velero use case) |
| Volume Resize | 3 | ✓ | — | Capacity updates on expansion |
| Block Metadata Properties | 8 | ✓ | — | Ordering, alignment, pagination, properties |
| **TOTAL** | **50** | | | |

---

## Make Targets vs. Test Execution

```bash
make e2e                # All 50 tests (5h timeout)
make e2e-fast           # Exclude slow + stored-diffs (40 tests, 2h)
make e2e-basic          # Only basic_cbt_test.go (4 tests, 30m)
make e2e-rox            # Only rox_pvc_test.go (5 tests, 30m)
make e2e-rox-deletion   # Only counter_deletion_test.go (2 tests, 30m)
make e2e-flattening     # flattening_prevention_test.go (4 tests, 30m)
make e2e-priority       # priority_flattening_test.go (3 tests, 3h)
make e2e-stored-diffs   # stored_diffs_test.go (4 tests, 1h)
make e2e-errors         # error_handling_test.go + error_compliance_test.go (8 tests, 30m)
make e2e-backup         # backup_workflow_test.go (5 tests, 1h)
```

---

## gRPC API Coverage

### GetMetadataAllocated
- **Tests**: Basic CBT (1), Priority Flattening (1), Stored Diffs (2), Backup Workflow (1), Error Handling (2), Error Compliance (1), Velero Compliance (2), Volume Resize (2), Block Metadata Properties (6)
- **Coverage**: Name-based, handle-based (via GetChangedBlocksByID), StartingOffset pagination, MaxResults batching
- **Streaming**: Collects all blocks via iterator pattern

### GetMetadataDelta
- **Tests**: Basic CBT (2), Flattening Prevention (2), Priority Flattening (1), Stored Diffs (2), Backup Workflow (3), Error Handling (3), Velero Compliance (2), Volume Resize (2), Block Metadata Properties (2)
- **Coverage**: Name-based (prev/target pairs), error handling (cross-PVC, reversed order), handle-based (GetChangedBlocksByID)
- **Streaming**: Collects all changed blocks via iterator pattern

### Authentication & RBAC
- **ServiceAccount**: `cbt-e2e-client` in test namespace
- **ClusterRole**: `external-snapshot-metadata-client-runner` (bound at BeforeSuite)
- **Token Generation**: Via iterator library's `BuildClients(config)`

### Pagination & Streaming
- **StartingOffset**: Skip first N bytes (Block Metadata Properties test)
- **MaxResults**: Per-message batch size (iterator still collects all)
- **Streaming**: gRPC server streams metadata blocks; client iterator batches per-message

---

## Error Scenarios Covered

| Scenario | Test | Expected Behavior |
|----------|------|-------------------|
| Non-existent snapshot | Error Handling | GetMetadataAllocated returns error |
| Cross-PVC delta | Error Handling, Error Compliance | GetMetadataDelta returns error |
| Reversed snapshot order | Error Handling | GetMetadataDelta returns error |
| Invalid snapshot handle | Error Compliance | GetChangedBlocksByID returns error |
| Cross-volume handle | Error Compliance | GetChangedBlocksByID returns error |
| Not-ready snapshot | Error Compliance | GetMetadataAllocated may error (race) |
| Deleted VolumeSnapshot (Type 1 CSI) | Velero Compliance | GetChangedBlocksByID returns error |
| Deleted VolumeSnapshot (Type 2 CSI) | Velero Compliance | GetChangedBlocksByID succeeds (handle retained) |
| Concurrent CBT calls | Error Handling | All succeed without errors |
| Large volume (5Gi, 50 blocks) | Error Handling | Streaming completes |

---

## Security & RBAC Testing

- **ServiceAccount Creation**: BeforeSuite creates `cbt-e2e-client` with explicit token binding
- **ClusterRoleBinding**: Links to `external-snapshot-metadata-client-runner` (pre-existing in cluster)
- **Token-Based Auth**: Iterator library auto-generates tokens from ServiceAccount
- **Privileged Namespace**: Test namespace marked `pod-security.kubernetes.io/enforce=privileged` for block device access
- **No Testing of**:
  - RBAC denial (assumes role binding exists)
  - Token expiration/refresh (iterator library handles)
  - Cross-namespace access (all tests use same namespace)

---

## Data Verification Approach

| Operation | Verification | Helper Function |
|-----------|--------------|------------------|
| Write block | Read hash post-write | `data.ReadBlockHash()` |
| Allocated block report | Offset contained in result | `MetadataResult.ContainsOffset()` |
| Changed blocks | Verify old blocks absent, new blocks present | Offset checks + `ContainsOffset()` |
| Restore integrity | Compare SHA256 hashes at snapshot points | `data.ReadBlockHash()` comparison |
| Large volume | Successful stream completion | `MetadataResult.Blocks` count |

---

## Cluster Preconditions

### Required Resources
- Kubernetes 1.33+ cluster
- CephCSI RBD provisioner with external-snapshot-metadata sidecar
- VolumeSnapshot CRDs (external-snapshotter)
- SnapshotMetadataService CRD (external-snapshot-metadata)
- Ceph >= 17 (Quincy)
- StorageClass: `ocs-storagecluster-ceph-rbd` (ODF default)
- VolumeSnapshotClass: `ocs-storagecluster-rbdplugin-snapclass` (ODF default)

### ODF Version Compatibility
- **ODF < 4.18**: Pod label `app=csi-rbdplugin-provisioner`
- **ODF 4.18-4.20**: Pod label `app.kubernetes.io/name=csi-rbdplugin,app.kubernetes.io/component=ctrlplugin`
- **ODF 4.21+**: Pod name pattern fallback (contains "rbd" + "ctrlplugin")

### Optional
- Sidecar check is warning-only (tests may partially fail without it)

---

## Future Test Coverage Gaps

| Gap | Impact | Mitigation |
|-----|--------|-----------|
| **Transaction isolation** | Multiple snapshot creation during reads | None (covered by concurrent test) |
| **Snapshot consistency** | Snapshot deleted during GetMetadata call | Error Compliance (partial) |
| **Cross-region DR** | Multi-cluster replication | Not in scope |
| **Snapshot merge** | Parent/child merge during snapshot lifetime | Not in scope (out-of-tree feature) |
| **Custom block size** | Non-1MiB blocks | RBD spec: fixed 1MiB only |
| **Thin provisioning** | Sparse volume write patterns | Block Metadata Properties (partial) |
| **I/O during snapshot** | Hot snapshot creation | All snapshots created after pod deletion |

