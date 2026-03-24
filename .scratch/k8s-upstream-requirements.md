# Kubernetes Upstream CBT Requirements

Comprehensive extraction from KEP-3314, external-snapshot-metadata repo, CSI spec, and k/k.

## Sources Analyzed

1. **KEP-3314**: `kubernetes/enhancements/keps/sig-storage/3314-csi-changed-block-tracking/README.md`
2. **external-snapshot-metadata repo**: `kubernetes-csi/external-snapshot-metadata` (main branch)
3. **kubernetes/kubernetes**: Searched for CBT e2e tests -- **none found** in `test/e2e/storage/`
4. **Proto spec**: `kubernetes-csi/external-snapshot-metadata/proto/schema.proto`
5. **Sidecar implementation**: `pkg/internal/server/grpc/`, `pkg/internal/authn/`, `pkg/internal/authz/`

---

## 1. SnapshotMetadataService CRD Requirements

### CRD Spec (v1beta1)
- **Scope**: Cluster-scoped (`+kubebuilder:resource:scope=Cluster`)
- **Short name**: `sms`
- **API group**: `cbt.storage.k8s.io`
- **Version**: `v1beta1` (was v1alpha1, graduated)

### Required Fields (`SnapshotMetadataServiceSpec`)
| Field | Type | Description |
|-------|------|-------------|
| `audience` | `string` | Expected audience value in client authentication tokens |
| `address` | `string` | TCP endpoint address for gRPC service (host:port, no scheme) |
| `caCert` | `[]byte` | CA certificate bundle for TLS validation |

### Testable Requirements
- REQ-CRD-1: CRD must be cluster-scoped
- REQ-CRD-2: Exactly one SMS CR per CSI driver (named for the driver)
- REQ-CRD-3: `audience` field must be a non-empty string
- REQ-CRD-4: `address` field must be a valid host:port endpoint
- REQ-CRD-5: `caCert` must be a valid CA certificate for TLS
- REQ-CRD-6: CR must be discoverable by backup applications via CSI driver name
- REQ-CRD-7: Deleting the SMS CR disables the feature at runtime

---

## 2. gRPC API Contract (Proto Spec)

### Service Definition
```protobuf
service SnapshotMetadata {
  rpc GetMetadataAllocated(GetMetadataAllocatedRequest) returns (stream GetMetadataAllocatedResponse);
  rpc GetMetadataDelta(GetMetadataDeltaRequest) returns (stream GetMetadataDeltaResponse);
}
```

### Enum: BlockMetadataType
| Value | Number | Description |
|-------|--------|-------------|
| `UNKNOWN` | 0 | Default/unset |
| `FIXED_LENGTH` | 1 | Fixed-size blocks |
| `VARIABLE_LENGTH` | 2 | Variable-sized extents |

### Message: BlockMetadata
| Field | Number | Type | Requirement |
|-------|--------|------|-------------|
| `byte_offset` | 1 | `int64` | REQUIRED, 0-based |
| `size_bytes` | 2 | `int64` | REQUIRED, >0 |

### GetMetadataAllocatedRequest
| Field | Number | Type | Requirement |
|-------|--------|------|-------------|
| `security_token` | 1 | `string` | REQUIRED |
| `namespace` | 2 | `string` | REQUIRED |
| `snapshot_name` | 3 | `string` | REQUIRED |
| `starting_offset` | 4 | `int64` | REQUIRED (0 = start) |
| `max_results` | 5 | `int32` | OPTIONAL (0 = driver default) |

### GetMetadataAllocatedResponse
| Field | Number | Type | Requirement |
|-------|--------|------|-------------|
| `block_metadata_type` | 1 | `BlockMetadataType` | REQUIRED |
| `volume_capacity_bytes` | 2 | `int64` | REQUIRED |
| `block_metadata` | 3 | `repeated BlockMetadata` | OPTIONAL |

