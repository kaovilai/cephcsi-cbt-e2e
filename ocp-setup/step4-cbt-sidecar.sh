#!/bin/bash
# Step 4: Configure CBT snapshot-metadata sidecar for CephCSI RBD
# Reference: https://github.com/red-hat-storage/ceph-csi-operator/blob/main/docs/features/rbd-snapshot-metadata.md
# Also references: https://access.redhat.com/articles/7130698
# Requires: ODF installed with RBD (step 3)
#
# What this script does, by ODF/OCP version:
#   - ODF <= 4.22 (OCP <= 4.22): the manual flow below. Creates RBAC, a Service with a
#     serving-cert secret, the SnapshotMetadataService (SMS) CR, and a "tls-key" volume in
#     the RBD Driver CR (which triggers sidecar injection by ceph-csi-controller-manager).
#     On ODF <= 4.21 it also overrides the sidecar image (step 4g), because early ODF 4.21
#     builds ship a sidecar that breaks GetMetadataDelta.
#   - ODF 5.x / OCP >= 4.23: ocs-client-operator creates the Service, TLS secret and
#     "tls-key" volume itself. The script detects that (Service
#     openshift-storage-rbd-snapshot-metadata), skips the manual Service/TLS/Driver CR
#     steps, and only creates the SMS CR. This is based on ocs-client-operator source
#     (operatorconfigmap_controller.go, pkg/templates/csi.go), not on a live ODF 5.0 cluster.
#
# Environment overrides:
#   SIDECAR_IMAGE  Sidecar image used by the ODF <= 4.21 override (step 4g).
#                  Default: registry.k8s.io/sig-storage/csi-snapshot-metadata:v0.2.0
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

ODF_VERSION=$(detect_odf_version)
echo "Detected ODF version: ${ODF_VERSION:-unknown}"

# ocs-client-operator (enabled on OCP >= 4.23 / 5.x) manages the sidecar Service, TLS secret
# and the "tls-key" Driver CR volume itself. Its Service is named
# openshift-storage-rbd-snapshot-metadata (templates.SnapshotMetadataServiceName). The manual
# Service created below is named csi-snapshot-metadata, so the two cannot be confused with
# a previous run of this script.
OPERATOR_SMS_SERVICE="openshift-storage-rbd-snapshot-metadata"
OPERATOR_MANAGED=false
if oc get service "$OPERATOR_SMS_SERVICE" -n "$NAMESPACE" &>/dev/null; then
    OPERATOR_MANAGED=true
    echo "Service '$OPERATOR_SMS_SERVICE' exists: the sidecar Service/TLS/Driver CR are operator-managed."
    echo "Skipping manual Service/TLS/Driver CR/image steps (4d, 4f, 4g, 4h); only the SMS CR will be created."
fi

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
oc apply -f - <<EOF
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
  namespace: ${NAMESPACE}
EOF
echo "ClusterRoleBinding created."

if ! $OPERATOR_MANAGED; then
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
    TLS_SECRET_FOUND=false
    for i in {1..30}; do
        if oc get secret -n "$NAMESPACE" csi-snapshot-metadata-certs &>/dev/null; then
            echo "TLS secret 'csi-snapshot-metadata-certs' is available."
            TLS_SECRET_FOUND=true
            break
        fi
        echo "  Waiting... ($i/30)"
        sleep 5
    done
    if ! $TLS_SECRET_FOUND; then
        echo "ERROR: TLS secret 'csi-snapshot-metadata-certs' was not created after 150s."
        echo "  Check that the Service has the serving-cert annotation and the cluster has cert-manager."
        exit 1
    fi
fi

# --- 4e: Create SnapshotMetadataService CR ---
echo ""
echo "--- 4e: Creating SnapshotMetadataService CR ---"
if $OPERATOR_MANAGED; then
    # ocs-client-operator writes the SMS spec (address, audience, caCert from the
    # openshift-service-ca.crt bundle, driverName) into a ConfigMap named after the driver,
    # but does not create the SMS CR itself in the code we reviewed (which component consumes
    # the ConfigMap was not confirmed). Create the CR from that ConfigMap unless one exists.
    if oc get snapshotmetadataservice "${CSI_DRIVER_NAME}" &>/dev/null; then
        echo "SnapshotMetadataService CR '${CSI_DRIVER_NAME}' already exists - leaving it unchanged."
    else
        SMS_ADDRESS=$(oc get configmap "${CSI_DRIVER_NAME}" -n "$NAMESPACE" -o jsonpath='{.data.address}' 2>/dev/null || echo "")
        SMS_AUDIENCE=$(oc get configmap "${CSI_DRIVER_NAME}" -n "$NAMESPACE" -o jsonpath='{.data.audience}' 2>/dev/null || echo "")
        SMS_CA_PEM=$(oc get configmap "${CSI_DRIVER_NAME}" -n "$NAMESPACE" -o jsonpath='{.data.caCert}' 2>/dev/null || echo "")
        if [ -z "$SMS_ADDRESS" ] || [ -z "$SMS_AUDIENCE" ] || [ -z "$SMS_CA_PEM" ]; then
            echo "ERROR: operator-managed sidecar detected, but ConfigMap '${CSI_DRIVER_NAME}' in $NAMESPACE"
            echo "  is missing address/audience/caCert. Wait for ocs-client-operator to reconcile and re-run."
            exit 1
        fi
        # SMS spec.caCert is a base64-encoded PEM bundle; the ConfigMap holds raw PEM.
        SMS_CA_B64=$(printf '%s' "$SMS_CA_PEM" | base64 | tr -d '\n')
        oc apply -f - <<EOF
