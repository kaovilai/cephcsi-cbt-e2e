#!/bin/bash
# Deploy csi-external-snapshot-metadata sidecar for CephCSI RBD on ODF/OCP
#
# This script deploys the CBT sidecar alongside the CephCSI RBD controller plugin.
# It handles the ODF 4.21 operator gap where the sidecar is auto-injected but
# without the TLS certificate volume mount.
#
# Prerequisites:
#   - OCP 4.20+ with DevPreviewNoUpgrade feature set enabled
#   - ODF installed with CephCSI RBD
#   - oc CLI with cluster-admin access
#
# Usage:
#   ./deploy-sidecar.sh          # Deploy everything
#   ./deploy-sidecar.sh cleanup  # Remove all resources

set -euo pipefail

NAMESPACE="openshift-storage"
DEPLOYMENT="openshift-storage.rbd.csi.ceph.com-ctrlplugin"
SERVICE_ACCOUNT="ceph-csi-rbd-ctrlplugin-sa"
DRIVER_NAME="openshift-storage.rbd.csi.ceph.com"
OPERATOR_DEPLOYMENT="ceph-csi-controller-manager"
SCC_NAME="ceph-csi-op-scc"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
err()  { echo -e "${RED}[ERROR]${NC} $*"; }

check_prereqs() {
    log "Checking prerequisites..."

    if ! oc whoami &>/dev/null; then
        err "Not logged in to OpenShift. Run 'oc login' first."
        exit 1
    fi

    local feature_set
    feature_set=$(oc get featuregate cluster -o jsonpath='{.spec.featureSet}' 2>/dev/null || echo "")
    if [[ "$feature_set" != "DevPreviewNoUpgrade" ]]; then
        err "DevPreviewNoUpgrade feature set is not enabled."
        echo "  Run: oc patch featuregate cluster --type=merge -p '{\"spec\":{\"featureSet\":\"DevPreviewNoUpgrade\"}}'"
        exit 1
    fi

    if ! oc get crd snapshotmetadataservices.cbt.storage.k8s.io &>/dev/null; then
        err "SnapshotMetadataService CRD not found. DevPreviewNoUpgrade may not have fully rolled out yet."
        exit 1
    fi

    if ! oc get deployment "$DEPLOYMENT" -n "$NAMESPACE" &>/dev/null; then
        err "RBD ctrlplugin deployment '$DEPLOYMENT' not found in '$NAMESPACE'."
        exit 1
    fi

    log "All prerequisites met."
}

get_sidecar_image() {
    # Use upstream image matching go.mod client library version (v0.2.0).
    # The OCP release payload ships v0.1.0-based sidecar which has an API
    # mismatch with the v0.2.0 client (base_snapshot_name vs base_snapshot_id).
    # See: https://github.com/kaovilai/cephcsi-cbt-e2e/issues/9
    SIDECAR_IMAGE="registry.k8s.io/sig-storage/csi-snapshot-metadata:v0.2.0"
    # Commented out: OCP release payload image (v0.1.0 API)
    # log "Getting sidecar image from cluster release payload..."
    # local release_image
    # release_image=$(oc get clusterversion version -o jsonpath='{.status.desired.image}')
    # SIDECAR_IMAGE=$(oc adm release info "$release_image" --image-for=csi-external-snapshot-metadata)
    log "Sidecar image: $SIDECAR_IMAGE"
}

create_rbac() {
    log "Creating RBAC ClusterRoles..."
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

    log "Creating ClusterRoleBinding for $SERVICE_ACCOUNT..."
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
  name: ${SERVICE_ACCOUNT}
  namespace: ${NAMESPACE}
EOF
}

create_service() {
    log "Creating Service with auto-TLS certificate annotation..."
    oc apply -f - <<EOF
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
    app: ${DEPLOYMENT}
EOF

    log "Waiting for TLS certificate secret..."
    for i in $(seq 1 30); do
        if oc get secret -n "$NAMESPACE" csi-snapshot-metadata-certs &>/dev/null; then
            log "TLS certificate secret created."
            return
        fi
        sleep 1
    done
    err "TLS certificate secret was not created within 30s."
    exit 1
}