### GetMetadataDeltaRequest
| Field | Number | Type | Requirement |
|-------|--------|------|-------------|
| `security_token` | 1 | `string` | REQUIRED |
| `namespace` | 2 | `string` | REQUIRED |
| `base_snapshot_id` | 3 | `string` | REQUIRED |
| `target_snapshot_name` | 4 | `string` | REQUIRED |
| `starting_offset` | 5 | `int64` | REQUIRED (0 = start) |
| `max_results` | 6 | `int32` | OPTIONAL (0 = driver default) |

### GetMetadataDeltaResponse
| Field | Number | Type | Requirement |
|-------|--------|------|-------------|
| `block_metadata_type` | 1 | `BlockMetadataType` | REQUIRED |
| `volume_capacity_bytes` | 2 | `int64` | REQUIRED |
| `block_metadata` | 3 | `repeated BlockMetadata` | OPTIONAL |

---

## 3. Streaming & Pagination Requirements

- REQ-STREAM-1: Both RPCs use server-streaming (multiple response messages)
- REQ-STREAM-2: `block_metadata_type` must remain constant across ALL response messages in a stream
- REQ-STREAM-3: `volume_capacity_bytes` must remain constant across ALL response messages in a stream
- REQ-STREAM-4: BlockMetadata tuples must be in ascending `byte_offset` order across the entire stream
- REQ-STREAM-5: BlockMetadata tuples must be non-overlapping
- REQ-STREAM-6: For FIXED_LENGTH type, `size_bytes` must be constant across all tuples
- REQ-STREAM-7: `max_results` limits tuples per response message; driver may send fewer
- REQ-STREAM-8: `max_results=0` means driver determines appropriate default
- REQ-STREAM-9: `starting_offset` enables stream resumption from interrupted calls
- REQ-STREAM-10: Driver may round `starting_offset` to block/extent boundaries
- REQ-STREAM-11: Stream terminates with `io.EOF` on successful completion

---

## 4. Error Codes & Conditions

### Sidecar-Level Errors (from implementation)

| gRPC Code | Condition | Source |
|-----------|-----------|--------|
| `InvalidArgument` (3) | Missing `security_token` | `validateGetMetadata*Request` |
| `InvalidArgument` (3) | Missing `namespace` | `validateGetMetadata*Request` |
| `InvalidArgument` (3) | Missing `snapshot_name` | `validateGetMetadataAllocatedRequest` |
| `InvalidArgument` (3) | Missing `base_snapshot_id` | `validateGetMetadataDeltaRequest` |
| `InvalidArgument` (3) | Missing `target_snapshot_name` | `validateGetMetadataDeltaRequest` |
| `InvalidArgument` (3) | CSI driver name mismatch | snapshot driver != configured driver |
| `Unauthenticated` (16) | Invalid/expired security token | TokenReview fails |
| `PermissionDenied` (7) | User lacks namespace access | SubjectAccessReview denies |
| `Unavailable` (14) | VolumeSnapshot not found | K8s API 404 |
| `Unavailable` (14) | VolumeSnapshot not ready | `ReadyToUse` is nil or false |
| `Unavailable` (14) | Missing bound VolumeSnapshotContent | `BoundVolumeSnapshotContentName` nil |
| `Unavailable` (14) | VolumeSnapshotContent not found | K8s API 404 |
| `Unavailable` (14) | VolumeSnapshotContent not ready | Status not ready |
| `Unavailable` (14) | Missing snapshot handle | `SnapshotHandle` nil in content status |
| `Unavailable` (14) | Secret retrieval failure | Snapshotter credentials error |
| `Unavailable` (14) | CSI driver not ready | Driver validation timeout |
| `Internal` (13) | CSI driver stream error (non-status) | `statusPassOrWrapError` wraps |
| `Internal` (13) | Failed to send response to client | `clientStream.Send()` error |
| `Internal` (13) | Authentication processing failure | TokenReview API error |
| `Internal` (13) | Authorization processing failure | SAR API error |

### CSI Driver-Level Errors (from KEP-3314)

| gRPC Code | Condition |
|-----------|-----------|
| `InvalidArgument` (3) | Required fields missing or values invalid |
| `NotFound` (5) | Snapshot ID doesn't exist |
| `FailedPrecondition` (9) | CBT not enabled in storage subsystem |
| `OutOfRange` (11) | `starting_offset` exceeds volume capacity |
| `Aborted` (10) | Status errors from CSI driver are preserved |

