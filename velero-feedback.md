# Velero Block Data Mover (PR #9528) - Ceph RBD CBT Accommodations

This document identifies specific accommodations needed in Velero's Block Data Mover
design (PR #9528) to support Ceph RBD Changed Block Tracking (CBT) via the CephCSI
Combined solution (RHSTOR-6095).

---

## 1. Snapshot Retention is Required for Ceph RBD

### Problem

Ceph RBD computes block deltas using `rbd snap diff`, which requires both the base and
target snapshots to exist as un-flattened RBD snapshots with a parent-child relationship
in the same clone chain. Velero's default behavior—deleting the VolumeSnapshot after
backup upload completes—breaks the incremental chain because `GetMetadataDelta` cannot
compute a diff against a deleted snapshot.

### Required Accommodation

PR #9528's **Case 2 (`RetainSnapshot: true`)** MUST be the documented default for Ceph
RBD volumes. The Volume Policy should enforce:

```yaml
volumePolicies:
  - conditions:
      storageClass:
        - ocs-storagecluster-ceph-rbd
    action:
      type: block-data-mover
      parameters:
        retainSnapshot: "true"
```

Without this, each backup after the first will fall back to a full backup since there is
no base snapshot to diff against.

### Impact

- **Storage overhead**: Retained snapshots consume Ceph pool space (COW extents). The
  CephCSI Combined solution's priority-based flattening manages this by enforcing a
  250-snapshot limit per image with intelligent eviction.
- **Velero garbage collection**: Velero must clean up retained snapshots when the backup
  chain is pruned (e.g., when the oldest backup in a schedule is expired). The
  `VolumeSnapshotContent` with `DeletionPolicy: Retain` must eventually be cleaned up.

---

## 2. ChangeId = CSI SnapshotHandle for Ceph

### How CephCSI Maps ChangeId

For CephCSI, the `VolumeSnapshotContent.Status.SnapshotHandle` IS the ChangeId. The
handle has the format:

```
<clusterID>-<pool>-<imageID>-<snapID>
```

This encodes the RBD pool, image, and snapshot uniquely. No separate ChangeId mapping is
needed.

### Required Accommodation

Velero must preserve both the `snapshotHandle` and any ChangeId mapping. Specifically:

- When storing backup metadata, persist `VolumeSnapshotContent.Status.SnapshotHandle` as
  the ChangeId for future delta computations.
- The `GetMetadataDelta` API accepts `base_snapshot_id` (the CSI handle), not the
  VolumeSnapshot name. This allows delta computation even if the base VolumeSnapshot K8s
  object has been deleted, as long as the underlying RBD snapshot is retained.

---

## 3. Flattened Snapshot Fallback

### Problem

When a Ceph RBD snapshot is flattened (its clone chain is broken), `rbd snap diff` cannot
compute deltas. `GetMetadataDelta` will return an error for flattened snapshots unless
stored diffs are available.

### The Combined Solution's Mitigation

The CephCSI Combined solution stores diffs in Ceph omap when flattening occurs, allowing
`GetMetadataDelta` to return correct data even for flattened snapshots. However, this is
not guaranteed for:

- Snapshots flattened before the Combined solution was deployed
- Edge cases where omap writes fail

### Required Accommodation

Velero's fallback-to-full-backup logic must handle this gracefully:

1. When `GetMetadataDelta` returns a gRPC error (e.g., `FAILED_PRECONDITION` or
   `INVALID_ARGUMENT`), Velero should fall back to `GetMetadataAllocated` for a full
   backup.
2. The fallback should be logged clearly so operators can investigate why the incremental
   chain broke.
3. After a full backup fallback, the new snapshot becomes the base for future
   incrementals—the chain is re-established.

---

## 4. 1 MiB Chunk Alignment is Compatible

### Compatibility Confirmation

Ceph RBD's default object size is 4 MiB (`order 22`). The CSI SnapshotMetadata service
reports blocks at the RBD object granularity or finer. Velero's 1 MiB chunk size for
Kopia's Block Address Table (BAT) is a divisor of 4 MiB, so:

- CBT-reported blocks will always align to 1 MiB boundaries or be multiples thereof.
- No partial-block issues are expected.
- Kopia's `WriteAt` with BAT support can efficiently handle the block metadata.

### Recommendation

Document this compatibility in the Block Data Mover design. If a CSI driver reports
blocks smaller than 1 MiB, Velero should coalesce them to the nearest 1 MiB boundary to
match Kopia's BAT chunk size.

---

## 5. Volume Policy Examples for Ceph

### Recommended Volume Policy for RBD (Block Data Mover)

```yaml
apiVersion: velero.io/v1
kind: ConfigMap
metadata:
  name: volume-policy
  namespace: velero
  labels:
    velero.io/volume-policy: ""
data:
  policy.yaml: |
    version: v1
    volumePolicies:
      # Ceph RBD: use block data mover with snapshot retention
      - conditions:
          csi:
            driver: openshift-storage.rbd.csi.ceph.com
        action:
          type: block-data-mover
          parameters:
            retainSnapshot: "true"
      # CephFS: use filesystem data mover (CBT not applicable)
      - conditions:
          csi:
            driver: openshift-storage.cephfs.csi.ceph.com
        action:
          type: fs-data-mover
```

### Why CephFS Uses FS Mover

CephFS does not support block-level snapshots or `rbd snap diff`. CBT is only applicable
to RBD volumes. CephFS volumes should continue using the filesystem-level data mover
(Kopia file-based backup).

---

## 6. SnapshotMetadataService Sidecar Deployment

### Prerequisite for CBT

The CSI Changed Block Tracking feature requires the `external-snapshot-metadata` sidecar
to be deployed in the CSI driver's provisioner pod. This sidecar:

1. Creates and manages the `SnapshotMetadataService` Custom Resource
2. Proxies gRPC calls from backup applications to the CSI driver
3. Handles authentication via Kubernetes service account tokens
4. Provides TLS-secured communication

### Required Documentation in Velero

Velero should document:

- **Pre-requisite check**: Before attempting CBT-based backup, verify that the
  `SnapshotMetadataService` CR exists for the CSI driver. The CR name matches the CSI
  driver name (e.g., `openshift-storage.rbd.csi.ceph.com`).
- **Error messaging**: If the CR is missing, provide clear error: "CSI driver does not
  support Changed Block Tracking. Deploy the external-snapshot-metadata sidecar."
- **Operator deployment**: For ODF/Ceph, the `ceph-csi-operator` will manage sidecar
  deployment. Document that ODF 4.18+ with CBT patches is required.

### Version Requirements

| Component | Minimum Version |
|-----------|----------------|
| Kubernetes | v1.33 (alpha), v1.36 (beta target per KEP-3314 PR #5877) |
| CSI spec | v1.10.0 (SnapshotMetadata service) |
| external-snapshot-metadata | v0.2.0 |
| external-snapshotter | v8.0.0 |
| Ceph | v17.0 (Quincy) |

### Beta Considerations (KEP-3314)

The SnapshotMetadata feature is graduating to beta in Kubernetes v1.36 (KEP-3314,
kubernetes/enhancements#5877). Key notes for Velero:

- **No feature gate**: This is an out-of-tree feature. Enablement is via CRD + sidecar
  deployment, not a Kubernetes feature gate.
- **API stays v1alpha1**: The `SnapshotMetadataService` CRD remains at
  `cbt.storage.k8s.io/v1alpha1` even for beta. No migration to `v1beta1`.
- **New metric**: `snapshot_metadata_controller_operation_total_seconds` with labels
  `operation_status` (Success/Failure) and `operation_name`
  (MetadataAllocated/MetadataDelta). Velero should surface or correlate these metrics.
- **Beta requires 2+ CSI driver implementations**: This signals broader vendor support,
  making it safer for Velero to rely on the API.

---

## 7. ROX PVC Compatibility with CSI Snapshot Exposer

### Current Behavior

Velero's CSI Snapshot Exposer creates a `backupPVC` to expose snapshot data for the data
mover to read. Currently this PVC uses `ReadWriteOnce` access mode.

### Recommended Change for Ceph

For Ceph RBD, the backup PVC should use **ReadOnlyMany (ROX)** access mode when possible:

1. **Prevents flattening**: ROX PVCs from snapshots reference the original RBD snapshot
   without creating a full clone. This preserves the snapshot chain for CBT.
2. **Multiple readers**: ROX allows concurrent backup data readers without access mode
   conflicts.
3. **No write overhead**: Since the data mover only reads from the backup PVC, RWO
   provides no benefit over ROX.

### Suggested Implementation

```go
// When creating the backup PVC for block data mover with CBT-capable driver:
if driver.SupportsCBT() {
    backupPVC.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{
        corev1.ReadOnlyMany,
    }
}
```

### Caveat

Not all CSI drivers support ROX for block volumes. Velero should:
1. Check if the StorageClass/driver supports ROX access mode
2. Fall back to RWO if ROX is not supported
3. Document that ROX is recommended for Ceph RBD to prevent flattening

---

## Summary of Required Changes

| # | Change | Priority | Velero Component |
|---|--------|----------|-----------------|
| 1 | Default `retainSnapshot: true` for Ceph RBD | **Critical** | Volume Policy, Block Data Mover |
| 2 | Persist `snapshotHandle` as ChangeId | **Critical** | Backup metadata, Block Data Mover |
| 3 | Graceful fallback to full backup on delta error | **High** | Block Data Mover |
| 4 | Document 1 MiB chunk alignment | **Low** | Documentation |
| 5 | Volume Policy examples for Ceph | **Medium** | Documentation, Examples |
| 6 | Document SnapshotMetadataService prerequisite | **High** | Documentation, Pre-flight checks |
| 7 | Support ROX access mode for backup PVC | **Medium** | CSI Snapshot Exposer |
