#!/bin/bash
# Step 4: Configure CBT snapshot-metadata sidecar for CephCSI RBD
# Reference: https://github.com/red-hat-storage/ceph-csi-operator/blob/main/docs/features/rbd-snapshot-metadata.md
# Also references: https://access.redhat.com/articles/7130698
# Requires: ODF installed with RBD (step 3)
set -euo pipefail

echo "=== Step 4: Configure CBT Sidecar ==="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

CTRLPLUGIN_LABEL=$(detect_ctrlplugin_label)
echo "Detected ctrlplugin pod label: $CTRLPLUGIN_LABEL"

# --- 4a: Verify prerequisites ---
echo "--- 4a: Verifying prerequisites ---"
oc get crd snapshotmetadataservices.cbt.storage.k8s.io || { echo "ERROR: SnapshotMetadataService CRD not found. Run step1 first."; exit 1; }
oc get deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" &>/dev/null || { echo "ERROR: RBD ctrlplugin deployment not found. Run step3 first."; exit 1; }

# --- 4b: Create RBAC ClusterRoles ---
echo ""
echo "--- 4b: Creating RBAC ClusterRoles ---"
oc apply -f - <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: external-snapshot-metadata-client-runner
rules:
- apiGroups: ["snapshot.storage.k8s.io"]
  resources: ["volumesnapshots", "volumesnapshotcontents"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["cbt.storage.k8s.io"]
  resources: ["snapshotmetadataservices"]
  verbs: ["get", "list"]
- apiGroups: [""]
  resources: ["serviceaccounts/token"]
  verbs: ["create", "get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: external-snapshot-metadata-runner
rules:
- apiGroups: ["cbt.storage.k8s.io"]
  resources: ["snapshotmetadataservices"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: ["authentication.k8s.io"]
  resources: ["tokenreviews"]
  verbs: ["create", "get"]
- apiGroups: ["authorization.k8s.io"]
  resources: ["subjectaccessreviews"]
  verbs: ["create", "get"]
- apiGroups: ["snapshot.storage.k8s.io"]
  resources: ["volumesnapshots", "volumesnapshotcontents"]
  verbs: ["get", "list"]
EOF
echo "ClusterRoles created."

# --- 4c: Create ClusterRoleBinding ---
echo ""
echo "--- 4c: Creating ClusterRoleBinding ---"
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
echo "ClusterRoleBinding created."

# --- 4d: Create Service with auto-TLS ---
echo ""
echo "--- 4d: Creating Service with auto-TLS certificate ---"
# Build selector YAML from detected label (handles both simple and compound labels)
SELECTOR_YAML=""
IFS=',' read -ra LABEL_PAIRS <<< "$CTRLPLUGIN_LABEL"
for pair in "${LABEL_PAIRS[@]}"; do
    key="${pair%%=*}"
    val="${pair#*=}"
    SELECTOR_YAML="${SELECTOR_YAML}    ${key}: ${val}"$'\n'
done

# Use a temp file to avoid heredoc variable expansion issues with set -u
SVC_YAML=$(mktemp)
trap 'rm -f "$SVC_YAML"' EXIT
cat > "$SVC_YAML" <<SVCEOF
apiVersion: v1
kind: Service
metadata:
  name: csi-snapshot-metadata
  namespace: ${NAMESPACE}
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
    PLACEHOLDER_SELECTOR: true
SVCEOF
# Replace placeholder with actual selector (heredoc terminators can't come from variable expansion)
sed -i'' -e "s|    PLACEHOLDER_SELECTOR: true|${SELECTOR_YAML%$'\n'}|" "$SVC_YAML"
oc apply -f "$SVC_YAML"
echo "Service created."

# Wait for TLS secret to be auto-generated
echo "Waiting for TLS secret..."
for i in $(seq 1 30); do
    if oc get secret -n "$NAMESPACE" csi-snapshot-metadata-certs &>/dev/null; then
        echo "TLS secret 'csi-snapshot-metadata-certs' is available."
        break
    fi
    echo "  Waiting... ($i/30)"
    sleep 5
done

# --- 4e: Create SnapshotMetadataService CR ---
echo ""
echo "--- 4e: Creating SnapshotMetadataService CR ---"
CA_CERT=$(oc get secrets -n "$NAMESPACE" csi-snapshot-metadata-certs -o jsonpath='{.data.tls\.crt}')
if [ -z "$CA_CERT" ]; then
    echo "ERROR: Could not get CA cert from secret. Aborting."
    exit 1
fi

oc apply -f - <<EOF
apiVersion: cbt.storage.k8s.io/v1alpha1
kind: SnapshotMetadataService
metadata:
  name: ${CSI_DRIVER_NAME}
spec:
  address: csi-snapshot-metadata.${NAMESPACE}.svc:6443
  caCert: ${CA_CERT}
  audience: ${CSI_DRIVER_NAME}
EOF
echo "SnapshotMetadataService CR created."

# --- 4f: Add TLS volume to Driver CR ---
# Per official docs: volume and mount names must be "tls-key" (operator filters by this name),
# mount path must be /tmp/certificates.
# Ref: https://github.com/red-hat-storage/ceph-csi-operator/blob/main/docs/features/rbd-snapshot-metadata.md#5-add-tls-volume-mount-to-rbd-driver-cr
echo ""
echo "--- 4f: Adding TLS volume to Driver CR (spec.controllerPlugin.volumes) ---"
oc patch drivers.csi.ceph.io "${CSI_DRIVER_NAME}" -n "$NAMESPACE" --type=merge -p "$(cat <<PATCHEOF
{
  "spec": {
    "controllerPlugin": {
      "volumes": [
        {
          "mount": {
            "mountPath": "/tmp/certificates",
            "name": "tls-key"
          },
          "volume": {
            "name": "tls-key",
            "secret": {
              "secretName": "csi-snapshot-metadata-certs"
            }
          }
        }
      ]
    }
  }
}
PATCHEOF
)"
echo "Driver CR patched with TLS volume."

# --- 4g: Override sidecar image to upstream v0.2.0 (ODF <= 4.21 only) ---
# ODF <= 4.21 ships an older sidecar image that uses BaseSnapshotName (name-based lookup)
# instead of BaseSnapshotId (handle passthrough). This breaks GetMetadataDelta because the
# sidecar tries to look up the CSI snapshot handle as a VolumeSnapshot name.
# Fix: https://github.com/red-hat-storage/ceph-csi-operator/commit/454e9130
# Expected in ODF 4.22+. Until then, override via driver-level ImageSet.
echo ""
echo "--- 4g: Checking if sidecar image override is needed ---"

# Detect ODF version from the installed CSV
ODF_VERSION=$(oc get csv -n "$NAMESPACE" -o jsonpath='{.items[?(@.metadata.name)].metadata.name}' 2>/dev/null | \
  tr ' ' '\n' | grep -E '^(odf-operator|ocs-operator)\.' | head -1 | grep -oE '[0-9]+\.[0-9]+' | head -1)
ODF_MAJOR=${ODF_VERSION%%.*}
ODF_MINOR=${ODF_VERSION##*.}
echo "Detected ODF version: ${ODF_VERSION:-unknown} (major=${ODF_MAJOR:-?}, minor=${ODF_MINOR:-?})"

# Only override on ODF <= 4.21 (fix expected in ODF 4.22+)
if [ -n "$ODF_MAJOR" ] && [ -n "$ODF_MINOR" ] && [ "$ODF_MAJOR" -eq 4 ] && [ "$ODF_MINOR" -ge 22 ]; then
    echo "ODF >= 4.22 detected. Sidecar image override not needed — skipping."
else
    if [ -z "$ODF_VERSION" ]; then
        echo "WARNING: Could not detect ODF version. Applying override as a precaution."
    else
        echo "ODF $ODF_VERSION <= 4.21. Applying sidecar image override."
    fi

    UPSTREAM_SIDECAR="registry.k8s.io/sig-storage/csi-snapshot-metadata:v0.2.0"

    # Create override ConfigMap from the ODF image set, replacing only snapshot-metadata
    CURRENT_IMAGESET=$(oc get operatorconfigs.csi.ceph.io ceph-csi-operator-config -n "$NAMESPACE" \
      -o jsonpath='{.spec.driverSpecDefaults.imageSet.name}' 2>/dev/null || echo "csi-images-v4.21")

    if oc get configmap "$CURRENT_IMAGESET" -n "$NAMESPACE" &>/dev/null; then
        oc get configmap "$CURRENT_IMAGESET" -n "$NAMESPACE" -o json | \
          jq --arg img "$UPSTREAM_SIDECAR" \
            '.data["snapshot-metadata"] = $img |
             .metadata.name = "csi-images-override" |
             del(.metadata.labels["olm.managed"], .metadata.ownerReferences,
                 .metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp)' | \
          oc apply -f -
    else
        # Fallback: create minimal ConfigMap with just the override
        oc apply -f - <<IMGEOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: csi-images-override
  namespace: ${NAMESPACE}
data:
  snapshot-metadata: "${UPSTREAM_SIDECAR}"
IMGEOF
    fi

    # Point the RBD driver to our override ImageSet (higher priority than operatorConfig)
    oc patch drivers.csi.ceph.io "${CSI_DRIVER_NAME}" -n "$NAMESPACE" --type=merge \
      -p '{"spec":{"imageSet":{"name":"csi-images-override"}}}'
    echo "RBD driver ImageSet overridden to use upstream sidecar: $UPSTREAM_SIDECAR"
fi

# --- 4h: Patch SCC to allow secret volumes (OpenShift-specific) ---
# The ceph-csi-op-scc managed by ocs-client-operator may not include "secret" in allowed
# volume types. We need it for the TLS cert volume mount.
# Scale down ocs-client-operator to prevent it from reverting the SCC patch.
echo ""
echo "--- 4h: Patching SCC to allow secret volumes ---"
VOLUMES=$(oc get scc ceph-csi-op-scc -o jsonpath='{.volumes}' 2>/dev/null || echo "")
if echo "$VOLUMES" | grep -q '"secret"'; then
    echo "SCC already allows secret volumes."
else
    echo "Scaling down ocs-client-operator to prevent SCC revert..."
    oc scale deployment ocs-client-operator-controller-manager -n "$NAMESPACE" --replicas=0
    oc patch scc ceph-csi-op-scc --type=json \
      -p '[{"op":"add","path":"/volumes/-","value":"secret"}]'
    echo "SCC patched to allow secret volumes."
    echo "WARNING: ocs-client-operator scaled to 0 to protect SCC patch."
    echo "  Re-enable with: oc scale deployment ocs-client-operator-controller-manager -n $NAMESPACE --replicas=1"
fi

# --- 4i: Restart operator to reconcile Driver CR changes ---
# The ceph-csi-operator reconciles the Driver CR into the deployment.
# Restarting it picks up the TLS volume, sidecar injection, and ImageSet changes.
echo ""
echo "--- 4i: Restarting ceph-csi-controller-manager to reconcile ---"
OPERATOR_POD=$(oc get pods -n "$NAMESPACE" -o name 2>/dev/null | grep ceph-csi-controller-manager | head -1)
if [ -n "$OPERATOR_POD" ]; then
    oc delete "$OPERATOR_POD" -n "$NAMESPACE" 2>/dev/null || true
fi
sleep 10

echo "Waiting for operator to reconcile deployment..."
for i in $(seq 1 36); do
    CONTAINERS=$(oc get deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" \
      -o jsonpath='{.spec.template.spec.containers[*].name}')
    VOLS=$(oc get deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" \
      -o jsonpath='{.spec.template.spec.volumes[*].name}')
    HAS_SIDECAR=false
    HAS_TLS_VOL=false
    echo "$CONTAINERS" | grep -q "csi-snapshot-metadata" && HAS_SIDECAR=true
    echo "$VOLS" | grep -q "tls-key" && HAS_TLS_VOL=true

    if $HAS_SIDECAR && $HAS_TLS_VOL; then
        echo "Operator has reconciled: sidecar injected, TLS volume mounted."
        break
    fi
    echo "  Waiting... sidecar=$HAS_SIDECAR tls-vol=$HAS_TLS_VOL ($i/36)"
    sleep 10
done

if ! $HAS_SIDECAR || ! $HAS_TLS_VOL; then
    echo "ERROR: Operator did not fully reconcile after 6 minutes."
    echo "  sidecar=$HAS_SIDECAR tls-vol=$HAS_TLS_VOL"
    echo "  Check operator logs: oc logs deployment/ceph-csi-controller-manager -n $NAMESPACE --tail=30"
    exit 1
fi

# Wait for rollout to complete
echo ""
echo "Waiting for deployment rollout..."
oc rollout status deployment/"${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" --timeout=300s

echo ""
echo "=== Step 4 Complete ==="
echo "CBT sidecar is configured. Run step5 to verify."