### Error Wrapping Behavior
- `statusPassOrWrapError()`: If error is nil or already a gRPC Status with code != Unknown, pass through unchanged. Otherwise wrap with specified code.
- CSI driver status errors are passed through to the client (code preserved)
- Non-status CSI errors are wrapped as `Internal`

---

## 5. Security Requirements

### Authentication (TokenReview)
- REQ-SEC-1: Every request must include a `security_token`
- REQ-SEC-2: Token is validated via Kubernetes TokenReview API
- REQ-SEC-3: Token must be audience-scoped to the SMS CR's `audience` value
- REQ-SEC-4: Dual audience validation: response must contain at least one audience AND must match requested audience exactly
- REQ-SEC-5: On auth failure, return `Unauthenticated` (code 16)
- REQ-SEC-6: Tokens have configurable expiry (default 600 seconds)
- REQ-SEC-7: Tokens are bindable to Pod for additional lifetime constraints

### Authorization (SubjectAccessReview)
- REQ-SEC-8: After authentication, sidecar performs SubjectAccessReview
- REQ-SEC-9: SAR checks: verb=`get`, resource=`volumesnapshots`, group=`snapshot.storage.k8s.io`, version=`v1`, in the request's namespace
- REQ-SEC-10: User info (username, groups, UID, extra) passed from TokenReview to SAR
- REQ-SEC-11: On authz failure, return `PermissionDenied` (code 7)

### TLS
- REQ-SEC-12: All client-server gRPC communication must be TLS-encrypted
- REQ-SEC-13: Clients must validate server certificate using SMS CR's `caCert`
- REQ-SEC-14: Server loads TLS cert/key from configured paths (or env vars `TLS_CERT_PATH`, `TLS_KEY_PATH`)

### RBAC Requirements

#### Sidecar ClusterRole (`external-snapshot-metadata-runner`)
| API Group | Resource | Verbs |
|-----------|----------|-------|
| `cbt.storage.k8s.io` | `snapshotmetadataservices` | get, list, watch, create, update, patch, delete |
| `authentication.k8s.io` | `tokenreviews` | create, get |
| `authorization.k8s.io` | `subjectaccessreviews` | create, get |
| `snapshot.storage.k8s.io` | `volumesnapshots`, `volumesnapshotcontents` | get, list |

#### Client ClusterRole (`external-snapshot-metadata-client-runner`)
| API Group | Resource | Verbs |
|-----------|----------|-------|
| `snapshot.storage.k8s.io` | `volumesnapshots`, `volumesnapshotcontents` | get, list, watch |
| `cbt.storage.k8s.io` | `snapshotmetadataservices` | get, list |
| (core) | `serviceaccounts/token` | create, get |

---

## 6. Sidecar Validation Logic

### Snapshot Resolution Flow
1. Look up VolumeSnapshot by namespace + name via K8s API
2. Verify `ReadyToUse` is true (not nil, not false)
3. Get `BoundVolumeSnapshotContentName` (must not be nil)
4. Look up VolumeSnapshotContent by name
5. Verify VolumeSnapshotContent is ready
6. Extract `SnapshotHandle` from content status (must not be nil)
7. Verify snapshot's driver matches the configured CSI driver name

### Delta-Specific Resolution
- `base_snapshot_id` is passed directly as a CSI snapshot handle (no K8s lookup)
- `target_snapshot_name` goes through full resolution flow above
- **NOTE**: The sidecar does NOT validate that base and target snapshots belong to the same volume (per code analysis)
- **NOTE**: The sidecar does NOT validate snapshot ordering (per code analysis)

### Secret Handling
- Snapshotter credentials obtained via `getSnapshotterCredentials()`
- Secrets fetched from VolumeSnapshotClass parameters
- Passed as `secrets` map in CSI request

---

## 7. Iterator/Client Library Requirements

### `GetSnapshotMetadata()` Function (pkg/iterator)
- Entry point for consuming snapshot metadata streams
- Takes `Args` struct with all configuration

