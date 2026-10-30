# Kubernetes CBT E2E Test Coverage Mapping

This document maps the Kubernetes CSI Changed Block Tracking (CBT) requirements —
from KEP-3314, the external-snapshot-metadata gRPC spec, and the sidecar
implementation — to E2E tests in this suite, highlighting **coverage gaps** with
upstream kubernetes/kubernetes.

Reference: [KEP-3314](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md) |
[external-snapshot-metadata](https://github.com/kubernetes-csi/external-snapshot-metadata) |
[velero-coverage.md](./velero-coverage.md)

> **Key finding**: `kubernetes/kubernetes` has a generic CSI snapshot-metadata e2e
> testsuite (`test/e2e/storage/testsuites/snapshot-metadata.go`, with `should verify
> GetMetadataDelta` and `should verify GetAllocatedMetadata` cases, plus
> `test/e2e/storage/utils/snapshot-metadata.go` and a
> `testing-manifests/storage-csi/external-snapshot-metadata/` runner). It runs against
> drivers that implement `SnapshotMetadataTestDriver` (the CSI hostpath driver wiring is in
> `test/e2e/storage/drivers/csi.go`) and has no Ceph-specific, flattening, ROX or
> Velero-workflow coverage. `kubernetes-csi/external-snapshot-metadata` has unit tests,
> CRD CEL validation tests and an integration-test workflow. CephCSI added its own RBD
> snapshot-metadata e2e tests in [ceph-csi#6459](https://github.com/ceph/ceph-csi/pull/6459)
> (merged Aug 2026). This suite targets the CBT API end-to-end against a real Ceph RBD
> driver through the sidecar, including Velero-oriented workflows. The Req ID tables below
> were not re-mapped against the upstream testsuite; its two cases overlap the basic
> `GetMetadataAllocated` / `GetMetadataDelta` happy paths (T-API-1, T-API-2).
>
> Status as of Oct 2026. KEP-3314 is **beta** (Kubernetes v1.36); there is no feature
> gate (the feature is out-of-tree), and the CRD is `v1beta1`. No cluster results
> are claimed by this document; see [results.md](./results.md) for the dated run.

---

## Upstream Test Landscape

| Repository | E2E Tests | Unit Tests | Notes |
|------------|-----------|------------|-------|
| `kubernetes/kubernetes` (`test/e2e/storage/`) | Generic snapshot-metadata testsuite (2 cases: `GetMetadataDelta`, `GetAllocatedMetadata`) | N/A | Driver-agnostic (`testsuites/snapshot-metadata.go`); no Ceph/flattening/ROX/Velero coverage |
| `kubernetes-csi/external-snapshot-metadata` | **None** | Comprehensive | Server gRPC, auth, iterator, validation — all mocked; plus CRD CEL tests and an integration-test workflow |
| `ceph/ceph-csi` | RBD snapshot-metadata e2e ([#6459](https://github.com/ceph/ceph-csi/pull/6459)) | Yes | Driver-level tests in the CephCSI repo |
| This suite (`cephcsi-cbt-e2e`) | **48 tests** (`It` blocks, see below) | N/A | Real Ceph RBD cluster, real CSI driver |

Per-file test counts (`It` blocks under `tests/e2e/`):

| File | Tests |
|------|-------|
| `block_metadata_properties_test.go` | 8 |
| `error_handling_test.go` | 8 |
| `backup_workflow_test.go` | 6 |
| `stored_diffs_test.go` | 6 |
| `flattening_prevention_test.go` | 4 |
| `rox_pvc_test.go` | 4 |
| `velero_compliance_test.go` | 4 |
| `volume_mode_rebind_test.go` | 3 |
| `volume_resize_test.go` | 3 |
| `counter_deletion_test.go` | 2 |
| **Total** | **48** |

---

## 1. gRPC API Behavior (Happy Path)

| Req ID | Upstream Requirement | Test File | Test Description | Status |
|--------|---------------------|-----------|------------------|--------|
| T-API-1 | `GetMetadataAllocated` returns all allocated blocks | `backup_workflow_test.go` | should perform full backup via GetMetadataAllocated | Covered |
| T-API-2 | `GetMetadataDelta` returns changed blocks between snapshots | `backup_workflow_test.go` | should perform first incremental backup via GetMetadataDelta | Covered |
| T-API-3 | Allocated blocks in ascending `byte_offset` order | `block_metadata_properties_test.go` | should return blocks in ascending order by ByteOffset | Covered |
| T-API-4 | Delta blocks in ascending `byte_offset` order | `block_metadata_properties_test.go` | should return blocks in ascending order by ByteOffset | Covered |
| T-API-5 | No overlapping block ranges in allocated response | `block_metadata_properties_test.go` | should return non-overlapping block ranges | Covered |
| T-API-6 | No overlapping block ranges in delta response | `block_metadata_properties_test.go` | should return non-overlapping block ranges | Covered |
| T-API-7 | `block_metadata_type` constant across all stream messages | `block_metadata_properties_test.go` | should report VARIABLE_LENGTH BlockMetadataType | Partial (allocated only; delta not validated) |
| T-API-8 | `volume_capacity_bytes` constant across all stream messages | `block_metadata_properties_test.go` | should report consistent VolumeCapacityBytes across calls | Covered |
| T-API-9 | `volume_capacity_bytes` matches actual PVC size | `volume_resize_test.go` | should report updated VolumeCapacityBytes after expansion | Covered |
| T-API-10 | For `FIXED_LENGTH`, `size_bytes` constant across all tuples | `block_metadata_properties_test.go` | should return 1MiB-aligned block offsets and sizes | Covered |
| T-API-11 | `starting_offset=0` returns from beginning | `block_metadata_properties_test.go` | should support StartingOffset for resumption | Implicit |
| T-API-12 | `starting_offset > 0` resumes from specified offset | `block_metadata_properties_test.go` | should support StartingOffset for resumption | Covered |
| T-API-13 | `max_results` limits tuples per response message | `block_metadata_properties_test.go` | should honor MaxResults parameter without error | Covered |
| T-API-14 | `max_results=0` returns all results (driver default) | `backup_workflow_test.go` | Default calls use max_results=0 | Implicit |
| T-API-15 | Stream terminates normally with EOF | All tests | Iterator collects all blocks then returns | Implicit |

---

## 2. Data Correctness

| Req ID | Upstream Requirement | Test File | Test Description | Status |
|--------|---------------------|-----------|------------------|--------|
| T-DATA-1 | Allocated blocks match actual written data regions | `backup_workflow_test.go` | should report accurate metadata matching all written data | Covered |
| T-DATA-2 | Delta blocks match actual changed regions | `backup_workflow_test.go` | should perform first incremental backup via GetMetadataDelta | Covered |
| T-DATA-3 | Unwritten regions not reported as allocated | `backup_workflow_test.go` | "should report accurate metadata matching all written data" checks written blocks are present; it does not assert absence of unwritten blocks | Partial |
| T-DATA-4 | Unchanged regions not reported in delta | `backup_workflow_test.go` | Verifies unchanged blocks (0,2) excluded from delta | Covered |
| T-DATA-5 | Data at reported offsets matches source snapshot | `backup_workflow_test.go` | should restore from chain and verify data integrity | Covered |
| T-DATA-6 | Multiple writes to same block = single delta entry | — | — | **GAP** |

---

## 3. Error Conditions

### Sidecar-Level Errors

| Req ID | Upstream Requirement | gRPC Code | Test File | Status |
|--------|---------------------|-----------|-----------|--------|
| T-ERR-1 | Missing `security_token` | `InvalidArgument` (3) | — | **GAP** |
| T-ERR-2 | Missing `namespace` | `InvalidArgument` (3) | — | **GAP** |
| T-ERR-3 | Missing `snapshot_name` | `InvalidArgument` (3) | — | **GAP** |
| T-ERR-4 | Missing `base_snapshot_id` (delta) | `InvalidArgument` (3) | — | **GAP** |
| T-ERR-5 | Missing `target_snapshot_name` (delta) | `InvalidArgument` (3) | — | **GAP** |
| T-ERR-6 | Invalid/expired token | `Unauthenticated` (16) | — | **GAP** |
| T-ERR-7 | Unauthorized user | `PermissionDenied` (7) | — | **GAP** |
| T-ERR-8 | Non-existent snapshot | `Unavailable` (14) | `error_handling_test.go` | Covered |
| T-ERR-9 | Snapshot not ready | `Unavailable` (14) | `error_handling_test.go` ("should return error when querying CBT on snapshot that is not ready") | Covered |
| T-ERR-10 | VolumeSnapshotContent not found | `Unavailable` (14) | — | **GAP** |
| T-ERR-11 | Missing snapshot handle | `Unavailable` (14) | — | **GAP** |
| T-ERR-12 | CSI driver mismatch | `InvalidArgument` (3) | — | **GAP** |
| T-ERR-13 | CSI driver not ready | `Unavailable` (14) | — | **GAP** |

### CSI Driver-Level Errors

| Req ID | Upstream Requirement | gRPC Code | Test File | Status |
|--------|---------------------|-----------|-----------|--------|
| T-ERR-14 | `starting_offset` > volume size | `OutOfRange` (11) | — | **GAP** |
| T-ERR-15 | CBT not enabled | `FailedPrecondition` (9) | — | **GAP** |
| — | Cross-PVC delta | Driver-specific | `error_handling_test.go` | Covered |
| — | Reversed snapshot order | Driver-specific | `error_handling_test.go` | Covered |
| — | Invalid snapshot handle | Driver-specific | `error_handling_test.go` ("should return error for invalid snapshot handle in GetChangedBlocksByID") | Covered |
| — | Cross-volume handle | Driver-specific | `error_handling_test.go` ("should return error for GetChangedBlocksByID with handle from different volume") | Covered |

---

## 4. Security & Authentication

| Req ID | Upstream Requirement | Test File | Status |
|--------|---------------------|-----------|--------|
| T-SEC-1 | Valid audience-scoped token succeeds | `e2e_suite_test.go` | Implicit (all tests use valid token) |
| T-SEC-2 | Wrong audience token fails with `Unauthenticated` | — | **GAP** |
| T-SEC-3 | Expired token fails with `Unauthenticated` | — | **GAP** |
| T-SEC-4 | User with `volumesnapshot get` permission succeeds | `e2e_suite_test.go` | Implicit (RBAC binding created) |
| T-SEC-5 | User without `volumesnapshot get` fails with `PermissionDenied` | — | **GAP** |
| T-SEC-6 | Cross-namespace access denied | — | **GAP** |
| T-SEC-7 | TLS connection required (plaintext rejected) | — | **GAP** |
| T-SEC-8 | Invalid CA cert fails connection | — | **GAP** |
| T-SEC-9 | RBAC: client can create `serviceaccounts/token` | `e2e_suite_test.go` | Implicit |
| T-SEC-10 | RBAC: client can get/list `snapshotmetadataservices` | `e2e_suite_test.go` | Implicit |

---

## 5. SnapshotMetadataService CRD

| Req ID | Upstream Requirement | Test File | Status |
|--------|---------------------|-----------|--------|
| T-CRD-1 | SMS CR can be created with valid spec | `e2e_suite_test.go` | Implicit (precondition check) |
| T-CRD-2 | SMS CR discoverable by CSI driver name | — | **GAP** |
| T-CRD-3 | SMS CR deletion disables metadata service | — | **GAP** |
| T-CRD-4 | SMS CR `address` points to working gRPC endpoint | `e2e_suite_test.go` | Implicit |
| T-CRD-5 | SMS CR `caCert` enables TLS verification | `e2e_suite_test.go` | Implicit |

---

## 6. Streaming & Resumption

| Req ID | Upstream Requirement | Test File | Status |
|--------|---------------------|-----------|--------|
| T-STRM-1 | Large response spans multiple stream messages | `error_handling_test.go` | Covered (5Gi, 50 blocks) |
| T-STRM-2 | Client can resume with `starting_offset` after interruption | `block_metadata_properties_test.go` | Covered |
| T-STRM-3 | Resumed stream returns correct remaining blocks | `block_metadata_properties_test.go` | Covered |
| T-STRM-4 | `max_results` pagination works correctly | `block_metadata_properties_test.go` | Covered |
| T-STRM-5 | Empty snapshot returns empty block list | — | **GAP** |
| T-STRM-6 | Client context cancellation terminates stream | — | **GAP** |

---

## 7. Snapshot Lifecycle

| Req ID | Upstream Requirement | Test File | Status |
|--------|---------------------|-----------|--------|
| T-SNAP-1 | Metadata available immediately after snapshot ready | All ordered tests | Implicit (WaitForSnapshotReady then query) |
| T-SNAP-2 | Metadata for deleted snapshot returns error | `velero_compliance_test.go` | Covered (Type 1 path) |
| T-SNAP-3 | Delta between different volumes returns error | `error_handling_test.go` | Covered |
| T-SNAP-4 | Delta with target older than base returns error | `error_handling_test.go` | Covered (CSI driver enforced; sidecar does not validate ordering) |
| T-SNAP-5 | Metadata survives snapshot content migration | — | **GAP** |

---

## 8. Backup Workflow (End-to-End)

| Req ID | Upstream Requirement | Test File | Status |
|--------|---------------------|-----------|--------|
| T-BKP-1 | Full backup: allocated blocks + verify data | `backup_workflow_test.go` | Covered |
| T-BKP-2 | Incremental backup: delta blocks + verify changes | `backup_workflow_test.go` | Covered |
| T-BKP-3 | Restore from full backup produces identical volume | `backup_workflow_test.go` | Covered |
| T-BKP-4 | Restore from incremental produces correct state | `backup_workflow_test.go` | Covered |
| T-BKP-5 | Multiple incrementals in chain work correctly | `backup_workflow_test.go` | Covered |

---

## 9. Compatibility

| Req ID | Upstream Requirement | Test File | Status |
|--------|---------------------|-----------|--------|
| T-COMPAT-1 | Driver without CBT returns `UNIMPLEMENTED` | — | **GAP** (requires non-CBT driver) |
| T-COMPAT-2 | VolumeSnapshot v1 objects work correctly | All tests | Covered |
| T-COMPAT-3 | Multiple concurrent metadata requests succeed | `error_handling_test.go` | Covered |

---

## 10. Volume Operations

| Upstream Requirement | Test File | Test Description | Status |
|---------------------|-----------|------------------|--------|
| CBT after volume expansion | `volume_resize_test.go` | VolumeCapacityBytes updated, expanded region blocks reported | Covered |
| Non-aligned volume size handling | `block_metadata_properties_test.go` | 1500Mi PVC (not 1MiB-aligned) | Covered |
| Cumulative delta (non-consecutive snaps) | — | No current test (the chained incremental test only compares consecutive snapshots) | **GAP** |

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

KEP-3314 reached beta in Kubernetes v1.36 and has no GA target yet. These gaps
represent areas where upstream graduation criteria (GA) are likely to require e2e
coverage:

| Priority | Gap | Why It Matters | Proposed Test |
|----------|-----|---------------|---------------|
| P0 | **No CRD lifecycle tests** (T-CRD-2, T-CRD-3) | SMS CR is the discovery mechanism backup tools rely on | Create `crd_lifecycle_test.go`: delete SMS CR, verify service becomes unavailable, re-create |
| P0 | **No empty snapshot test** (T-STRM-5) | Common edge case; empty volume backup must not crash | Add to `backup_workflow_test.go` (or a new file): snapshot before any writes |
| P0 | **No security/auth e2e tests** (T-SEC-2 through T-SEC-8) | Auth/authz testing is a likely graduation requirement per the KEP | Create `security_test.go`: wrong audience, expired token, unauthorized SA, cross-namespace, TLS validation (requires raw gRPC infrastructure) |
| P0 | **No sidecar error validation** (T-ERR-1 through T-ERR-7) | API contract violations if not enforced | Create `sidecar_errors_test.go`: send raw gRPC calls with missing fields, verify error codes (requires raw gRPC infrastructure) |
| P1 | **No client cancellation test** (T-STRM-6) | gRPC streaming contract requirement | Add to `error_handling_test.go`: cancel context mid-stream |
| P2 | **No `starting_offset > capacity` test** (T-ERR-14) | OutOfRange error path untested | Add to `block_metadata_properties_test.go` |
| P2 | **No multi-write-same-block test** (T-DATA-6) | Data correctness edge case | Add to `backup_workflow_test.go`: overwrite block, verify single delta |
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
| 1 | `base_snapshot_id` in delta is a **CSI handle**, not a K8s VolumeSnapshot name (introduced in sidecar v0.2.0; earlier sidecars used `BaseSnapshotName`) | Tests must use `GetChangedBlocksByID` (handle-based). Our suite handles this correctly. Sidecars older than v0.2.0 break delta calls; this suite's validated workaround image is `registry.k8s.io/sig-storage/csi-snapshot-metadata:v0.2.0` (see [results.md](./results.md)). The sidecar only serves gRPC, so a v0.2.0 sidecar works with a v1beta1 SMS CR. |
| 2 | Sidecar does **NOT validate** same-volume for delta requests | Cross-volume errors come from CSI driver, not sidecar. Error code may differ. |
| 3 | Sidecar does **NOT validate** snapshot ordering | Reversed-order errors come from CSI driver, not sidecar. Error code may differ. |
| 4 | `SnapshotMetadataService` CRD is **v1beta1** since external-snapshot-metadata v1.0.0 (v1alpha1 removed, no conversion, schema unchanged); the KEP reached beta in Kubernetes v1.36 with no feature gate (out-of-tree) | The test client reads the v1beta1 SMS CR itself. OCP <= 4.22 ships the v1alpha1 CRD, so the v1beta1 CRD must be applied manually there (OCP 5.0 ships v1beta1). |
| 5 | KEP says `NotFound` for missing snapshots, sidecar returns `Unavailable` | Error code assertions should use `Unavailable` (14), not `NotFound` (5). |

---

## 11. Monitoring & Observability

| Req ID | Upstream Requirement | Test File | Status |
|--------|---------------------|-----------|--------|
| T-MON-1 | `snapshot_metadata_controller_operation_total_seconds` histogram metric | — | **GAP** |
| T-MON-2 | `/health` endpoint returns `ServiceUnavailable` until gRPC ready | — | **GAP** |

---

## Relationship to Velero Coverage

This document focuses on **upstream K8s/CSI requirements**. The companion
[velero-coverage.md](./velero-coverage.md) maps **Velero Block Data Mover**
([PR #9528](https://github.com/velero-io/velero/pull/9528), merged; Velero 1.19.0 in development) requirements. Key overlaps:

| Area | K8s Coverage (this doc) | Velero Coverage |
|------|------------------------|-----------------|
| Handle-based delta | T-API-2, T-SNAP-2 | Handle retention, Type 1 vs Type 2 |
| Backup workflow | T-BKP-1 through T-BKP-5 | Full + incremental chain simulation |
| Error fallback | T-ERR-8, T-ERR-9 | Any CBT error degrades changed -> allocated -> full |
| Snapshot retention | T-SNAP-2 | **Not supported in 1.19.0.** Class-based `Retain` was rolled back ([velero#10753](https://github.com/velero-io/velero/pull/10753), merged 2026-10-29); Case 2 retention is proposed for a 1.19.x patch ([velero#10744](https://github.com/velero-io/velero/issues/10744)). Until then Ceph RBD deltas fall back |
| Ceph-specific (450/250 snapshot thresholds, flattening, omap proposals) | Not in upstream spec | Velero-specific coverage |

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
