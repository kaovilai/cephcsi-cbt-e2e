# Deploying csi-external-snapshot-metadata Sidecar for CephCSI RBD on ODF/OCP

This document describes how to deploy the `csi-external-snapshot-metadata` sidecar
alongside the CephCSI RBD controller plugin on an OpenShift 4.21+ cluster with ODF,
enabling the CSI Changed Block Tracking (CBT) API (`cbt.storage.k8s.io/v1alpha1`).

## Prerequisites

- OpenShift 4.20+ cluster (tested on OCP 4.21.4, Kubernetes v1.34.4)
- ODF (OpenShift Data Foundation) installed with CephCSI RBD
- `DevPreviewNoUpgrade` feature set enabled (makes cluster non-upgradable)
- `oc` CLI with cluster-admin access

## Cluster State Before Deployment

The following were already present on our OCP 4.21.4 cluster:

| Resource | Status |
|----------|--------|
| `DevPreviewNoUpgrade` feature set | Enabled |
| `SnapshotMetadataService` CRD (`cbt.storage.k8s.io/v1alpha1`) | Present (auto-created by feature set) |
| `cephcsi-operator.v4.21.0-rhodf` CSV | Succeeded |
| RBD ctrlplugin deployment | `openshift-storage.rbd.csi.ceph.com-ctrlplugin` (2 replicas) |
| RBD ctrlplugin service account | `ceph-csi-rbd-ctrlplugin-sa` |
| CSI socket path | `unix:///csi/csi.sock` (volume `socket-dir`) |
| SCC for CephCSI | `ceph-csi-op-scc` (allows hostPath, configMap, emptyDir, projected) |

## Step 0: Enable DevPreviewNoUpgrade (if not already set)

```bash
oc patch featuregate cluster --type=merge -p '{"spec":{"featureSet":"DevPreviewNoUpgrade"}}'
```

Verify the `SnapshotMetadataService` CRD was created:
```bash
oc get crd snapshotmetadataservices.cbt.storage.k8s.io
```

## Step 1: Get the Sidecar Image from the Release Payload

Get the exact image for YOUR cluster version (not the `oc` client version):

```bash
# Get the cluster's release image
RELEASE_IMAGE=$(oc get clusterversion version -o jsonpath='{.status.desired.image}')

# Extract the sidecar image from the release payload
SIDECAR_IMAGE=$(oc adm release info "$RELEASE_IMAGE" --image-for=csi-external-snapshot-metadata)

echo "Sidecar image: $SIDECAR_IMAGE"
```

On our OCP 4.21.4 cluster:
```
quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:17deffb731a21c00179cacb45fc46248f1d032cba8030154662ed563335113c1
```

## Step 2: Create RBAC ClusterRoles

Two ClusterRoles are needed:
- `external-snapshot-metadata-client-runner` - for CBT client tools/applications
- `external-snapshot-metadata-runner` - for the sidecar itself

```bash
oc apply -f - <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: external-snapshot-metadata-client-runner
rules:
- apiGroups:
  - snapshot.storage.k8s.io
  resources:
  - volumesnapshots
  - volumesnapshotcontents
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - cbt.storage.k8s.io
  resources:
  - snapshotmetadataservices
  verbs:
  - get
  - list
- apiGroups:
  - ""
  resources:
  - serviceaccounts/token
  verbs:
  - create
  - get
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: external-snapshot-metadata-runner
rules:
- apiGroups:
  - cbt.storage.k8s.io
  resources:
  - snapshotmetadataservices
  verbs:
  - get
  - list
  - watch
  - create
  - update
  - patch
  - delete
- apiGroups:
  - authentication.k8s.io
  resources:
  - tokenreviews
  verbs:
  - create
  - get
- apiGroups:
  - authorization.k8s.io
  resources:
  - subjectaccessreviews
  verbs:
  - create
  - get
- apiGroups:
  - snapshot.storage.k8s.io
  resources:
  - volumesnapshots
  - volumesnapshotcontents
  verbs:
  - get
  - list
EOF
```

## Step 3: Create ClusterRoleBinding for CephCSI RBD Service Account

Bind the `external-snapshot-metadata-runner` role to the CephCSI RBD service account:

```bash
oc apply -f - <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ceph-csi-rbd-snapshot-metadata-runner
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: external-snapshot-metadata-runner
subjects:
- kind: ServiceAccount
  name: ceph-csi-rbd-ctrlplugin-sa
  namespace: openshift-storage
EOF
```

## Step 4: Create Service with Auto-TLS Certificate

OpenShift's service serving certificate signer automatically generates a TLS
certificate when the `service.beta.openshift.io/serving-cert-secret-name` annotation
is present on a Service. This eliminates the need for manual certificate generation.

```bash
oc apply -f - <<'EOF'
apiVersion: v1
kind: Service
metadata:
  name: csi-snapshot-metadata
  namespace: openshift-storage
  labels:
    app.kubernetes.io/name: csi-snapshot-metadata
  annotations:
    service.beta.openshift.io/serving-cert-secret-name: csi-snapshot-metadata-certs
spec:
  ports:
  - name: snapshot-metadata
    port: 6443
    protocol: TCP
    targetPort: 50051
  selector:
    app: openshift-storage.rbd.csi.ceph.com-ctrlplugin
EOF
```