### Args Validation
- REQ-ITER-1: `Emitter` is required
- REQ-ITER-2: `Namespace` is required
- REQ-ITER-3: `SnapshotName` is required
- REQ-ITER-4: `SANamespace` and `SAName` must both be provided or both omitted
- REQ-ITER-5: `TokenExpirySecs` defaults to 600 if not specified
- REQ-ITER-6: Service account and CSI driver can be auto-derived from snapshot metadata

### IteratorEmitter Interface
```go
SnapshotMetadataIteratorRecord(recordNumber int, metadata IteratorMetadata) error
SnapshotMetadataIteratorDone(numberRecords int) error
```
- Emitter can abort enumeration by returning error
- `IteratorDone` called on successful completion

---

## 8. Tools (snapshot-metadata-lister / snapshot-metadata-verifier)

### snapshot-metadata-lister
- Three modes: allocated blocks, delta by name, delta by CSI handle
- Flags: `-n` namespace, `-s` snapshot, `-p` previous snapshot, `-P` previous snapshot ID
- Output: table or JSON (`-o` flag)
- Auth: kubeconfig, configurable service account, token expiry (default 600s)
- Supports `--starting-offset` and `--max-results`

### snapshot-metadata-verifier
- Verifies allocated blocks against actual block device content
- Verifies changed blocks between two snapshots against device content
- Requires source/target device paths (`-src`, `-tgt`)
- Opens block devices and compares via `VerifierEmitter`
- Same auth/connection flags as lister

---

## 9. Upstream E2E Test Coverage

### In kubernetes/kubernetes
**No CBT-specific e2e tests found.** No files in `test/e2e/storage/` reference snapshot_metadata, CBT, or changed_block_tracking.

### In kubernetes-csi/external-snapshot-metadata
**No e2e test directory exists.** Only unit tests in:
- `pkg/sidecar/sidecar_test.go` - Flag parsing, server lifecycle, TLS errors
- `pkg/iterator/iter_test.go`, `clients_test.go`, `emitters_test.go` - Iterator unit tests
- `pkg/internal/server/grpc/*_test.go` - Comprehensive unit tests for server logic
- `pkg/internal/authn/token_auth_test.go` - Token auth unit tests
- `pkg/internal/authz/sar_authorizer_test.go` - SAR auth unit tests

### Existing Unit Test Coverage (server/grpc)

#### GetMetadataAllocated Tests
- `TestGetMetadataAllocatedViaGRPCClient`: end-to-end gRPC with invalid args, invalid token, driver not ready, CSI stream errors (status vs non-status), success
- `TestValidateGetMetadataAllocatedRequest`: nil request, missing token, missing namespace, missing snapshot name, valid request
- `TestConvertToCSIGetMetadataAllocatedRequest`: snapshot not found, not ready, missing bound content, content not found, content not ready, missing handle, driver mismatch, secret failure
- `TestStreamGetMetadataAllocatedResponse`: CSI stream error, K8s send error, multiple responses, non-zero starting offset
- `TestGetMetadataAllocatedClientErrorHandling`: client context cancellation, server deadline exceeded

#### GetMetadataDelta Tests
- `TestGetMetadataDeltaViaGRPCClient`: same pattern as allocated (6 scenarios)
- `TestValidateGetMetadataDeltaRequest`: nil, empty, missing token/namespace/base_id/target_name, valid (7 scenarios)
- `TestConvertToCSIGetMetadataDeltaRequest`: 12 scenarios mirroring allocated + delta-specific
- `TestStreamGetMetadataDeltaResponse`: same pattern as allocated
- `TestGetMetadataDeltaClientErrorHandling`: cancellation, deadline

---

## 10. Graduation Criteria (from KEP-3314)

### Alpha (achieved)
- Approved CRDs and gRPC spec
- Handle gRPC requests without K8s API server load proportional to metadata size
- Initial e2e tests

### Beta (current target)
- Two different storage providers implement SnapshotMetadata service
- Two backup applications complete backup workflows
- Expanded e2e coverage with auth/authz testing
- Performance metrics from CSI driver maintainers

