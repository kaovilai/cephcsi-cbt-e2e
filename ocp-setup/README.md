# OCP Cluster Setup for CBT E2E Testing

## Version Matrix (as of Oct 2026)

| Component | Status |
| --------- | ------ |
| OCP 4.21 | Kubernetes 1.34. Last environment validated by this repo (OCP 4.21.7, see [Cluster Info](#cluster-info)) |
| OCP 4.22 | GA 2026-06-09, Kubernetes 1.35. Ships `ExternalSnapshotMetadata` in `DevPreviewNoUpgrade` only |
| OCP 5.0 | Release candidate (not GA), Kubernetes 1.36. The release after 4.22 was renamed from 4.23 to 5.0 |
| ODF | 4.22 is followed by **ODF 5.0** (there is no ODF 4.23). Version checks in these scripts compare the major version first |
| CBT status in Red Hat products | Developer Preview on OCP/ODF 4.20-4.22 ([KCS 7130698](https://access.redhat.com/articles/7130698), written for ODF 4.20; 4.21/4.22 assumed to match); Technology Preview on OCP 5.0 ([KCS 7146879](https://access.redhat.com/solutions/7146879)); not GA |
| KEP-3314 upstream | Beta since Kubernetes 1.36 (alpha 1.33). Minimum Kubernetes for this suite: 1.33 |

Only OCP 4.21 / ODF 4.21 with the v0.2.0 sidecar workaround has been exercised end to end by this repo.
Nothing here has been run against OCP 4.22 or 5.0; behavior described for those versions is taken
from upstream source and docs.

## CNV-Deployer Build Parameters

This cluster was provisioned via cnv-deployer with the following parameters (last validated environment, OCP 4.21):

| Parameter | Value | Description |
| --------- | ----- | ----------- |
| CLUSTER_NAME | `mig-tkaovila-mar23` | No dots, underscores, max 20 chars |
| OCP_VERSION | `4.21` | OpenShift Container Platform version |
| CLUSTER_SIZE | `Standard` (70G) | Per-worker disk size: 70G Standard, 50G Small, 150G Medium, 250G Large |
| DEPLOY_CNV | unchecked | Not needed for CBT testing |
| DEPLOY_OCS | unchecked | ODF installed manually after featuregate |
| FEATURE_GATE | N/A | Applied manually as CustomNoUpgrade |
| CNV_VERSION | `4.21` | Irrelevant since CNV not deployed |
| DEFAULT_SC | `ocs-storagecluster-ceph-rbd-virtualization` | Available after ODF install |
| PREPARE_WINDOWS | unchecked | |
| FIPS_ENABLED | unchecked | |
| CLUSTER_TTL | `48` | Hours before auto-destruction |
| MIG_QE_AUTOMATION_BRANCH | `main` | |

### Why DEPLOY_OCS is unchecked

The `ExternalSnapshotMetadata` feature gate and the `SnapshotMetadataService` CRD
only become available after a feature set that enables the gate is applied (here
`CustomNoUpgrade`). The CRD should exist before the sidecar is configured in step 4. The
`v1beta1` CRD used by this suite is not what the gate installs on OCP <= 4.22 (see below),
so step 0 applies it directly when it is missing.

## ExternalSnapshotMetadata Feature Gate

### What it does

`ExternalSnapshotMetadata` is an OpenShift feature gate for [KEP-3314: CSI Changed Block Tracking](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md). KEP-3314 itself has no upstream Kubernetes feature gate (the API is out-of-tree). When the OpenShift gate is enabled, it does **one thing**: the `cluster-csi-snapshot-controller-operator` installs the `SnapshotMetadataService` CRD (`snapshotmetadataservices.cbt.storage.k8s.io`).

Which CRD version it installs depends on the OCP version:

| OCP version | CRD installed by the gate |
| ----------- | ------------------------- |
| 4.21, 4.22 | `v1alpha1` |
| 5.0 | `v1beta1` only |

This suite and `step4-cbt-sidecar.sh` use `cbt.storage.k8s.io/v1beta1`, so on OCP <= 4.22 the `v1beta1` CRD must be applied manually (step 0 does this when no CRD exists). `v1alpha1` was removed upstream in external-snapshot-metadata v1.0.0, with no conversion; the schema is unchanged.

Which feature sets enable the gate:

| OCP version | Feature sets |
| ----------- | ------------ |
| 4.20 - 4.22 | `DevPreviewNoUpgrade` |
| 4.23 / 5.0 | `TechPreviewNoUpgrade` and `DevPreviewNoUpgrade` |
| any | `CustomNoUpgrade` with `ExternalSnapshotMetadata` listed (used by step 1) |

It is not enabled by default on any version. Step 0 accepts `CustomNoUpgrade`, `DevPreviewNoUpgrade` and `TechPreviewNoUpgrade`; step 1 leaves a `DevPreviewNoUpgrade`/`TechPreviewNoUpgrade` cluster unchanged.

It does **not**:
- Pass `--feature-gates` flags to kube-apiserver, kubelet, or any other component
- Deploy any additional pods, sidecars, or controllers
- Modify any existing CSI driver configurations

The CRD allows CSI drivers with CBT support to advertise their snapshot metadata gRPC service to backup tools (like Velero). A `SnapshotMetadataService` CR contains: `address` (sidecar TCP endpoint), `caCert` (TLS CA certificate), and `audience` (TokenRequest authentication audience).

### Manual alternative (without feature gate)

Since the feature gate only installs a CRD, you can skip the feature gate entirely and apply the CRD directly. Step 0 does this with the upstream CRD pinned to `external-snapshot-metadata` v1.1.0, the version in `go.mod` (override with `SMS_CRD_URL`):

```bash
# From upstream, pinned to the release matching go.mod (v1beta1)
oc apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshot-metadata/v1.1.0/client/config/crd/cbt.storage.k8s.io_snapshotmetadataservices.yaml
```

The OpenShift fork ([openshift/csi-external-snapshot-metadata](https://github.com/openshift/csi-external-snapshot-metadata)) is not used: its `main` branch carries the `v1beta1` CRD, but `release-4.21` and `release-4.22` still carry `v1alpha1`, so there is no release branch to pin to.

This avoids `CustomNoUpgrade` (which makes the cluster non-upgradable and triggers a full node rollout). The CRD is inert until a CSI driver with CBT sidecar support creates `SnapshotMetadataService` CRs.

### Important notes

- **Kubernetes version**: KEP-3314 alpha landed in Kubernetes 1.33 and beta in 1.36; this suite requires Kubernetes 1.33+ (the upstream compatibility floor). The kubernetes-csi docs table lists a higher minimum for the v1beta1 API (Kubernetes 1.36 / CSI spec 1.12), so confirm against the sidecar version you deploy.
- **CBT support in OpenShift is pre-GA**: Developer Preview on OCP/ODF 4.20-4.22, Technology Preview on OCP 5.0. The feature gate is not on by default.
- **The CRD alone is harmless** — it just defines a new resource type with no runtime effect

### References

- [KEP-3314: CSI Changed Block Tracking](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md)
- [external-snapshot-metadata sidecar repo](https://github.com/kubernetes-csi/external-snapshot-metadata)
- [OpenShift fork](https://github.com/openshift/csi-external-snapshot-metadata)
- [ceph-csi-operator: RBD snapshot metadata](https://github.com/red-hat-storage/ceph-csi-operator/blob/main/docs/features/rbd-snapshot-metadata.md)

## Prerequisites

- `oc` CLI with cluster-admin access
- Cluster login: `oc login -u kubeadmin -p <password> <api-url> --insecure-skip-tls-verify`
- Reference: [Red Hat KCS 7130698](https://access.redhat.com/articles/7130698) - Configure external-snapshot-metadata sidecar for RBD (Developer Preview; written for ODF 4.20); [KCS 7146879](https://access.redhat.com/solutions/7146879) for OCP 5.0 (Technology Preview)

## Setup Steps

Run all steps with the master script, or run each step individually.

```bash
# Run everything (including e2e-fast at the end)
./setup-all.sh

# Run setup only, skip e2e tests
./setup-all.sh --skip-e2e

# Run setup + specific e2e target
./setup-all.sh --e2e-target=e2e-fast
```

Individual step scripts (each is idempotent where possible):

| Script | Description | Wait Time |
| ------ | ----------- | --------- |
| `step0-preflight.sh` | Check ODF prerequisites (nodes, RAM, storage, marketplace) | instant |
| `step1-featuregate.sh` | Enable CustomNoUpgrade with ExternalSnapshotMetadata (optional; step 0 can apply the CRD directly) | ~30 min (node rollout) |
| `step2-install-odf.sh` | Install ODF operator via CLI | ~5 min |
| `step3-create-storagecluster.sh` | Create ODF StorageCluster on worker nodes | ~15 min |
| `step4-cbt-sidecar.sh` | Configure CBT sidecar (RBAC, Service, TLS, SMS CR, Driver CR patch; ODF <= 4.21 image override). Skips the operator-managed parts on ODF 5.x. See [ceph-csi-operator docs](https://github.com/red-hat-storage/ceph-csi-operator/blob/main/docs/features/rbd-snapshot-metadata.md) | ~5 min |
| `step5-verify.sh` | Verify all prerequisites for e2e tests | instant |
| `step6-run-e2e.sh` | Run CBT e2e test suite | 30 min - 5h |
| `uninstall-odf.sh` | Uninstall ODF and clean up all resources | ~10 min |

## Uninstalling ODF

The `uninstall-odf.sh` script performs a complete ODF removal following the [official Red Hat procedure](https://docs.redhat.com/en/documentation/red_hat_openshift_data_foundation/4.19/html/deploying_openshift_data_foundation_on_any_platform/uninstalling_openshift_data_foundation):

```bash
# Standard uninstall (fails if PVCs still use ODF storage)
./uninstall-odf.sh

# Force uninstall (skips PVC check, orphans existing PVCs)
./uninstall-odf.sh --force

# Keep CRDs (useful if reinstalling ODF)
./uninstall-odf.sh --force --skip-crd-cleanup
```

The script handles: StorageCluster deletion, stuck finalizer removal, operator/CSV cleanup, namespace deletion, StorageClass/VolumeSnapshotClass removal, node label/taint cleanup, `/var/lib/rook` cleanup, orphaned PV removal, CRD deletion, and webhook cleanup.

## ODF Performance Profiles

ODF provides three performance profiles with different resource requirements (cluster-wide totals across all workers):

| Profile | CPU | Memory | Use case |
| ------- | --- | ------ | -------- |
| `lean` | 24 | 72 GiB | Test/dev clusters, minimal footprint |
| `balanced` | 30 | 72 GiB | Default for production |
| `performance` | 45 | 96 GiB | High-throughput workloads |

The preflight check (step0) auto-detects the best profile based on available resources. Step3 sets `resourceProfile` in the StorageCluster spec.

Override with:
```bash
ODF_PROFILE=lean ./setup-all.sh
```

## Operator Compatibility

Step 4 configures the CBT sidecar via the Driver CR following the [official ceph-csi-operator docs](https://github.com/red-hat-storage/ceph-csi-operator/blob/main/docs/features/rbd-snapshot-metadata.md). Both operators stay running:

- **ceph-csi-controller-manager**: Reconciles Driver CR changes (TLS volume, ImageSet, sidecar injection) into the deployment. The sidecar is injected when the Driver CR has a volume named `tls-key` (mount path `/tmp/certificates`).
- **ocs-client-operator**: Manages the `ceph-csi-op-scc` SCC. On ODF <= 4.21 the SCC only allows `configMap`, `emptyDir`, `hostPath`, and `projected`, so the TLS volume uses a `projected` volume (sourcing from the secret) instead of a `secret` volume. No SCC patch or operator scale-down is needed. The release-4.22 and release-5.0 branches of ocs-client-operator allow `secret` volumes, so the projected form is only required on <= 4.21 (it works on all versions).

### SELinux host mount for RBD Filesystem volumes

ceph-csi-operator always sets `seLinuxMount: true` on the RBD `CSIDriver`, so kubelet
passes a `context=` mount option and skips relabeling. The node plugin must be able to
see the host's SELinux configuration for that to work. With
`nodePlugin.enableSeLinuxHostMount: false` (the operator default), libselinux inside the
plugin reports SELinux disabled, `mount` silently drops `context=`, and an RBD
`volumeMode: Filesystem` volume stays `unlabeled_t`, so confined or non-root pods get
`EACCES` ([ceph-csi#6679](https://github.com/ceph/ceph-csi/issues/6679)). Set it to
`true` in the Driver CR (`spec.nodePlugin`) or OperatorConfig
(`spec.driverSpecDefaults.nodePlugin`); the Driver CR wins
([ceph-csi-operator#649](https://github.com/ceph/ceph-csi-operator/pull/649) merged;
[rook#18636](https://github.com/rook/rook/pull/18636) updates the OpenShift example).
`step5-verify.sh` warns when the CSIDriver advertises `seLinuxMount` but the setting is
not enabled. Existing volumes pick up `context=` only after they are unstaged and
remounted. Block-mode volumes are unaffected.

### Sidecar image (ODF <= 4.21)

Early ODF 4.21 builds ship a sidecar that breaks `GetMetadataDelta` (it looks up the CSI snapshot handle as a VolumeSnapshot name). Step 4 (4g) overrides it through a driver-level ImageSet. The image is `SIDECAR_IMAGE`, defaulting to `registry.k8s.io/sig-storage/csi-snapshot-metadata:v0.2.0`, the only configuration this repo has validated. The upstream fix ([ceph-csi-operator 454e9130](https://github.com/red-hat-storage/ceph-csi-operator/commit/454e9130), a sidecar image bump to v0.2.0) is in ceph-csi-operator `release-4.22`, `release-5.0` and `main`; ODF 4.22 ships v0.2.0 and ODF 5.0 ships v1.0.0. A late ODF 4.21.z build may already carry a fixed sidecar (not verified), so check the shipped image before overriding.

```bash
# Default: upstream v0.2.0
bash step4-cbt-sidecar.sh

# Try the SMS v1beta1-only v1.1.0 sidecar (not validated by this repo; see below)
SIDECAR_IMAGE=registry.k8s.io/sig-storage/csi-snapshot-metadata:v1.1.0 bash step4-cbt-sidecar.sh
```

v1.0.0 and v1.1.0 are `v1beta1`-only releases (v1.0.0 removed `v1alpha1` and replaced `grpc_health_probe` with an HTTP `/health` probe on `:8080`). Velero validated v1.1.0 live on Ceph/ODF ([velero-io/velero#10307](https://github.com/velero-io/velero/pull/10307); OCP 5.0 + ODF 4.21.10 per [velero-io/velero#10294](https://github.com/velero-io/velero/issues/10294), with a hand-applied v1beta1 CRD and the upstream sidecar rather than ODF's shipped build), but this repo has not, and the operator-injected probes on release-4.21/4.22 may assume the v0.x probe. Test the override before relying on it. The override is applied only on ODF <= 4.21 (or when the version cannot be detected); ODF 4.22+ and 5.x are left alone.

### ODF 5.0 / OCP >= 4.23: operator-managed sidecar

Based on ocs-client-operator source (`internal/controller/operatorconfigmap_controller.go`, gated on OCP >= 4.23 / 5.x; not verified on a live ODF 5.0 cluster), the operator itself creates the `openshift-storage-rbd-snapshot-metadata` Service (6443 to 50051, serving-cert annotation), its TLS secret, the `tls-key` volume in the RBD Driver CR, and a ConfigMap named after the driver (`address`, `audience`, `caCert` from the service CA bundle, `driverName`). It does not appear to create the `SnapshotMetadataService` CR. When step 4 finds that Service it skips the manual Service/TLS/Driver CR/image steps and only creates the SMS CR (from that ConfigMap, unless a CR already exists). The ClusterRoles/binding are still applied.

## Cluster Info

Last validated environment (results.md, 2026-03-26; predates external-snapshot-metadata v1.1.0, CephCSI 3.17/3.18 and ODF 4.22). Not a current cluster.

- **API**: `https://api.mig-tkaovila-mar23.rhos-psi.cnv-qe.rhood.us:6443`
- **OCP Version**: 4.21.7 (Kubernetes v1.34.5)
- **Nodes**: 6 (3 control-plane + 3 workers)
