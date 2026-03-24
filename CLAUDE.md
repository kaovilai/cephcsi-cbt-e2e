# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

E2E test suite for **Ceph RBD Changed Block Tracking (CBT)** via the Kubernetes CSI external-snapshot-metadata API. Tests validate incremental backup capabilities (GetMetadataAllocated, GetMetadataDelta) needed by Velero and similar backup tools.

Requires a live Kubernetes 1.33+ cluster with CephCSI, ODF/Rook, and the external-snapshot-metadata sidecar deployed.

## Build and Test Commands

```bash
make build              # Compile all packages
make lint               # golangci-lint run ./...
make e2e                # Full suite (5h timeout)
make e2e-fast           # Skip slow + stored-diffs tests (2h timeout)
make e2e-basic          # Basic CBT tests only (30m)
make e2e-rox            # ReadOnlyMany PVC tests (30m)
make e2e-rox-deletion   # Counter-based deletion tests (30m)
make e2e-flattening     # Flattening prevention tests (30m)
make e2e-priority       # Priority flattening - slow (3h)
make e2e-stored-diffs   # Stored diffs fallback (1h)
make e2e-errors         # Error handling tests (30m)
make e2e-backup         # Backup workflow tests (1h)
```

Override cluster defaults via environment variables:
```bash
STORAGE_CLASS=my-sc SNAPSHOT_CLASS=my-snapclass CEPHCSI_NAMESPACE=rook-ceph make e2e-basic
```

Run a single test by description:
```bash
go run github.com/onsi/ginkgo/v2/ginkgo -v --focus='should return allocated blocks' ./tests/e2e/... -- --storage-class=ocs-storagecluster-ceph-rbd --snapshot-class=ocs-storagecluster-rbdplugin-snapclass
```

## Architecture

```
pkg/
├── cbt/    # gRPC client wrapping external-snapshot-metadata iterator API
│           # GetAllocatedBlocks, GetChangedBlocks, GetChangedBlocksByID
├── k8s/    # Kubernetes resource lifecycle helpers (PVC, Pod, Snapshot, Namespace)
│           # Pod exec, toolbox pod discovery, wait utilities
├── data/   # Block device data operations: known-pattern writes (dd),
│           # SHA256 hash reads, block-level verification against CBT metadata
└── rbd/    # Ceph RBD introspection via toolbox pod exec
            # Image info, parent/clone chain, omap metadata, Ceph version checks
tests/e2e/  # Ginkgo v2 BDD test suite (Ordered test containers)
config/     # StorageClass and VolumeSnapshotClass YAML manifests
```

## Test Patterns

- Tests use **Ginkgo v2** with `Ordered` containers: `BeforeAll` sets up resources, `It` blocks run assertions, `AfterAll` cleans up in reverse order.
- Gomega matchers are dot-imported (`. "github.com/onsi/gomega"`).
- Shared state (clients, config flags) lives in `e2e_suite_test.go` as package-level vars initialized in `init()` and `TestCephCSICBT`.
- `BeforeSuite` validates cluster preconditions (K8s version, CRDs, CephCSI pods, sidecar, Ceph version).
- Ginkgo labels `slow` and `stored-diffs` categorize long-running tests for filtering.

## Key Domain Concepts

- **CBT (Changed Block Tracking)**: Reports which blocks were allocated or changed between snapshots at the block-device level via `rbd snap diff`.
- **GetMetadataAllocated**: All allocated blocks in a single snapshot.
- **GetMetadataDelta**: Blocks changed between two snapshots (requires both snapshots in the RBD clone chain).
- **Flattening**: CephCSI may flatten snapshots (collapse clone chain), which breaks delta computation. The "Combined solution" stores diffs in omap before flattening as a fallback.
- **250-snapshot limit**: CephCSI enforces a per-image snapshot cap with priority-based eviction.
- **SnapshotMetadataService CRD**: Stays at `v1alpha1` (out-of-tree API), does not graduate with the K8s beta milestone.

## ODF Version Compatibility

The test suite handles multiple ODF versions for pod/label discovery:
- ODF < 4.18: label `app=csi-rbdplugin-provisioner`
- ODF 4.18+: label `app.kubernetes.io/name=csi-rbdplugin,app.kubernetes.io/component=ctrlplugin`
- ODF 4.21+: fallback to pod name pattern matching (`rbd` + `ctrlplugin`)

## OpenShift Cluster Setup (csi-driver-host-path)

See `openshift-instruction.txt` for the full walkthrough. Summary of required steps on an OCP 4.20+ cluster:

1. **Enable DevPreviewNoUpgrade** (makes cluster non-upgradable):
   ```
   oc patch featuregate cluster --type=merge -p '{"spec":{"featureSet":"DevPreviewNoUpgrade"}}'
   ```
   This creates the `snapshotmetadataservices.cbt.storage.k8s.io` CRD.

2. **Create ClusterRoles**: `external-snapshot-metadata-client-runner` (for client tools) and `external-snapshot-metadata-runner` (for the sidecar).

3. **Create Service and SnapshotMetadataService**:
   - Service `csi-snapshot-metadata` in `openshift-cluster-csi-drivers` with annotation `service.beta.openshift.io/serving-cert-secret-name: csi-snapshot-metadata-certs` to auto-generate TLS certs.
   - Extract the TLS cert from the generated secret and embed it in the `SnapshotMetadataService` CR's `caCert` field.
   - The `audience` field must match the token audience used by the gRPC client.

4. **Create CSIDriver, StorageClass, VolumeSnapshotClass** for `hostpath.csi.k8s.io`.

5. **Create ServiceAccount and ClusterRoleBindings** binding `csi-hostpathplugin-sa` to OpenShift CSI roles (attacher, provisioner, resizer, snapshotter, snapshot-metadata).

6. **Deploy CSI driver StatefulSet** (`csi-hostpathplugin`) in `openshift-cluster-csi-drivers` with the `csi-snapshot-metadata` sidecar container mounting the TLS cert secret. The hostpath plugin must be started with `--enable-snapshot-metadata`.

7. **Test with snapshot-metadata-lister**:
   ```
   # Allocated blocks for a snapshot
   oc exec -n testns -c tools snapshot-metadata-tools -- /tools/snapshot-metadata-lister -n testns -s test-snapshot1
   # Delta between two snapshots
   oc exec -n testns -c tools snapshot-metadata-tools -- /tools/snapshot-metadata-lister -n testns -p test-snapshot1 -s test-snapshot2
   ```

Key images used:
- CSI hostpath plugin: `registry.k8s.io/sig-storage/hostpathplugin:v1.17.0`
- Snapshot metadata sidecar: `quay.io/openshift/origin-csi-external-snapshot-metadata:latest` (also available in OCP release payload as `csi-external-snapshot-metadata`)

References:
- [KEP-3314: CSI Changed Block Tracking](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md)
- [external-snapshot-metadata deployment](https://github.com/kubernetes-csi/external-snapshot-metadata/blob/main/deploy/README.md)
- [SnapshotMetadataService API types](https://github.com/kubernetes-csi/external-snapshot-metadata/blob/main/client/apis/snapshotmetadataservice/v1alpha1/types.go)