### GA
- CSI drivers ship SnapshotMetadata as standard component
- User feedback period completed
- Conformance tests for all GA endpoints

### Important Notes
- **No standard feature gate**: Optional out-of-tree CSI component
- **SnapshotMetadataService CRD stays out-of-tree**: Does not graduate with K8s beta milestone
- **VolumeSnapshot v1 required**: Snapshot controller v7.0+, no beta/alpha snapshot support

---

## 11. Monitoring & Observability

- REQ-MON-1: Metric `snapshot_metadata_controller_operation_total_seconds` with labels:
  - `operation_status`: "Success" or "Failure"
  - `operation_name`: "MetadataAllocated" or "MetadataDelta"
  - `grpc_status_code`: gRPC status code
  - `target_snapshot`: target snapshot name
  - `base_snapshot_id`: (delta only) base snapshot ID
- REQ-MON-2: Health endpoint (`/health`) returns ServiceUnavailable until gRPC server ready

---

## 12. Compatibility & Version Skew

- REQ-COMPAT-1: Old CSI driver without CBT returns gRPC `UNIMPLEMENTED` (12)
- REQ-COMPAT-2: Sidecar gracefully handles driver not implementing SnapshotMetadata
- REQ-COMPAT-3: VolumeSnapshot v1 objects required
- REQ-COMPAT-4: Kubernetes 1.20+ required (per deploy docs; KEP targets 1.33+ for beta)

---

## 13. Comprehensive Testable Requirements Checklist

### API Behavior (Happy Path)
- [ ] T-API-1: GetMetadataAllocated returns all allocated blocks for a snapshot
- [ ] T-API-2: GetMetadataDelta returns changed blocks between two snapshots
- [ ] T-API-3: Allocated blocks are in ascending byte_offset order
- [ ] T-API-4: Delta blocks are in ascending byte_offset order
- [ ] T-API-5: No overlapping block ranges in allocated response
- [ ] T-API-6: No overlapping block ranges in delta response
- [ ] T-API-7: block_metadata_type constant across all stream messages
- [ ] T-API-8: volume_capacity_bytes constant across all stream messages
- [ ] T-API-9: volume_capacity_bytes matches actual PVC size
- [ ] T-API-10: For FIXED_LENGTH, size_bytes is constant across all tuples
- [ ] T-API-11: starting_offset=0 returns from beginning
- [ ] T-API-12: starting_offset > 0 resumes from specified offset
- [ ] T-API-13: max_results limits tuples per response message
- [ ] T-API-14: max_results=0 returns all results (driver default)
- [ ] T-API-15: Stream terminates normally with EOF

### API Behavior (Data Correctness)
- [ ] T-DATA-1: Allocated blocks match actual written data regions
- [ ] T-DATA-2: Delta blocks match actual changed data regions between snapshots
- [ ] T-DATA-3: Unwritten regions are not reported as allocated
- [ ] T-DATA-4: Unchanged regions are not reported in delta
- [ ] T-DATA-5: Data at reported block offsets matches source snapshot content
- [ ] T-DATA-6: Multiple writes to same block appear as single delta entry

### Error Conditions
- [ ] T-ERR-1: Missing security_token returns InvalidArgument
- [ ] T-ERR-2: Missing namespace returns InvalidArgument
- [ ] T-ERR-3: Missing snapshot_name returns InvalidArgument
- [ ] T-ERR-4: Missing base_snapshot_id (delta) returns InvalidArgument
- [ ] T-ERR-5: Missing target_snapshot_name (delta) returns InvalidArgument
- [ ] T-ERR-6: Invalid/expired token returns Unauthenticated
- [ ] T-ERR-7: Unauthorized user returns PermissionDenied
- [ ] T-ERR-8: Non-existent snapshot returns Unavailable
- [ ] T-ERR-9: Snapshot not ready returns Unavailable
- [ ] T-ERR-10: VolumeSnapshotContent not found returns Unavailable
- [ ] T-ERR-11: Missing snapshot handle returns Unavailable
- [ ] T-ERR-12: CSI driver mismatch returns InvalidArgument
- [ ] T-ERR-13: CSI driver not ready returns Unavailable
- [ ] T-ERR-14: starting_offset > volume size returns OutOfRange (CSI level)
- [ ] T-ERR-15: CBT not enabled returns FailedPrecondition (CSI level)