apiVersion: cbt.storage.k8s.io/v1beta1
kind: SnapshotMetadataService
metadata:
  name: ${CSI_DRIVER_NAME}
spec:
  address: ${SMS_ADDRESS}
  caCert: ${SMS_CA_B64}
  audience: ${SMS_AUDIENCE}
EOF
        echo "SnapshotMetadataService CR created from operator-managed ConfigMap."
    fi
else
    CA_CERT=$(oc get secrets -n "$NAMESPACE" csi-snapshot-metadata-certs -o jsonpath='{.data.tls\.crt}' 2>/dev/null || echo "")
    if [ -z "$CA_CERT" ]; then
        echo "ERROR: Could not get CA cert from secret. Aborting."
        exit 1
    fi

    oc apply -f - <<EOF
apiVersion: cbt.storage.k8s.io/v1beta1
kind: SnapshotMetadataService
metadata:
  name: ${CSI_DRIVER_NAME}
spec:
  address: csi-snapshot-metadata.${NAMESPACE}.svc:6443
  caCert: ${CA_CERT}
  audience: ${CSI_DRIVER_NAME}
EOF
    echo "SnapshotMetadataService CR created."
fi

# --- 4f: Add TLS volume to Driver CR ---
# Per official docs: volume and mount names must be "tls-key" (operator filters by this name),
# mount path must be /tmp/certificates. The presence of this volume is what makes
# ceph-csi-operator inject the csi-snapshot-metadata sidecar.
# Ref: https://github.com/red-hat-storage/ceph-csi-operator/blob/main/docs/features/rbd-snapshot-metadata.md#5-add-tls-volume-mount-to-rbd-driver-cr
#
# NOTE: The official docs show a "secret" volume type, but we use "projected" instead.
# On ODF <= 4.21 the ocs-client-operator hardcodes the ceph-csi-op-scc SCC with only configMap,
# emptyDir, hostPath, and projected volume types - no "secret":
# https://github.com/red-hat-storage/ocs-client-operator/blob/6237b4c7d94bcc3f7f495806239b0730fa466fb0/pkg/templates/csi.go#L51-L55
# ("csi: Allow secret volumes in CSI SCC" is present in the release-4.22 and release-5.0
# branches, so the projected workaround is only needed on <= 4.21; it works everywhere.)
# Using a projected volume sourcing from the secret avoids the SCC issue entirely:
# no SCC patch needed, no operator scale-down.
# The ceph-csi-operator passes the volume through to the deployment as-is (see
# internal/controller/driver_controller.go), so projected works identically to secret.
if ! $OPERATOR_MANAGED; then
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
            "projected": {
              "sources": [
                {
                  "secret": {
                    "name": "csi-snapshot-metadata-certs"
                  }
                }
              ]
            }
          }
        }
      ]
    }
  }
}
PATCHEOF
)"
    echo "Driver CR patched with TLS volume (projected)."
fi

