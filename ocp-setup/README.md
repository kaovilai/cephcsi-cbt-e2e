# OCP Cluster Setup for CBT E2E Testing

## CNV-Deployer Build Parameters

This cluster was provisioned via cnv-deployer with the following parameters:

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
only become available after the `CustomNoUpgrade` feature set is applied. ODF must
be installed after the feature gate is enabled so the CephCSI operator can detect
the CRD and auto-inject the snapshot-metadata sidecar container.

## ExternalSnapshotMetadata Feature Gate

### What it does

The `ExternalSnapshotMetadata` feature gate is part of [KEP-3314: CSI Changed Block Tracking](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md). When enabled, it does **one thing**: the `cluster-csi-snapshot-controller-operator` installs the `SnapshotMetadataService` CRD (`snapshotmetadataservices.cbt.storage.k8s.io/v1beta1`).

It does **not**:
- Pass `--feature-gates` flags to kube-apiserver, kubelet, or any other component
- Deploy any additional pods, sidecars, or controllers
- Modify any existing CSI driver configurations

The CRD allows CSI drivers with CBT support to advertise their snapshot metadata gRPC service to backup tools (like Velero). A `SnapshotMetadataService` CR contains: `address` (sidecar TCP endpoint), `caCert` (TLS CA certificate), and `audience` (TokenRequest authentication audience).

### Manual alternative (without feature gate)

Since the feature gate only installs a CRD, you can skip the feature gate entirely and apply the CRD directly:

```bash
# From upstream
oc apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshot-metadata/main/client/config/crd/cbt.storage.k8s.io_snapshotmetadataservices.yaml

# Or from OpenShift's fork
oc apply -f https://raw.githubusercontent.com/openshift/csi-external-snapshot-metadata/main/client/config/crd/cbt.storage.k8s.io_snapshotmetadataservices.yaml
```

This avoids `CustomNoUpgrade` (which makes the cluster non-upgradable and triggers a full node rollout). The CRD is inert until a CSI driver with CBT sidecar support creates `SnapshotMetadataService` CRs.

### Important notes

- **Kubernetes version**: The upstream `CSIVolumeSnapshotMetadata` feature gate requires Kubernetes 1.34+, which maps to OCP 4.20+
- **No CSI drivers currently ship with CBT support in OpenShift** — this is why it's DevPreview only
- **The CRD alone is harmless** — it just defines a new resource type with no runtime effect

### References

- [KEP-3314: CSI Changed Block Tracking](https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3314-csi-changed-block-tracking/README.md)
- [external-snapshot-metadata sidecar repo](https://github.com/kubernetes-csi/external-snapshot-metadata)
- [OpenShift fork](https://github.com/openshift/csi-external-snapshot-metadata)

## Prerequisites

- `oc` CLI with cluster-admin access
- Cluster login: `oc login -u kubeadmin -p <password> <api-url> --insecure-skip-tls-verify`
- Reference: [Red Hat KCS 7130698](https://access.redhat.com/articles/7130698) - Configure external-snapshot-metadata sidecar for RBD

## Setup Steps

Run all steps with the master script, or run each step individually.

```bash
# Run everything (including e2e-basic at the end)
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
| `step1-featuregate.sh` | Enable CustomNoUpgrade with ExternalSnapshotMetadata | ~30 min (node rollout) |
| `step2-install-odf.sh` | Install ODF operator via CLI | ~5 min |
| `step3-create-storagecluster.sh` | Create ODF StorageCluster on worker nodes | ~15 min |
| `step4-cbt-sidecar.sh` | Configure CBT sidecar (RBAC, Service, TLS, SMS CR, Driver CR patch). See [ceph-csi-operator docs](https://github.com/red-hat-storage/ceph-csi-operator/blob/main/docs/features/rbd-snapshot-metadata.md) | ~5 min |
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

- **ceph-csi-controller-manager**: Reconciles Driver CR changes (TLS volume, ImageSet, sidecar injection) into the deployment.
- **ocs-client-operator**: Manages the `ceph-csi-op-scc` SCC. The TLS volume uses a `projected` volume type (sourcing from the secret) instead of a `secret` volume type, because the SCC only allows `configMap`, `emptyDir`, `hostPath`, and `projected`. No SCC patch or operator scale-down is needed.

## Cluster Info

- **API**: `https://api.mig-tkaovila-mar23.rhos-psi.cnv-qe.rhood.us:6443`
- **OCP Version**: 4.21.7 (Kubernetes v1.34.5)
- **Nodes**: 6 (3 control-plane + 3 workers)