create_sms() {
    log "Creating SnapshotMetadataService CR..."
    local ca_cert
    ca_cert=$(oc get secrets -n "$NAMESPACE" csi-snapshot-metadata-certs \
        -o jsonpath='{.data.tls\.crt}')

    oc apply -f - <<EOF
apiVersion: cbt.storage.k8s.io/v1beta1
kind: SnapshotMetadataService
metadata:
  name: ${DRIVER_NAME}
spec:
  address: csi-snapshot-metadata.${NAMESPACE}.svc:6443
  caCert: ${ca_cert}
  audience: ${DRIVER_NAME}
EOF
}

fix_scc() {
    log "Checking SCC for secret volume support..."
    local volumes
    volumes=$(oc get scc "$SCC_NAME" -o jsonpath='{.volumes}' 2>/dev/null || echo "")

    if echo "$volumes" | grep -q '"secret"'; then
        log "SCC already allows secret volumes."
    else
        log "Adding 'secret' to allowed volume types in $SCC_NAME..."
        oc patch scc "$SCC_NAME" --type=json \
            -p '[{"op":"add","path":"/volumes/-","value":"secret"}]'
    fi
}

scale_operator() {
    local replicas=$1
    log "Scaling $OPERATOR_DEPLOYMENT to $replicas replicas..."
    oc scale deployment "$OPERATOR_DEPLOYMENT" -n "$NAMESPACE" --replicas="$replicas"

    if [[ "$replicas" == "0" ]]; then
        log "Waiting for operator to stop..."
        oc rollout status deployment/"$OPERATOR_DEPLOYMENT" -n "$NAMESPACE" --timeout=60s 2>/dev/null || true
    fi
}

patch_deployment() {
    log "Checking if sidecar container exists in deployment..."
    local sidecar_idx
    sidecar_idx=$(oc get deployment "$DEPLOYMENT" -n "$NAMESPACE" -o json | \
        jq '[.spec.template.spec.containers | to_entries[] | select(.value.name == "csi-snapshot-metadata") | .key][0]')

    if [[ "$sidecar_idx" == "null" ]]; then
        warn "Sidecar container not found. The operator may need the SMS CR to auto-inject it."
        warn "Waiting 30s for operator to inject sidecar..."
        sleep 30
        sidecar_idx=$(oc get deployment "$DEPLOYMENT" -n "$NAMESPACE" -o json | \
            jq '[.spec.template.spec.containers | to_entries[] | select(.value.name == "csi-snapshot-metadata") | .key][0]')

        if [[ "$sidecar_idx" == "null" ]]; then
            err "Sidecar container still not found. Manual injection may be needed."
            exit 1
        fi
    fi

    log "Sidecar container found at index $sidecar_idx."

    # Check if cert volume already exists
    local has_cert_vol
    has_cert_vol=$(oc get deployment "$DEPLOYMENT" -n "$NAMESPACE" -o json | \
        jq '[.spec.template.spec.volumes[] | select(.name == "csi-snapshot-metadata-server-certs")] | length')

    if [[ "$has_cert_vol" != "0" ]]; then
        log "Cert volume already exists in deployment."
        return
    fi

    log "Scaling down operator to prevent reconciliation..."
    scale_operator 0

    log "Patching deployment with TLS cert volume and mount..."
    oc patch deployment "$DEPLOYMENT" -n "$NAMESPACE" --type=json -p "[
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
        \"path\": \"/spec/template/spec/containers/${sidecar_idx}/volumeMounts/-\",
        \"value\": {
          \"name\": \"csi-snapshot-metadata-server-certs\",
          \"mountPath\": \"/tmp/certificates\",
          \"readOnly\": true
        }
      }
    ]"

    log "Waiting for deployment rollout..."
    oc rollout status deployment/"$DEPLOYMENT" -n "$NAMESPACE" --timeout=180s
}

