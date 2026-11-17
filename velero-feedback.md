# Velero Block Data Mover - Ceph RBD CBT Feedback

This document captures what the Velero Block Data Mover (BDM) design
([PR #9528](https://github.com/velero-io/velero/pull/9528)) and implementation need to
support Ceph RBD Changed Block Tracking (CBT) via CephCSI, and which of the original
asks have since been resolved, implemented differently, or remain open. Status is as of
2026-10-27. Velero 1.19.0 is in development and **not released** (latest release:
v1.18.4), so no release date should be assumed.

Companion documents: [velero-coverage.md](./velero-coverage.md) (test mapping) and
[k8s-coverage.md](./k8s-coverage.md).

## Status at a Glance

| # | Topic | Status |
|---|-------|--------|
| 1 | Snapshot retention for Ceph RBD | **Not supported in 1.19.0.** The `VolumeSnapshotClass` `deletionPolicy` inheritance was rolled back ([#10753](https://github.com/velero-io/velero/pull/10753), merged 2026-10-29); a separate Case 2 design ([#10744](https://github.com/velero-io/velero/issues/10744)) targets a 1.19.x patch |
| 2 | ChangeId = CSI snapshot handle | Resolved: confirmed for Ceph and fixed on `main` ([#10307](https://github.com/velero-io/velero/pull/10307)); fix not in 1.18.x |
| 3 | Fallback when the delta cannot be computed | Implemented (degrades changed -> allocated -> full); Ceph-specific documentation remains open |
| 4 | 1 MiB chunk alignment | Compatibility note; unchanged |
| 5 | Volume Policy examples for Ceph | Open (documentation); examples below use the implemented policy shape |
| 6 | SnapshotMetadataService prerequisite | Partly covered by the merged docs; Ceph/ODF setup guidance remains open |
| 7 | ROX access mode for the backup PVC | Configurable per storage class; Ceph-specific guidance remains open |

---

## 1. Snapshot Retention is Required for Ceph RBD

### Problem

Ceph RBD computes block deltas using `rbd snap diff`, which requires both the base and
target snapshots to exist as un-flattened RBD snapshots with a parent-child relationship
in the same clone chain. Deleting the VolumeSnapshot after backup upload completes (the
design's **Case 1**) removes the base snapshot, so `GetMetadataDelta` cannot compute a
diff against it. The design's **Case 2** (retain the snapshot) is required for Ceph.

### Status as of 2026-10-27

The 2026-10-27 Velero community meeting decided that **Velero 1.19.0 does not support
Case 2**:

- The class-inherit half of [velero-io/velero#10307](https://github.com/velero-io/velero/pull/10307)
  was rolled back ([#10753](https://github.com/velero-io/velero/pull/10753), merged 2026-10-29), so the backup
  `VolumeSnapshotContent` is always `Delete`, as in 1.18.
- The changeID fix from the same PR ([#10294](https://github.com/velero-io/velero/issues/10294))
  stays.
- Velero no longer reads the `VolumeSnapshotClass` `deletionPolicy` for data movement.
- The file system data mover never retains snapshots.

Consequence on Ceph RBD with 1.19.0: once the base snapshot has been deleted,
`GetMetadataDelta` fails (ceph-csi reports `no snap source in omap`) and the uploader
falls back to a non-CBT backup (`fallbackFull`). The backup data is still correct; only
the incremental saving is lost. This is the expected 1.19.0 behavior, not a driver
regression.

Case 2 is now a separate design targeting a 1.19.x patch: umbrella
[#10743](https://github.com/velero-io/velero/issues/10743), design
[#10744](https://github.com/velero-io/velero/issues/10744), and a draft design document
([base-snapshot-retention.md](https://github.com/kaovilai/velero/blob/design-base-snapshot-retention/design/block-data-mover/base-snapshot-retention.md)).
Everything in that draft beyond the meeting decisions is a proposal:

- Precedence: a VolumePolicy parameter (working name `retainBaseSnapshot`) overrides
  autodetection from the SnapshotMetadataService `spec.requireBaseSnapshotForDelta`
  field, which overrides the default of not retaining. The parameter name is not
  settled.
- Velero owns the Retain lifecycle instead of reading the VolumeSnapshotClass.
- Keep one retained snapshot per volume chain: delete the previous one after the next
  backup succeeds, and delete it when its backup is deleted.
- Failed, canceled, or interrupted data movement never leaves a retained snapshot.
- KEP-3314 places the "does this driver need the base snapshot" decision on the backup
  application; `requireBaseSnapshotForDelta` is the discovery mechanism.

Timeline: Ceph CBT is deferred, not dropped. It remains in the 1.19 stream (1.19.x) and
is **targeted** for OADP 1.7; that is a target, not a commitment.

### The Withdrawn Mechanism (Pre-Rollback Builds)

Builds between [#10307](https://github.com/velero-io/velero/pull/10307) and the rollback
made the backup `VolumeSnapshotContent` inherit `DeletionPolicy` from the source
VolumeSnapshotContent / `VolumeSnapshotClass`. This is the mechanism the results below
were produced with, and it is **withdrawn for 1.19.0**. It was also incomplete: nothing
tracked or deleted the retained snapshot, so snapshots leaked
([#9835](https://github.com/velero-io/velero/issues/9835),
[#10304](https://github.com/velero-io/velero/issues/10304)). The class used in those
tests looked like this:

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

Do not plan new deployments on this mechanism. Under 1.19.0 the class is ignored for
data movement, and under pre-release builds it retained without managing deletion.

### What Was Validated (Pre-Rollback Builds, Base Retained)

These results were obtained with a retained base snapshot on pre-release Velero builds
(OCP with Rook, and ODF):

- End-to-end CBT produced real incrementals, for example a delta of exactly
  150994944 bytes.
- Whole-device sha256 matched at every restore point, VMs booted, and multi-disk VMs
  including a hotplugged disk matched checksums.
- In-place incremental restore (`existingVolumeDataPolicy: incremental`) moved
  270532608 bytes for a 30 GiB volume in 22 s.
- Counter-example: with the base snapshot deleted (a `Delete` class), ceph-csi failed
  with `no snap source in omap` and the backup fell back to full. This is the same
  path 1.19.0 takes by default.

### Remaining Asks

- **Case 2 design and implementation**: settle the configuration source, the tracking
  record, rotation, and deletion semantics in
  [#10744](https://github.com/velero-io/velero/issues/10744), then land it in a 1.19.x
  patch. [#10691](https://github.com/velero-io/velero/pull/10691) is the draft
  candidate implementation.
- **Documentation**: the block data mover docs (`csi-snapshot-data-movement.md`,
  updated by [#10537](https://github.com/velero-io/velero/pull/10537)) should state
  that 1.19.0 does not retain base snapshots for data movement, that Ceph RBD
  incrementals fall back until Case 2 lands, and how to configure retention once the
  parameter exists.
- **Autodetection**: read `requireBaseSnapshotForDelta` from the SnapshotMetadataService
  ([velero#10722](https://github.com/velero-io/velero/issues/10722)) after an
  external-snapshot-metadata release includes it, and have Ceph RBD set it to `true`
  ([ceph-csi#6680](https://github.com/ceph/ceph-csi/issues/6680)).

### Impact

- **Storage overhead**: A retained base snapshot consumes Ceph pool space (COW
  extents). The proposed one-per-chain rule bounds this to one extra snapshot per
  volume chain, versus one per backup under the withdrawn mechanism.
- **Self-defeating accumulation**: CephCSI flattens when an image reaches
  `maxSnapshotsOnImage` (default 450) and flattens down to `minSnapshotsOnImage`
  (default 250); these are CephCSI defaults, not a hard limit of 250. Retained or
  leaked snapshots count toward the threshold, and flattening breaks the clone chain
  CBT depends on. CephCSI's priority-based flattening (deleted snapshots first, then
  clones, then live snapshots) is a **proposal and is not implemented**; current
  behavior is threshold-based only.
- **Recovery**: after flattening breaks the chain, the next backup degrades
  automatically and subsequent backups resume incrementally from the new base
  (`incremental -> incremental -> [flatten] -> full -> incremental`). Backup data
  already written to the repository is independent of the source RBD images.

---

## 2. ChangeId = CSI SnapshotHandle for Ceph

### How CephCSI Maps ChangeId

For CephCSI, `VolumeSnapshotContent.Status.SnapshotHandle` is the ChangeId. The handle
has the format:

```
<clusterID>-<pool>-<imageID>-<snapID>
```

This encodes the RBD pool, image, and snapshot uniquely. No separate ChangeId mapping is
needed. The CBT API is snapshot-oriented (no `volume_id`); the handle is passed as
`snapshot_id` / `base_snapshot_id`, and CephCSI resolves it internally through the RADOS
OMAP journal to the RBD image and snapshot.

### Status

- Velero persists the change ID and volume ID in the backup
  ([#9863](https://github.com/velero-io/velero/pull/9863)).
- [#9714](https://github.com/velero-io/velero/issues/9714) (verify ChangeId for Ceph)
  is closed as completed.
- [#10294](https://github.com/velero-io/velero/issues/10294): changeID retrieval
  returned empty for all non-vSphere CSI drivers (including Ceph RBD), so every
  "incremental" silently ran as a full backup. Fixed by
  [#10307](https://github.com/velero-io/velero/pull/10307) (merged 2026-09-09; reads
  `backupVSC.Spec.Source.SnapshotHandle`). That PR's live Ceph/ODF validation moved a
  20 MiB delta as exactly 20,971,520 bytes. The fix is on `main`/v1.19 and **not** in
  1.18.x, so Ceph users on 1.18.x do not get real incrementals from the block data
  mover.
- `GetMetadataDelta` accepts the CSI handle rather than a VolumeSnapshot name, so delta
  computation works after the base VolumeSnapshot object is deleted as long as the
  underlying RBD snapshot is retained.

---

## 3. Flattened Snapshot Fallback

### Problem

When a Ceph RBD snapshot's clone chain is broken by flattening, `rbd snap diff` cannot
compute deltas, and CephCSI has no driver-level fallback: `GetMetadataDelta` returns an
error. CephCSI's current CBT implementation
([ceph/ceph-csi#5347](https://github.com/ceph/ceph-csi/pull/5347)) uses
`rbd DiffIterateByID` directly and requires intact chains. Storing diffs in omap when
flattening (to extend CBT beyond the snapshot thresholds) is a **proposal only**: no
PRs, issues or design docs exist for it in ceph/ceph-csi.

### Velero Behavior

Velero implements the fallback. In `pkg/uploader/cbt/set.go`
([#9736](https://github.com/velero-io/velero/pull/9736), merged), `SetBitmapOrFull`
degrades **changed blocks -> allocated blocks -> full**:

1. If `GetChangedBlocks` returns an error, Velero falls through to
   `GetAllocatedBlocks` (an allocated-blocks backup, still reported as a fallback).
2. A full backup (`SetFull()`) is used only when the service is nil, the
   snapshot/changeID is empty, or both calls fail.
3. Any CBT service error, missing changeID or missing parent triggers this path; it is
   not limited to specific gRPC codes. The backup description shows
   `Backup Type: Incremental (fallen back to Full)` and the reason is recorded in the
   DataUpload `status.activities`.
4. The new snapshot becomes the base for future incrementals, so the chain is
   re-established.

### Remaining Ask

Document the Ceph-specific causes of fallback (flattening at `maxSnapshotsOnImage`,
missing retention) so operators can recognize why an incremental chain broke, and
consider surfacing the failing gRPC status in the fallback reason.

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

Volume Policies select the data mover per volume through the `snapshot` action's
`parameters.dataMover` (`velero-block` or `velero-fs`). They do not carry a retention
parameter; retention is set on the `VolumeSnapshotClass` (see section 1).

### Recommended Volume Policy for RBD (Block Data Mover)

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: volume-policy
  namespace: velero
data:
  policy.yaml: |
    version: v1
    volumePolicies:
      # Ceph RBD: block data mover (retention is set via the VolumeSnapshotClass)
      - conditions:
          csi:
            driver: openshift-storage.rbd.csi.ceph.com
        action:
          type: snapshot
          parameters:
            dataMover: velero-block
      # CephFS: file system data mover (CBT not applicable)
      - conditions:
          csi:
            driver: openshift-storage.cephfs.csi.ceph.com
        action:
          type: snapshot
          parameters:
            dataMover: velero-fs
```

The ConfigMap is referenced with `velero backup create --resource-policies-configmap
volume-policy`. The backup itself enables data movement with
`--snapshot-move-data --data-mover velero-block`; there is no alpha feature flag, but
the CSI plugin must be enabled.

### Why CephFS Uses the FS Mover

CephFS does not support block-level snapshots or `rbd snap diff`. CBT is only applicable
to RBD volumes. CephFS volumes should continue using the file system data mover.

### Remaining Ask

Add a Ceph RBD example to the Velero docs once the Case 2 retention parameter is
accepted, showing that parameter plus the policy above. The current docs page only shows
an EBS/EFS example, and the `VolumeSnapshotClass` `deletionPolicy` path must not be
documented as the Ceph mechanism.

---

## 6. SnapshotMetadataService Sidecar Deployment

### Prerequisite for CBT

The CSI Changed Block Tracking feature requires the `external-snapshot-metadata`
sidecar in the CSI driver's controller plugin pod. The roles are split as follows:

1. The **sidecar** proxies gRPC calls from backup applications to the CSI driver,
   authenticates callers via Kubernetes service account tokens, and serves TLS.
2. The **`SnapshotMetadataService` CR** (cluster-scoped, named after the CSI driver, e.g.
   `openshift-storage.rbd.csi.ceph.com`) tells clients where to connect (address, CA
   cert, audience). It is created by the cluster admin or a driver operator; the sidecar
   does not create it.
3. For ODF/Ceph, the `ceph-csi-operator` injects the sidecar into the controller plugin
   when the Driver CR's `controllerPlugin.volumes` contains a volume named `tls-key`
   (documented in ceph-csi-operator `docs/features/rbd-snapshot-metadata.md`, Beta).
   On OCP 5.0 / ODF 5.0 the `ocs-client-operator` also automates the Service, TLS
   secret and `tls-key` volume, but not the SnapshotMetadataService CR.

### What the Velero Docs Cover

The merged docs page
([#10537](https://github.com/velero-io/velero/pull/10537)) lists, as block data mover
prerequisites, that the CSI driver must support the Kubernetes CBT API
(`SnapshotMetadataService`), and documents `csiSnapshotMetadataServiceConfigs.saName`
in the `node-agent-config` ConfigMap for services that need a dedicated ServiceAccount.
It does not describe how to verify the SMS CR exists, does not describe
Ceph/ODF-specific setup, and does not mention snapshot retention.

### Remaining Asks

- **Pre-flight check**: before a CBT backup, verify the `SnapshotMetadataService` CR
  for the driver exists, and report a clear error when it is missing ("CSI driver does
  not support Changed Block Tracking") rather than only degrading silently.
- **Ceph/ODF setup pointer**: link to the ceph-csi-operator RBD snapshot-metadata
  documentation and note that, as of Oct 2026, CBT is Developer Preview on OCP/ODF
  4.20-4.22 (KCS 7130698 is written for ODF 4.20; 4.21/4.22 assumed) and Technology Preview on OCP/ODF 5.0 (not GA anywhere).
- **ODF sidecar image caveat**: early ODF 4.21 builds ship an older sidecar that uses
  `BaseSnapshotName` instead of `BaseSnapshotId`, which breaks delta calls; see
  `ocp-setup/step4-cbt-sidecar.sh` for the workaround validated by this suite.

### Version Requirements

| Component | Minimum Version |
|-----------|----------------|
| Kubernetes | v1.33 (alpha); beta in v1.36 (KEP-3314) |
| CSI spec | v1.10.0 (SnapshotMetadata service) |
| external-snapshot-metadata | v1.0.0 for the v1beta1 API (v0.2.0 is the last release built against the v1alpha1 SMS API; it works with a v1beta1 CRD in this suite because the tests read the SMS CR directly); v1.1.0 is the latest release |
| external-snapshotter | v8.0.0 |
| Ceph | Pacific (16.2.0), the ceph-csi README minimum for RBD; no CBT-specific minimum is documented. This suite last ran against Ceph 20.1.0 |

(The kubernetes-csi documentation table lists a higher minimum, K8s 1.36 / CSI 1.12,
than the KEP's 1.33 / 1.10.)

### KEP-3314 Status

KEP-3314 (`kubernetes/enhancements`) reached **beta in Kubernetes v1.36**
(`kep.yaml`: stage beta, alpha v1.33, beta v1.36; stable is not set and no GA target
exists). Key notes for Velero:

- **No feature gate**: this is an out-of-tree feature. Enablement is via CRD + sidecar
  deployment, not a Kubernetes feature gate.
- **API graduated to v1beta1**: the `SnapshotMetadataService` CRD is
  `cbt.storage.k8s.io/v1beta1` in external-snapshot-metadata v1.0.0; v1alpha1 was
  removed with no conversion and an unchanged schema.
- **Metric**: `snapshot_metadata_controller_operation_total_seconds` with labels
  `operation_status` (Success/Failure) and `operation_name`
  (MetadataAllocated/MetadataDelta). Velero could surface or correlate these metrics.
- OCP ships the v1alpha1 CRD on 4.21/4.22 (v1beta1 only on 5.0), so on <= 4.22 the
  v1beta1 CRD must be applied manually.

---

## 7. ROX PVC Compatibility with CSI Snapshot Exposer

### Current Behavior

Velero's CSI Snapshot Exposer creates a `backupPVC` to expose snapshot data to the data
mover. For the block data mover the `backupPVC` is always `volumeMode: Block`. Its
access mode is `ReadWriteOnce` by default and can be set to `ReadOnlyMany` per storage
class through the `backupPVC` ConfigMap (`readOnly: true`; see the Velero
`data-movement-backup-pvc-configuration` docs). On SELinux-enabled clusters such as
OpenShift, `readOnly: true` also requires `spcNoRelabeling: true`, and the storage
class must support creating a `ReadOnlyMany` PVC from a snapshot or the DataUpload
stays in `Accepted`.

### Ceph Relevance

For Ceph RBD, a read-only restore avoids a full clone and, in current CephCSI, avoids
flattening the parent chain that CBT depends on:

- ceph-csi [#6534](https://github.com/ceph/ceph-csi/pull/6534) (v3.18.0) skips
  flattening when all volume capabilities of a snapshot restore are read-only. ODF
  builds with an older ceph-csi still flatten.
- RBD **shallow read-only volumes** (as in CephFS,
  [ceph-csi#3651](https://github.com/ceph/ceph-csi/pull/3651)) and **counter-based
  snapshot deletion** (CephFS has reference tracking,
  [ceph-csi#2893](https://github.com/ceph/ceph-csi/pull/2893)) remain proposals for
  RBD; no PR, issue or design exists.
- Real clones remain for read-write restores.

### Remaining Ask

Document that `readOnly: true` for the Ceph RBD storage class is recommended for the
block data mover (with `spcNoRelabeling` on OpenShift) and requires a ceph-csi that
skips flattening for read-only restores to be fully effective. Velero does not choose
`ReadOnlyMany` automatically. This suite's ROX tests (`rox_pvc_test.go`,
`flattening_prevention_test.go`) have not been validated against ceph-csi >= 3.18.

---

## Summary of Remaining Changes

| # | Change | Priority | Velero Component |
|---|--------|----------|-----------------|
| 1 | Design and land Case 2 retention for storage that needs the base snapshot ([#10743](https://github.com/velero-io/velero/issues/10743), [#10744](https://github.com/velero-io/velero/issues/10744)); target 1.19.x | **Critical** | Block data mover, design |
| 2 | Track and delete retained snapshots (rotation after the next backup, deletion with the backup); [#10691](https://github.com/velero-io/velero/pull/10691) is the draft candidate | **High** | Backup deletion, DataUpload controller |
| 3 | Backport or document the [#10307](https://github.com/velero-io/velero/pull/10307) changeID fix for 1.18.x users (the class-inherit part is withdrawn) | **High** | Release notes / backport |
| 4 | Document Ceph-specific fallback causes; surface the failing status in the fallback reason | **Medium** | Block Data Mover, documentation |
| 5 | Ceph RBD Volume Policy example once the retention parameter is accepted | **Medium** | Documentation, examples |
| 6 | Pre-flight check and docs for the SnapshotMetadataService prerequisite (Ceph/ODF setup) | **High** | Documentation, pre-flight checks |
| 7 | Document read-only `backupPVC` for Ceph RBD (and the ceph-csi >= 3.18 dependency) | **Medium** | Documentation |
| 8 | Document 1 MiB chunk alignment | **Low** | Documentation |
| 9 | Bound block data mover write concurrency and set a Go soft memory limit; on a 96 vCPU node the default OOMed ([#10724](https://github.com/velero-io/velero/issues/10724), closed as completed; [#10740](https://github.com/velero-io/velero/pull/10740) merged 2026-10-28) | **High** | Block data mover |
| 10 | Do not block the single DataDownload worker while waiting for a `WaitForFirstConsumer` PVC ([#10730](https://github.com/velero-io/velero/issues/10730); workaround `restorePVC.ignoreDelayBinding: true`) | **High** | DataDownload controller |
