#!/bin/bash
# Step 4: Configure CBT snapshot-metadata sidecar for CephCSI RBD
# Reference: https://access.redhat.com/articles/7130698
# Also references: deploy-snapshot-metadata-sidecar.md in this repo
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

# --- 4f: Wait for operator to auto-inject sidecar ---
echo ""
echo "--- 4f: Waiting for operator to auto-inject sidecar container ---"
# Restart the operator pod to trigger reconciliation with the new SnapshotMetadataService CR
echo "Restarting ceph-csi-controller-manager pod to trigger reconciliation..."
oc delete pod -n "$NAMESPACE" -l app.kubernetes.io/name=ceph-csi-operator --ignore-not-found 2>/dev/null || true
oc delete pod -n "$NAMESPACE" -l control-plane=controller-manager --field-selector metadata.name!=ocs-client-operator-controller-manager --ignore-not-found 2>/dev/null || true
# Also try direct pod deletion by deployment label
OPERATOR_POD=$(oc get pods -n "$NAMESPACE" -o name 2>/dev/null | grep ceph-csi-controller-manager | head -1)
if [ -n "$OPERATOR_POD" ]; then
    oc delete "$OPERATOR_POD" -n "$NAMESPACE" 2>/dev/null || true
fi
sleep 20
for i in $(seq 1 24); do
    CONTAINERS=$(oc get deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" \
      -o jsonpath='{.spec.template.spec.containers[*].name}')
    if echo "$CONTAINERS" | grep -q "csi-snapshot-metadata"; then
        echo "Operator has injected the csi-snapshot-metadata container."
        break
    fi
    echo "  Waiting for sidecar injection... ($i/24)"
    sleep 10
done

if ! echo "$CONTAINERS" | grep -q "csi-snapshot-metadata"; then
    echo "ERROR: Sidecar not injected after 4 minutes. Check operator logs:"
    echo "  oc logs deployment/ceph-csi-controller-manager -n $NAMESPACE --tail=20"
    exit 1
fi

# --- 4g: Scale down operators to prevent reconciliation ---
# WARNING: This disables the ceph-csi and ocs-client operators, preventing automatic
# updates and self-healing. The operators would revert our SCC and TLS volume mount
# patches on next reconcile.
echo ""
echo "--- 4g: Scaling down operators ---"
echo "WARNING: Scaling operators to 0 replicas to prevent reconciliation."
echo "  Re-enable with:"
echo "  oc scale deployment ceph-csi-controller-manager -n $NAMESPACE --replicas=1"
echo "  oc scale deployment ocs-client-operator-controller-manager -n $NAMESPACE --replicas=1"
oc scale deployment ceph-csi-controller-manager -n "$NAMESPACE" --replicas=0
oc scale deployment ocs-client-operator-controller-manager -n "$NAMESPACE" --replicas=0
echo "Operators scaled down."

# --- 4h: Patch SCC to allow secret volumes ---
# Done after operator scale-down to prevent the ocs-client-operator from reverting it
echo ""
echo "--- 4h: Patching SCC to allow secret volumes ---"
VOLUMES=$(oc get scc ceph-csi-op-scc -o jsonpath='{.volumes}' 2>/dev/null || echo "")
if echo "$VOLUMES" | grep -q '"secret"'; then
    echo "SCC already allows secret volumes."
else
    oc patch scc ceph-csi-op-scc --type=json \
      -p '[{"op":"add","path":"/volumes/-","value":"secret"}]'
    echo "SCC patched to allow secret volumes."
fi

# --- 4i: Patch deployment to add TLS cert volume mount ---
echo ""
echo "--- 4i: Patching deployment to add TLS volume mount ---"

# Find the sidecar container index
SIDECAR_IDX=$(oc get deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" -o json | \
  jq '[.spec.template.spec.containers | to_entries[] | select(.value.name == "csi-snapshot-metadata") | .key][0]')

if [ -z "$SIDECAR_IDX" ] || [ "$SIDECAR_IDX" = "null" ]; then
    echo "ERROR: csi-snapshot-metadata container not found in deployment."
    echo "Available containers: $(oc get deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[*].name}')"
    exit 1
fi

echo "Sidecar container index: $SIDECAR_IDX"

# Check if volume already exists
EXISTING_VOL=$(oc get deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" -o json | \
  jq '.spec.template.spec.volumes[]? | select(.name == "csi-snapshot-metadata-server-certs")' 2>/dev/null || echo "")

if [ -n "$EXISTING_VOL" ]; then
    echo "TLS volume already exists in deployment."
else
    oc patch deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" --type=json -p "[
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
    echo "Deployment patched with TLS volume mount."
fi

# Wait for rollout - scale down and back up to ensure fresh pods use the updated SCC
echo ""
echo "Waiting for deployment rollout..."
oc scale deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" --replicas=0
sleep 5
oc scale deployment "${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" --replicas=2
oc rollout status deployment/"${CSI_DRIVER_NAME}-ctrlplugin" -n "$NAMESPACE" --timeout=300s

echo ""
echo "=== Step 4 Complete ==="
echo "CBT sidecar is configured. Run step5 to verify."
