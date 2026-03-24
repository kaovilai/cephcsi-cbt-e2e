# Kubernetes CBT E2E Test Coverage Mapping

This document maps the Kubernetes CSI Changed Block Tracking (CBT) requirements —
from KEP-3314, the external-snapshot-metadata gRPC spec, and the sidecar
implementation — to E2E tests in this suite, highlighting **coverage gaps** with
upstream kubernetes/kubernetes.

Reference: [KEP-3314](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md) |
[external-snapshot-metadata](https://github.com/kubernetes-csi/external-snapshot-metadata) |
[velero-coverage.md](./velero-coverage.md)

> **Key finding**: No CBT e2e tests exist upstream in `kubernetes/kubernetes` or
> `kubernetes-csi/external-snapshot-metadata`. Only unit tests exist in the
> sidecar repo. This suite is currently the only known e2e validation of the
> CBT API against a real CSI driver.

---

## Upstream Test Landscape

| Repository | E2E Tests | Unit Tests | Notes |
|------------|-----------|------------|-------|
| `kubernetes/kubernetes` (`test/e2e/storage/`) | **None** | N/A | No snapshot_metadata, CBT, or changed_block_tracking files |
| `kubernetes-csi/external-snapshot-metadata` | **None** | Comprehensive | Server gRPC, auth, iterator, validation — all mocked |
| This suite (`cephcsi-cbt-e2e`) | **50 tests** | N/A | Real Ceph RBD cluster, real CSI driver |

---

## 1. gRPC API Behavior (Happy Path)

| Req ID | Upstream Requirement | Test File | Lines | Test Description | Status |
|--------|---------------------|-----------|-------|------------------|--------|
| T-API-1 | `GetMetadataAllocated` returns all allocated blocks | `basic_cbt_test.go` | 93-102 | should return allocated blocks for a single snapshot | Covered |
| T-API-2 | `GetMetadataDelta` returns changed blocks between snapshots | `basic_cbt_test.go` | 104-117 | should return changed blocks between consecutive snapshots | Covered |
| T-API-3 | Allocated blocks in ascending `byte_offset` order | `block_metadata_properties_test.go` | 111-117 | should return blocks in ascending order by ByteOffset | Covered |
| T-API-4 | Delta blocks in ascending `byte_offset` order | `block_metadata_properties_test.go` | 111-117 | should return blocks in ascending order by ByteOffset | Covered |
| T-API-5 | No overlapping block ranges in allocated response | `block_metadata_properties_test.go` | 119-122 | should return non-overlapping block ranges | Covered |
| T-API-6 | No overlapping block ranges in delta response | `block_metadata_properties_test.go` | 119-122 | should return non-overlapping block ranges | Covered |
| T-API-7 | `block_metadata_type` constant across all stream messages | `block_metadata_properties_test.go` | 142-145 | should report FIXED_LENGTH BlockMetadataType | Partial (allocated only; delta not validated) |
| T-API-8 | `volume_capacity_bytes` constant across all stream messages | `block_metadata_properties_test.go` | 124-130 | should report consistent VolumeCapacityBytes across calls | Covered |
| T-API-9 | `volume_capacity_bytes` matches actual PVC size | `volume_resize_test.go` | 104-110 | should report updated VolumeCapacityBytes after expansion | Covered |
| T-API-10 | For `FIXED_LENGTH`, `size_bytes` constant across all tuples | `block_metadata_properties_test.go` | 132-140 | should return 1MiB-aligned block offsets and sizes | Covered |
| T-API-11 | `starting_offset=0` returns from beginning | `block_metadata_properties_test.go` | 147-171 | should support StartingOffset for resumption | Implicit |
| T-API-12 | `starting_offset > 0` resumes from specified offset | `block_metadata_properties_test.go` | 147-171 | should support StartingOffset for resumption | Covered |
| T-API-13 | `max_results` limits tuples per response message | `block_metadata_properties_test.go` | 173-183 | should honor MaxResults parameter without error | Covered |
| T-API-14 | `max_results=0` returns all results (driver default) | `basic_cbt_test.go` | 93-102 | Default calls use max_results=0 | Implicit |
| T-API-15 | Stream terminates normally with EOF | All tests | — | Iterator collects all blocks then returns | Implicit |

---

## 2. Data Correctness

| Req ID | Upstream Requirement | Test File | Lines | Test Description | Status |
|--------|---------------------|-----------|-------|------------------|--------|
| T-DATA-1 | Allocated blocks match actual written data regions | `basic_cbt_test.go` | 133-143 | should report accurate metadata matching written data | Covered |
| T-DATA-2 | Delta blocks match actual changed regions | `backup_workflow_test.go` | 144-164 | should perform first incremental backup via GetMetadataDelta | Covered |
| T-DATA-3 | Unwritten regions not reported as allocated | `basic_cbt_test.go` | 133-143 | Validates only written blocks appear | Partial |
| T-DATA-4 | Unchanged regions not reported in delta | `backup_workflow_test.go` | 144-164 | Verifies unchanged blocks (0,2) excluded from delta | Covered |
| T-DATA-5 | Data at reported offsets matches source snapshot | `backup_workflow_test.go` | 183-219 | should restore from chain and verify data integrity | Covered |
| T-DATA-6 | Multiple writes to same block = single delta entry | — | — | — | **GAP** |

---

## 3. Error Conditions

### Sidecar-Level Errors

| Req ID | Upstream Requirement | gRPC Code | Test File | Lines | Status |
|--------|---------------------|-----------|-----------|-------|--------|
| T-ERR-1 | Missing `security_token` | `InvalidArgument` (3) | — | — | **GAP** |
| T-ERR-2 | Missing `namespace` | `InvalidArgument` (3) | — | — | **GAP** |
| T-ERR-3 | Missing `snapshot_name` | `InvalidArgument` (3) | — | — | **GAP** |
| T-ERR-4 | Missing `base_snapshot_id` (delta) | `InvalidArgument` (3) | — | — | **GAP** |
| T-ERR-5 | Missing `target_snapshot_name` (delta) | `InvalidArgument` (3) | — | — | **GAP** |
| T-ERR-6 | Invalid/expired token | `Unauthenticated` (16) | — | — | **GAP** |
| T-ERR-7 | Unauthorized user | `PermissionDenied` (7) | — | — | **GAP** |
| T-ERR-8 | Non-existent snapshot | `Unavailable` (14) | `error_handling_test.go` | 24-28 | Covered |
| T-ERR-9 | Snapshot not ready | `Unavailable` (14) | `error_compliance_test.go` | 133-174 | Covered |
| T-ERR-10 | VolumeSnapshotContent not found | `Unavailable` (14) | — | — | **GAP** |
| T-ERR-11 | Missing snapshot handle | `Unavailable` (14) | — | — | **GAP** |
| T-ERR-12 | CSI driver mismatch | `InvalidArgument` (3) | — | — | **GAP** |
| T-ERR-13 | CSI driver not ready | `Unavailable` (14) | — | — | **GAP** |

### CSI Driver-Level Errors

| Req ID | Upstream Requirement | gRPC Code | Test File | Lines | Status |
|--------|---------------------|-----------|-----------|-------|--------|
| T-ERR-14 | `starting_offset` > volume size | `OutOfRange` (11) | — | — | **GAP** |
| T-ERR-15 | CBT not enabled | `FailedPrecondition` (9) | — | — | **GAP** |
| — | Cross-PVC delta | Driver-specific | `error_handling_test.go` | 30-92 | Covered |
| — | Reversed snapshot order | Driver-specific | `error_handling_test.go` | 94-137 | Covered |
| — | Invalid snapshot handle | Driver-specific | `error_compliance_test.go` | 22-62 | Covered |
| — | Cross-volume handle | Driver-specific | `error_compliance_test.go` | 64-131 | Covered |

---

## 4. Security & Authentication

| Req ID | Upstream Requirement | Test File | Lines | Status |
|--------|---------------------|-----------|-------|--------|
| T-SEC-1 | Valid audience-scoped token succeeds | `e2e_suite_test.go` | 88-219 | Implicit (all tests use valid token) |
| T-SEC-2 | Wrong audience token fails with `Unauthenticated` | — | — | **GAP** |
| T-SEC-3 | Expired token fails with `Unauthenticated` | — | — | **GAP** |
| T-SEC-4 | User with `volumesnapshot get` permission succeeds | `e2e_suite_test.go` | 88-219 | Implicit (RBAC binding created) |
| T-SEC-5 | User without `volumesnapshot get` fails with `PermissionDenied` | — | — | **GAP** |
| T-SEC-6 | Cross-namespace access denied | — | — | **GAP** |
| T-SEC-7 | TLS connection required (plaintext rejected) | — | — | **GAP** |
| T-SEC-8 | Invalid CA cert fails connection | — | — | **GAP** |
| T-SEC-9 | RBAC: client can create `serviceaccounts/token` | `e2e_suite_test.go` | 88-219 | Implicit |
| T-SEC-10 | RBAC: client can get/list `snapshotmetadataservices` | `e2e_suite_test.go` | 88-219 | Implicit |

---

## 5. SnapshotMetadataService CRD

| Req ID | Upstream Requirement | Test File | Lines | Status |
|--------|---------------------|-----------|-------|--------|
| T-CRD-1 | SMS CR can be created with valid spec | `e2e_suite_test.go` | 88-219 | Implicit (precondition check) |
| T-CRD-2 | SMS CR discoverable by CSI driver name | — | — | **GAP** |
| T-CRD-3 | SMS CR deletion disables metadata service | — | — | **GAP** |
| T-CRD-4 | SMS CR `address` points to working gRPC endpoint | `e2e_suite_test.go` | 88-219 | Implicit |
| T-CRD-5 | SMS CR `caCert` enables TLS verification | `e2e_suite_test.go` | 88-219 | Implicit |

---

## 6. Streaming & Resumption

| Req ID | Upstream Requirement | Test File | Lines | Status |
|--------|---------------------|-----------|-------|--------|
| T-STRM-1 | Large response spans multiple stream messages | `error_handling_test.go` | 189-229 | Covered (5Gi, 50 blocks) |
| T-STRM-2 | Client can resume with `starting_offset` after interruption | `block_metadata_properties_test.go` | 147-171 | Covered |
| T-STRM-3 | Resumed stream returns correct remaining blocks | `block_metadata_properties_test.go` | 147-171 | Covered |
| T-STRM-4 | `max_results` pagination works correctly | `block_metadata_properties_test.go` | 173-183 | Covered |
| T-STRM-5 | Empty snapshot returns empty block list | — | — | **GAP** |
| T-STRM-6 | Client context cancellation terminates stream | — | — | **GAP** |

---

## 7. Snapshot Lifecycle

| Req ID | Upstream Requirement | Test File | Lines | Status |
|--------|---------------------|-----------|-------|--------|
| T-SNAP-1 | Metadata available immediately after snapshot ready | All ordered tests | — | Implicit (WaitForSnapshotReady then query) |
| T-SNAP-2 | Metadata for deleted snapshot returns error | `velero_compliance_test.go` | 135-199 | Covered (Type 1 path) |
| T-SNAP-3 | Delta between different volumes returns error | `error_handling_test.go` | 30-92 | Covered |
| T-SNAP-4 | Delta with target older than base returns error | `error_handling_test.go` | 94-137 | Covered (CSI driver enforced; sidecar does not validate ordering) |
| T-SNAP-5 | Metadata survives snapshot content migration | — | — | **GAP** |

---

## 8. Backup Workflow (End-to-End)

| Req ID | Upstream Requirement | Test File | Lines | Status |
|--------|---------------------|-----------|-------|--------|
| T-BKP-1 | Full backup: allocated blocks + verify data | `backup_workflow_test.go` | 128-142 | Covered |
| T-BKP-2 | Incremental backup: delta blocks + verify changes | `backup_workflow_test.go` | 144-164 | Covered |
| T-BKP-3 | Restore from full backup produces identical volume | `backup_workflow_test.go` | 183-218 | Covered |
| T-BKP-4 | Restore from incremental produces correct state | `backup_workflow_test.go` | 183-218 | Covered |
| T-BKP-5 | Multiple incrementals in chain work correctly | `backup_workflow_test.go` | 166-181 | Covered |

---

## 9. Compatibility

| Req ID | Upstream Requirement | Test File | Lines | Status |
|--------|---------------------|-----------|-------|--------|
| T-COMPAT-1 | Driver without CBT returns `UNIMPLEMENTED` | — | — | **GAP** (requires non-CBT driver) |
| T-COMPAT-2 | VolumeSnapshot v1 objects work correctly | All tests | — | Covered |
| T-COMPAT-3 | Multiple concurrent metadata requests succeed | `error_handling_test.go` | 139-187 | Covered |

---

## 10. Volume Operations

| Upstream Requirement | Test File | Lines | Test Description | Status |
|---------------------|-----------|-------|------------------|--------|
| CBT after volume expansion | `volume_resize_test.go` | 104-136 | VolumeCapacityBytes updated, expanded region blocks reported | Covered |
| Non-aligned volume size handling | `block_metadata_properties_test.go` | 185-237 | 1500Mi PVC (not 1MiB-aligned) | Covered |
| Cumulative delta (non-consecutive snaps) | `basic_cbt_test.go` | 119-131 | should return cumulative changes | Covered |

---

## Gap Summary

### By Category

Counts use: **Covered** = explicitly tested, **Implicit** = exercised indirectly
(suite wouldn't run without it), **Partial** = partially tested, **Gap** = not tested.

| Category | Total | Covered | Implicit | Partial | Gap | Explicit % |
|----------|-------|---------|----------|---------|-----|-----------|
| API Behavior (Happy Path) | 15 | 10 | 3 | 2 | 0 | 67% (100% incl. implicit) |
| Data Correctness | 6 | 4 | 0 | 1 | 1 | 67% |
| Error Conditions (Sidecar) | 13 | 2 | 0 | 0 | 11 | 15% |
| Error Conditions (CSI Driver) | 6 | 4 | 0 | 0 | 2 | 67% |
| Security & Auth | 10 | 0 | 4 | 0 | 6 | 0% (40% incl. implicit) |
| CRD Operations | 5 | 0 | 3 | 0 | 2 | 0% (60% incl. implicit) |
| Streaming & Resumption | 6 | 4 | 0 | 0 | 2 | 67% |
| Snapshot Lifecycle | 5 | 3 | 1 | 0 | 1 | 60% (80% incl. implicit) |
| Backup Workflow | 5 | 5 | 0 | 0 | 0 | 100% |
| Compatibility | 3 | 2 | 0 | 0 | 1 | 67% |
| Monitoring & Observability | 2 | 0 | 0 | 0 | 2 | 0% |
| **TOTAL** | **76** | **34** | **11** | **3** | **28** | **45%** (63% incl. implicit) |

### Critical Gaps (High Impact)

These gaps represent areas where upstream graduation criteria (beta/GA) will
likely require e2e coverage:

| Priority | Gap | Why It Matters | Proposed Test |
|----------|-----|---------------|---------------|
| P0 | **No CRD lifecycle tests** (T-CRD-2, T-CRD-3) | SMS CR is the discovery mechanism backup tools rely on | Create `crd_lifecycle_test.go`: delete SMS CR, verify service becomes unavailable, re-create |
| P0 | **No empty snapshot test** (T-STRM-5) | Common edge case; empty volume backup must not crash | Add to `basic_cbt_test.go`: snapshot before any writes |
| P0 | **No security/auth e2e tests** (T-SEC-2 through T-SEC-8) | Beta graduation requires auth/authz testing per KEP | Create `security_test.go`: wrong audience, expired token, unauthorized SA, cross-namespace, TLS validation (requires raw gRPC infrastructure) |
| P0 | **No sidecar error validation** (T-ERR-1 through T-ERR-7) | API contract violations if not enforced | Create `sidecar_errors_test.go`: send raw gRPC calls with missing fields, verify error codes (requires raw gRPC infrastructure) |
| P1 | **No client cancellation test** (T-STRM-6) | gRPC streaming contract requirement | Add to `error_handling_test.go`: cancel context mid-stream |
| P2 | **No `starting_offset > capacity` test** (T-ERR-14) | OutOfRange error path untested | Add to `block_metadata_properties_test.go` |
| P2 | **No multi-write-same-block test** (T-DATA-6) | Data correctness edge case | Add to `basic_cbt_test.go`: overwrite block, verify single delta |
| P3 | **No UNIMPLEMENTED driver test** (T-COMPAT-1) | Requires separate non-CBT CSI driver deployment | Out of scope for this suite |

### Sidecar Error Gaps (Detail)

These require **raw gRPC calls** bypassing the iterator library (which
auto-populates required fields):

| Error Condition | gRPC Code | How to Test |
|----------------|-----------|-------------|
| Missing `security_token` | `InvalidArgument` | Direct gRPC call with empty token |
| Missing `namespace` | `InvalidArgument` | Direct gRPC call with empty namespace |
| Missing `snapshot_name` | `InvalidArgument` | Direct gRPC call with empty snapshot name |
| Invalid token | `Unauthenticated` | Forge token with wrong audience or expired |
| Unauthorized user | `PermissionDenied` | Create SA without `external-snapshot-metadata-client-runner` binding |
| CSI driver mismatch | `InvalidArgument` | Create snapshot with different CSI driver, query via CBT |

### Security Gap Details

| Gap | Implementation Approach |
|-----|------------------------|
| Wrong audience token | Create token with `audience: ["wrong"]`, call gRPC directly |
| Expired token | Create token with `expirationSeconds: 1`, wait, then call |
| Unauthorized SA | Create new SA without RBAC binding, attempt CBT query |
| Cross-namespace access | Create snapshot in ns-A, query from SA in ns-B |
| TLS validation | Attempt gRPC with `InsecureSkipVerify` or wrong CA |
| Plaintext rejection | Attempt gRPC without TLS |

---

## KEP Spec vs Implementation Discrepancies

Issues discovered during research that affect test design:

| # | Discrepancy | Impact on Tests |
|---|-------------|-----------------|
| 1 | `base_snapshot_id` in delta is a **CSI handle**, not a K8s VolumeSnapshot name | Tests must use `GetChangedBlocksByID` (handle-based). Our suite handles this correctly. |
| 2 | Sidecar does **NOT validate** same-volume for delta requests | Cross-volume errors come from CSI driver, not sidecar. Error code may differ. |
| 3 | Sidecar does **NOT validate** snapshot ordering | Reversed-order errors come from CSI driver, not sidecar. Error code may differ. |
| 4 | CRD graduated to **v1beta1** (not v1alpha1 as noted in CLAUDE.md) | Suite should test against v1beta1 CRD. Update CLAUDE.md reference. |
| 5 | KEP says `NotFound` for missing snapshots, sidecar returns `Unavailable` | Error code assertions should use `Unavailable` (14), not `NotFound` (5). |

---

## 11. Monitoring & Observability

| Req ID | Upstream Requirement | Test File | Lines | Status |
|--------|---------------------|-----------|-------|--------|
| T-MON-1 | `snapshot_metadata_controller_operation_total_seconds` histogram metric | — | — | **GAP** |
| T-MON-2 | `/health` endpoint returns `ServiceUnavailable` until gRPC ready | — | — | **GAP** |

---

## Relationship to Velero Coverage

This document focuses on **upstream K8s/CSI requirements**. The companion
[velero-coverage.md](./velero-coverage.md) maps **Velero Block Data Mover**
(PR #9528) requirements. Key overlaps:

| Area | K8s Coverage (this doc) | Velero Coverage |
|------|------------------------|-----------------|
| Handle-based delta | T-API-2, T-SNAP-2 | Handle retention, Type 1 vs Type 2 |
| Backup workflow | T-BKP-1 through T-BKP-5 | Full + incremental chain simulation |
| Error fallback | T-ERR-8, T-ERR-9 | Fallback-to-full on FailedPrecondition |
| Snapshot retention | T-SNAP-2 | `retainSnapshot: "true"` requirement |
| Ceph-specific (250 limit, flattening, omap) | Not in upstream spec | Velero-specific coverage |

---

## Running Gap-Specific Tests

Once gap tests are implemented:

```bash
# Security tests
make e2e-security        # Auth/authz/TLS validation (proposed)

# Sidecar error tests
make e2e-sidecar-errors  # Raw gRPC error code validation (proposed)

# CRD lifecycle tests
make e2e-crd             # SMS CR create/delete lifecycle (proposed)

# All gap tests
make e2e-gaps            # All new gap tests (proposed)
```

---

## Proposed New Test Files

| File | Tests | Priority | Estimated Time |
|------|-------|----------|---------------|
| `security_test.go` | T-SEC-2 through T-SEC-8 (6 tests) | P0 | 30m |
| `sidecar_errors_test.go` | T-ERR-1 through T-ERR-7, T-ERR-10, T-ERR-11, T-ERR-12, T-ERR-13, T-ERR-15 (12 tests) | P0 | 45m |
| `crd_lifecycle_test.go` | T-CRD-2, T-CRD-3 (2 tests) | P0 | 15m |
| `monitoring_test.go` | T-MON-1, T-MON-2 (2 tests) | P2 | 15m |
| Additions to existing files | T-API-7 delta, T-DATA-6, T-STRM-5, T-STRM-6, T-ERR-14 (5 tests) | P1-P2 | 15m |