Verify the TLS secret was auto-generated:
```bash
oc get secret -n openshift-storage csi-snapshot-metadata-certs
# Expected: TYPE=kubernetes.io/tls, DATA=2
```

## Step 5: Create SnapshotMetadataService CR

The SnapshotMetadataService CR name **must match the CSI driver name**:

```bash
CA_CERT=$(oc get secrets -n openshift-storage csi-snapshot-metadata-certs \
  -o jsonpath='{.data.tls\.crt}')

oc apply -f - <<EOF
apiVersion: cbt.storage.k8s.io/v1alpha1
kind: SnapshotMetadataService
metadata:
  name: openshift-storage.rbd.csi.ceph.com
spec:
  address: csi-snapshot-metadata.openshift-storage.svc:6443
  caCert: ${CA_CERT}
  audience: openshift-storage.rbd.csi.ceph.com
EOF
```

## Step 6: Update SCC to Allow Secret Volumes

The `ceph-csi-op-scc` SCC used by CephCSI pods only allows `configMap`, `emptyDir`,
`hostPath`, and `projected` volume types. The sidecar needs a `secret` volume for
TLS certificates.

```bash
oc patch scc ceph-csi-op-scc --type=json \
  -p '[{"op":"add","path":"/volumes/-","value":"secret"}]'
```

Verify:
```bash
oc get scc ceph-csi-op-scc -o jsonpath='{.volumes}'
# Expected: ["configMap","emptyDir","hostPath","projected","secret"]
```

## Step 7: Scale Down the CephCSI Operator

The `ceph-csi-controller-manager` (from `cephcsi-operator.v4.21.0-rhodf` CSV)
reconciles the Driver CR and the RBD ctrlplugin deployment. It will revert manual
patches to the deployment.

**Important**: On ODF 4.21, the operator auto-injects the `csi-snapshot-metadata`
container when a SnapshotMetadataService CR exists, but it does NOT mount the TLS
certificate secret volume. This is a gap in the operator implementation.

Scale down to prevent reconciliation:
```bash
oc scale deployment ceph-csi-controller-manager -n openshift-storage --replicas=0
```

## Step 8: Patch the RBD ctrlplugin Deployment

The operator already added the `csi-snapshot-metadata` container with TLS args
(`--tls-cert=/tmp/certificates/tls.crt`, `--tls-key=/tmp/certificates/tls.key`)
but without the volume mount. We add the missing volume and volume mount:

```bash
# Find the sidecar container index
SIDECAR_IDX=$(oc get deployment openshift-storage.rbd.csi.ceph.com-ctrlplugin \
  -n openshift-storage -o json | \
  jq '[.spec.template.spec.containers | to_entries[] | select(.value.name == "csi-snapshot-metadata") | .key][0]')

echo "Sidecar container index: $SIDECAR_IDX"

# Patch: add secret volume + volume mount to the sidecar container
oc patch deployment openshift-storage.rbd.csi.ceph.com-ctrlplugin \
  -n openshift-storage --type=json -p "[
  {
    \"op\": \"add\",
    \"path\": \"/spec/template/spec/volumes/-\",
    \"value\": {
      \"name\": \"csi-snapshot-metadata-server-certs\",
      \"secret\": {
        \"secretName\": \"csi-snapshot-metadata-certs\"
      }
    }
  },
  {
    \"op\": \"add\",
    \"path\": \"/spec/template/spec/containers/${SIDECAR_IDX}/volumeMounts/-\",
    \"value\": {
      \"name\": \"csi-snapshot-metadata-server-certs\",
      \"mountPath\": \"/tmp/certificates\",
      \"readOnly\": true
    }
  }
]"
```

Wait for the rollout:
```bash
oc rollout status deployment/openshift-storage.rbd.csi.ceph.com-ctrlplugin \
  -n openshift-storage --timeout=120s
```

## Step 9: Verify the Sidecar is Healthy

```bash
# Check all containers are running (should be 9/9)
oc get pods -n openshift-storage \
  -l app=openshift-storage.rbd.csi.ceph.com-ctrlplugin

# Check sidecar logs for successful startup
oc logs -n openshift-storage \
  deployment/openshift-storage.rbd.csi.ceph.com-ctrlplugin \
  -c csi-snapshot-metadata --tail=20
```

Expected log output:
```
I0305 ... sidecar.go:88] CSI driver name: "openshift-storage.rbd.csi.ceph.com"
I0305 ... sidecar.go:100] GRPC server started at ...
```

## Step 10: Scale the Operator Back Up (Optional)

**Warning**: Scaling the operator back up will likely revert the volume mount patch.
For testing purposes, keep the operator scaled down. For a production-ready solution,
the ceph-csi-operator needs to be updated to handle TLS cert mounting natively.

```bash
# Only if you want to restore operator reconciliation:
# oc scale deployment ceph-csi-controller-manager -n openshift-storage --replicas=1
```