verify() {
    log "Verifying deployment..."

    local ready
    ready=$(oc get deployment "$DEPLOYMENT" -n "$NAMESPACE" \
        -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")

    if [[ "$ready" -ge 1 ]]; then
        log "Deployment has $ready ready replicas."
    else
        err "No ready replicas. Check pod events:"
        oc get pods -n "$NAMESPACE" -l "app=$DEPLOYMENT" --no-headers
        exit 1
    fi

    log "Checking sidecar logs..."
    local logs
    logs=$(oc logs -n "$NAMESPACE" "deployment/$DEPLOYMENT" \
        -c csi-snapshot-metadata --tail=5 2>/dev/null || echo "FAILED")

    if echo "$logs" | grep -q "GRPC server started\|CSI driver name"; then
        log "Sidecar is running and connected to CSI driver."
    else
        warn "Could not confirm sidecar is healthy. Check logs manually:"
        echo "$logs"
    fi

    log ""
    log "=== Deployment Summary ==="
    log "SnapshotMetadataService: $(oc get snapshotmetadataservice "$DRIVER_NAME" -o jsonpath='{.metadata.name}' 2>/dev/null || echo 'NOT FOUND')"
    log "Service:                 csi-snapshot-metadata.$NAMESPACE.svc:6443"
    log "TLS Secret:              csi-snapshot-metadata-certs"
    log "Sidecar Image:           $(oc get deployment "$DEPLOYMENT" -n "$NAMESPACE" -o json | jq -r '.spec.template.spec.containers[] | select(.name == "csi-snapshot-metadata") | .image' 2>/dev/null)"
    log "Operator Scaled Down:    $(oc get deployment "$OPERATOR_DEPLOYMENT" -n "$NAMESPACE" -o jsonpath='{.spec.replicas}' 2>/dev/null) replicas"
    log ""
    warn "The ceph-csi-controller-manager is scaled to 0. It will revert the TLS"
    warn "volume mount if scaled back up. Keep it down while testing CBT."
}

cleanup() {
    log "Cleaning up snapshot-metadata resources..."

    oc delete snapshotmetadataservice "$DRIVER_NAME" --ignore-not-found 2>/dev/null || true
    log "Scaling operator back up..."
    scale_operator 1 2>/dev/null || true
    sleep 10

    oc delete clusterrolebinding ceph-csi-rbd-snapshot-metadata-runner --ignore-not-found 2>/dev/null || true
    oc delete clusterrole external-snapshot-metadata-runner --ignore-not-found 2>/dev/null || true
    oc delete clusterrole external-snapshot-metadata-client-runner --ignore-not-found 2>/dev/null || true
    oc delete service csi-snapshot-metadata -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true

    # Remove 'secret' from SCC volumes
    local volumes
    volumes=$(oc get scc "$SCC_NAME" -o jsonpath='{.volumes}' 2>/dev/null || echo "")
    if echo "$volumes" | grep -q '"secret"'; then
        local idx
        idx=$(oc get scc "$SCC_NAME" -o json | jq '[.volumes | to_entries[] | select(.value == "secret") | .key][0]')
        if [[ "$idx" != "null" ]]; then
            oc patch scc "$SCC_NAME" --type=json \
                -p "[{\"op\":\"remove\",\"path\":\"/volumes/$idx\"}]" 2>/dev/null || true
        fi
    fi

    log "Cleanup complete. Operator will reconcile and remove the sidecar container."
}

# Main
case "${1:-deploy}" in
    deploy)
        check_prereqs
        get_sidecar_image
        create_rbac
        create_service
        create_sms
        fix_scc
        patch_deployment
        verify
        ;;
    cleanup)
        cleanup
        ;;
    *)
        echo "Usage: $0 [deploy|cleanup]"
        exit 1
        ;;
esac