# --- 4g: Override sidecar image (ODF <= 4.21 only) ---
# Early ODF 4.21 builds ship an older sidecar image (ose-csi-external-snapshot-metadata-rhel9)
# that uses BaseSnapshotName (name-based lookup) instead of BaseSnapshotId (handle
# passthrough). This breaks GetMetadataDelta because the sidecar tries to look up the CSI
# snapshot handle as a VolumeSnapshot name.
#
# Upstream fix: https://github.com/red-hat-storage/ceph-csi-operator/commit/454e9130
# ("csi: update K8s CSI sidecar images", bumps the default snapshot-metadata v0.1.0 -> v0.2.0).
# It is in the ceph-csi-operator release-4.22, release-5.0 and main branches, not in
# release-4.21. ODF 4.22 ships snapshot-metadata v0.2.0; ODF 5.0 ships v1.0.0
# (ocs-client-operator DEFAULT_CSI_IMG_SNAPSHOT_METADATA_VERSION). The downstream
# openshift/csi-external-snapshot-metadata release-4.21 branch was rebased to v0.2.0, so a
# late ODF 4.21.z build MAY already carry the fix (unverified) - test the shipped image
# before relying on the override. Until then, override via a driver-level ImageSet.
#
# Image choice (SIDECAR_IMAGE):
#   - Default v0.2.0 is the workaround validated by this repo (results.md, OCP 4.21.7).
#   - v1.0.0 and v1.1.0 are SnapshotMetadataService v1beta1-only releases (v1alpha1 removed).
#     Velero validated v1.1.0 live on Ceph/ODF with a hand-applied v1beta1 CRD
#     (https://github.com/velero-io/velero/pull/10307; platform OCP 5.0 + ODF 4.21.10 per
#     https://github.com/velero-io/velero/issues/10294), but this repo has NOT validated it.
#     v1.x also replaces grpc_health_probe with an HTTP /health endpoint on :8080, and the
#     ceph-csi-operator release-4.21/4.22 branches still build against the v0.2.0 client, so
#     test an override such as
#       SIDECAR_IMAGE=registry.k8s.io/sig-storage/csi-snapshot-metadata:v1.1.0
#     (container becomes Ready, probes pass, delta tests pass) before adopting it.
echo ""
echo "--- 4g: Checking if sidecar image override is needed ---"

SIDECAR_IMAGE="${SIDECAR_IMAGE:-registry.k8s.io/sig-storage/csi-snapshot-metadata:v0.2.0}"

# Only override on ODF <= 4.21. version_ge compares MAJOR first, so ODF 5.x (and any
# future major) is never overridden; an unknown version gets the override as a precaution.
if $OPERATOR_MANAGED; then
    echo "Sidecar is operator-managed. Image override skipped."
elif version_ge "$ODF_VERSION" 4 22; then
    echo "ODF $ODF_VERSION >= 4.22 detected. Sidecar image override not needed - skipping."
else
    if [ -z "$ODF_VERSION" ]; then
        echo "WARNING: Could not detect ODF version. Applying override as a precaution."
    else
        echo "ODF $ODF_VERSION <= 4.21. Applying sidecar image override."
    fi

    # Create override ConfigMap from the ODF image set, replacing only snapshot-metadata.
    # The OperatorConfig names the ODF-managed ImageSet ConfigMap. If it is not set, fall back
    # to the ODF-version-based name (e.g. csi-images-v4.21), which is the naming seen on
    # ODF 4.21; the version is unknown only when no ODF CSV was found, so default to 4.21.
    CURRENT_IMAGESET=$(oc get operatorconfigs.csi.ceph.io ceph-csi-operator-config -n "$NAMESPACE" \
      -o jsonpath='{.spec.driverSpecDefaults.imageSet.name}' 2>/dev/null || true)
    CURRENT_IMAGESET="${CURRENT_IMAGESET:-csi-images-v${ODF_VERSION:-4.21}}"
    echo "Base ImageSet ConfigMap: $CURRENT_IMAGESET"

    if oc get configmap "$CURRENT_IMAGESET" -n "$NAMESPACE" &>/dev/null; then
        oc get configmap "$CURRENT_IMAGESET" -n "$NAMESPACE" -o json | \
          jq --arg img "$SIDECAR_IMAGE" \
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
  snapshot-metadata: "${SIDECAR_IMAGE}"
IMGEOF
    fi

    # Point the RBD driver to our override ImageSet (higher priority than operatorConfig)
    oc patch drivers.csi.ceph.io "${CSI_DRIVER_NAME}" -n "$NAMESPACE" --type=merge \
      -p '{"spec":{"imageSet":{"name":"csi-images-override"}}}'
    echo "RBD driver ImageSet overridden to use sidecar image: $SIDECAR_IMAGE"
fi

# --- 4h: Restart operator to reconcile Driver CR changes ---
# The ceph-csi-operator reconciles the Driver CR into the deployment.
# Restarting it picks up the TLS volume, sidecar injection, and ImageSet changes.
# When the sidecar is operator-managed there are no Driver CR changes from this script, so
# only wait for the deployment to carry the sidecar.
echo ""
if $OPERATOR_MANAGED; then
    echo "--- 4h: Operator-managed sidecar: not restarting ceph-csi-controller-manager ---"
else
    echo "--- 4h: Restarting ceph-csi-controller-manager to reconcile ---"
    OPERATOR_POD=$(oc get pods -n "$NAMESPACE" -o name 2>/dev/null | grep ceph-csi-controller-manager | head -1 || true)
    if [ -n "$OPERATOR_POD" ]; then
        oc delete "$OPERATOR_POD" -n "$NAMESPACE" 2>/dev/null || true
    fi
    sleep 10
fi

echo "Waiting for operator to reconcile deployment..."
HAS_SIDECAR=false
HAS_TLS_VOL=false
for i in {1..36}; do
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