## Troubleshooting

### SCC Rejection: "secret volumes are not allowed"

The `ceph-csi-op-scc` SCC doesn't include `secret` in its allowed volume types.
Add it with:
```bash
oc patch scc ceph-csi-op-scc --type=json \
  -p '[{"op":"add","path":"/volumes/-","value":"secret"}]'
```

### Operator Reverts Deployment Patches

The `ceph-csi-controller-manager` continuously reconciles the deployment. Scale it
down before patching:
```bash
oc scale deployment ceph-csi-controller-manager -n openshift-storage --replicas=0
```

### Sidecar CrashLoopBackOff: "failed to load tls certificates"

The cert volume mount is missing. Verify the deployment has both:
1. A volume `csi-snapshot-metadata-server-certs` referencing secret `csi-snapshot-metadata-certs`
2. A volumeMount on the `csi-snapshot-metadata` container at `/tmp/certificates`

### Wrong Sidecar Image

Always use the image from the cluster's release payload, not a generic tag:
```bash
RELEASE_IMAGE=$(oc get clusterversion version -o jsonpath='{.status.desired.image}')
oc adm release info "$RELEASE_IMAGE" --image-for=csi-external-snapshot-metadata
```

## Key Differences from KCS Hostpath Example

| Aspect | KCS Hostpath Example | CephCSI RBD on ODF |
|--------|---------------------|--------------------|
| Namespace | `openshift-cluster-csi-drivers` | `openshift-storage` |
| Service Account | `csi-hostpathplugin-sa` | `ceph-csi-rbd-ctrlplugin-sa` |
| SMS CR name | `hostpath.csi.k8s.io` | `openshift-storage.rbd.csi.ceph.com` |
| Service address | `csi-snapshot-metadata.openshift-cluster-csi-drivers.svc:6443` | `csi-snapshot-metadata.openshift-storage.svc:6443` |
| Service selector | `app.kubernetes.io/name: csi-snapshot-metadata` | `app: openshift-storage.rbd.csi.ceph.com-ctrlplugin` |
| Deployment | New StatefulSet | Patch existing Deployment |
| SCC | `privileged` via ClusterRole | `ceph-csi-op-scc` (needs `secret` added) |
| Operator | None | `ceph-csi-controller-manager` (must scale down) |
| Sidecar image | `quay.io/openshift/origin-csi-external-snapshot-metadata:latest` | From release payload via `oc adm release info` |
| Sidecar injection | Manual | Auto-injected by operator (but TLS volume missing) |

## Observed Operator Behavior (ODF 4.21.0-rhodf)

When a `SnapshotMetadataService` CR is created, the `ceph-csi-controller-manager`
(CephCSI operator) automatically:

1. **Adds the `csi-snapshot-metadata` container** to the RBD ctrlplugin deployment
   with the correct args:
   ```
   --v=5
   --timeout=150s
   --port=50051
   --csi-address=unix:///csi/csi.sock
   --tls-cert=/tmp/certificates/tls.crt
   --tls-key=/tmp/certificates/tls.key
   ```

2. **Uses the Red Hat image**: `registry.redhat.io/openshift4/ose-csi-external-snapshot-metadata-rhel9@sha256:...`
   (different from the release payload image, but functionally equivalent)

3. **Does NOT add the TLS certificate volume or volume mount** - this is the gap
   that requires manual patching.

4. **Continuously reconciles** - any manual changes to the deployment are reverted
   within seconds, requiring the operator to be scaled down.

## Resources Created

| Resource | Name | Namespace |
|----------|------|-----------|
| ClusterRole | `external-snapshot-metadata-client-runner` | - |
| ClusterRole | `external-snapshot-metadata-runner` | - |
| ClusterRoleBinding | `ceph-csi-rbd-snapshot-metadata-runner` | - |
| Service | `csi-snapshot-metadata` | `openshift-storage` |
| Secret (auto) | `csi-snapshot-metadata-certs` | `openshift-storage` |
| SnapshotMetadataService | `openshift-storage.rbd.csi.ceph.com` | - (cluster-scoped) |

## Cleanup

To remove all snapshot-metadata resources and restore the original state:

```bash
# Delete SnapshotMetadataService CR (will trigger operator to remove sidecar)
oc delete snapshotmetadataservice openshift-storage.rbd.csi.ceph.com

# Scale operator back up (it will reconcile and remove the sidecar container)
oc scale deployment ceph-csi-controller-manager -n openshift-storage --replicas=1

# Wait for reconciliation
sleep 30

# Delete RBAC
oc delete clusterrolebinding ceph-csi-rbd-snapshot-metadata-runner
oc delete clusterrole external-snapshot-metadata-runner
oc delete clusterrole external-snapshot-metadata-client-runner

# Delete Service and auto-generated cert
oc delete service csi-snapshot-metadata -n openshift-storage

# Remove 'secret' from SCC (restore original state)
oc patch scc ceph-csi-op-scc --type=json \
  -p '[{"op":"test","path":"/volumes/4","value":"secret"},{"op":"remove","path":"/volumes/4"}]'
```