### Security
- [ ] T-SEC-1: Valid audience-scoped token succeeds
- [ ] T-SEC-2: Wrong audience token fails with Unauthenticated
- [ ] T-SEC-3: Expired token fails with Unauthenticated
- [ ] T-SEC-4: User with volumesnapshot get permission succeeds
- [ ] T-SEC-5: User without volumesnapshot get permission fails with PermissionDenied
- [ ] T-SEC-6: Cross-namespace access denied (token scoped to different namespace)
- [ ] T-SEC-7: TLS connection required (plaintext rejected)
- [ ] T-SEC-8: Invalid CA cert fails connection
- [ ] T-SEC-9: RBAC: client can create serviceaccounts/token
- [ ] T-SEC-10: RBAC: client can get/list snapshotmetadataservices

### CRD Operations
- [ ] T-CRD-1: SMS CR can be created with valid spec
- [ ] T-CRD-2: SMS CR can be discovered by CSI driver name
- [ ] T-CRD-3: SMS CR deletion disables metadata service
- [ ] T-CRD-4: SMS CR address field points to working gRPC endpoint
- [ ] T-CRD-5: SMS CR caCert enables TLS verification

### Streaming & Resumption
- [ ] T-STRM-1: Large metadata response spans multiple stream messages
- [ ] T-STRM-2: Client can resume with starting_offset after interruption
- [ ] T-STRM-3: Resumed stream returns correct remaining blocks
- [ ] T-STRM-4: max_results pagination works correctly
- [ ] T-STRM-5: Empty snapshot returns empty block list
- [ ] T-STRM-6: Client context cancellation terminates stream

### Snapshot Lifecycle
- [ ] T-SNAP-1: Metadata available immediately after snapshot is ready
- [ ] T-SNAP-2: Metadata for deleted snapshot returns error
- [ ] T-SNAP-3: Delta between snapshots from different volumes returns error
- [ ] T-SNAP-4: Delta with target older than base returns error (if validated)
- [ ] T-SNAP-5: Metadata survives snapshot content migration

### Backup Workflow (End-to-End)
- [ ] T-BKP-1: Full backup: get allocated blocks, read and verify all data
- [ ] T-BKP-2: Incremental backup: get delta blocks, read and verify changed data
- [ ] T-BKP-3: Restore from full backup produces identical volume
- [ ] T-BKP-4: Restore from incremental backup produces correct volume state
- [ ] T-BKP-5: Multiple incremental backups in chain work correctly

### Compatibility
- [ ] T-COMPAT-1: Driver without CBT returns UNIMPLEMENTED
- [ ] T-COMPAT-2: VolumeSnapshot v1 objects work correctly
- [ ] T-COMPAT-3: Multiple concurrent metadata requests succeed

---

## 14. Key Differences: KEP Spec vs Implementation

1. **base_snapshot_id in Delta**: The KEP mentions both snapshots should be VolumeSnapshot names, but the proto uses `base_snapshot_id` (a CSI snapshot handle string), not a K8s VolumeSnapshot name. The sidecar passes it directly to the CSI driver without K8s lookup.

2. **Same-volume validation**: KEP requires both snapshots belong to the same volume for delta. The sidecar implementation does NOT validate this -- it's left to the CSI driver.

3. **Snapshot ordering validation**: KEP requires target snapshot created after base. The sidecar implementation does NOT validate this -- it's left to the CSI driver.

4. **CRD version**: KEP originally specified v1alpha1, but the current code uses v1beta1. The CLAUDE.md notes say "stays at v1alpha1" but the actual upstream code has graduated to v1beta1.

5. **Error code differences**: KEP specifies `NotFound` for missing snapshots, but sidecar returns `Unavailable`. The `NotFound` code would come from the CSI driver level.
