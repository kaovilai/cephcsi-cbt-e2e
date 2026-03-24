#!/bin/bash
# Step 3: Create ODF StorageCluster on worker nodes
# Requires: ODF operator installed (step 2)
set -euo pipefail

echo "=== Step 3: Create ODF StorageCluster ==="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

# OSD storage class: standard-csi (OpenStack), gp3-csi (AWS), thin-csi (vSphere)
OSD_STORAGE_CLASS="${OSD_STORAGE_CLASS:-standard-csi}"
ODF_PROFILE="${ODF_PROFILE:-lean}"
echo "Using OSD StorageClass: $OSD_STORAGE_CLASS"
echo "Using ODF performance profile: $ODF_PROFILE"

# Wait for odf-operator CSV to be Succeeded
echo "Verifying ODF operator is ready..."
for i in $(seq 1 20); do
    if oc get csv -n openshift-storage 2>/dev/null | grep -q "odf-operator.*Succeeded"; then
        echo "ODF operator is ready."
        break
    fi
    echo "  Waiting for ODF operator CSV... ($i/20)"
    sleep 15
done

if ! oc get csv -n openshift-storage 2>/dev/null | grep -q "odf-operator.*Succeeded"; then
    echo "ERROR: ODF operator CSV not ready after 5 minutes."
    exit 1
fi

# Also wait for ocs-operator CSV
echo "Waiting for ocs-operator CSV..."
for i in $(seq 1 40); do
    if oc get csv -n openshift-storage 2>/dev/null | grep -q "ocs-operator.*Succeeded"; then
        echo "OCS operator is ready."
        break
    fi
    echo "  Waiting for ocs-operator CSV... ($i/40)"
    sleep 15
done

if ! oc get csv -n openshift-storage 2>/dev/null | grep -q "ocs-operator.*Succeeded"; then
    echo "ERROR: OCS operator CSV not ready after 10 minutes."
    exit 1
fi

# Label worker nodes for ODF storage (required for scheduling OSD and provider pods)
echo ""
echo "Labeling worker nodes for ODF storage..."
oc label nodes -l node-role.kubernetes.io/worker cluster.ocs.openshift.io/openshift-storage= --overwrite

# Check if StorageCluster already exists
if oc get storagecluster -n openshift-storage ocs-storagecluster &>/dev/null; then
    echo "StorageCluster 'ocs-storagecluster' already exists."
    oc get storagecluster -n openshift-storage ocs-storagecluster
else
    # Get worker node names for placement
    echo ""
    echo "Worker nodes:"
    oc get nodes -l node-role.kubernetes.io/worker -o name

    # Create StorageCluster
    echo ""
    echo "Creating StorageCluster..."
    oc apply -f - <<EOF
apiVersion: ocs.openshift.io/v1
kind: StorageCluster
metadata:
  name: ocs-storagecluster
  namespace: openshift-storage
spec:
  resourceProfile: ${ODF_PROFILE}
  multiCloudGateway:
    reconcileStrategy: ignore
  manageNodes: false
  monDataDirHostPath: /var/lib/rook
  storageDeviceSets:
  - count: 1
    dataPVCTemplate:
      spec:
        accessModes:
        - ReadWriteOnce
        resources:
          requests:
            storage: 50Gi
        storageClassName: ${OSD_STORAGE_CLASS}
        volumeMode: Block
    name: ocs-deviceset
    placement: {}
    portable: true
    replica: 3
EOF
    echo "StorageCluster created."
fi

echo ""
echo "Waiting for StorageCluster to become Ready..."
for i in $(seq 1 80); do
    PHASE=$(oc get storagecluster -n openshift-storage ocs-storagecluster -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    if [ "$PHASE" = "Ready" ]; then
        echo "StorageCluster is Ready!"
        break
    fi
    echo "  Attempt $i/80: phase='$PHASE', waiting 15s..."
    sleep 15
done

PHASE=$(oc get storagecluster -n openshift-storage ocs-storagecluster -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
if [ "$PHASE" != "Ready" ]; then
    echo "ERROR: StorageCluster not Ready after 20 minutes (phase=$PHASE)"
    echo "Check: oc get pods -n openshift-storage"
    echo "Check: oc describe storagecluster -n openshift-storage ocs-storagecluster"
    exit 1
fi

echo ""
echo "Storage classes:"
oc get sc | grep -E "NAME|ocs"
echo ""
echo "VolumeSnapshotClasses:"
oc get volumesnapshotclass 2>/dev/null | grep -E "NAME|ocs" || echo "  (none yet)"

# --- Enable Ceph toolbox (required for e2e tests to check Ceph version, RBD introspection) ---
echo ""
echo "--- Enabling Ceph toolbox ---"
if oc get pods -n "$NAMESPACE" -l app=rook-ceph-tools --no-headers 2>/dev/null | grep -q .; then
    echo "Ceph toolbox pod already exists."
else
    oc patch storagecluster ocs-storagecluster -n "$NAMESPACE" --type merge \
      -p '{"spec":{"enableCephTools": true}}'
    echo "Waiting for Ceph toolbox pod to be ready..."
    for i in $(seq 1 24); do
        if oc get pods -n "$NAMESPACE" -l app=rook-ceph-tools --no-headers 2>/dev/null | grep -q "Running"; then
            echo "Ceph toolbox pod is ready."
            break
        fi
        echo "  Waiting for toolbox pod... ($i/24)"
        sleep 10
    done
    if ! oc get pods -n "$NAMESPACE" -l app=rook-ceph-tools --no-headers 2>/dev/null | grep -q "Running"; then
        echo "WARNING: Ceph toolbox pod not ready after 4 minutes. E2E tests may fail."
    fi
fi

echo ""
echo "=== Step 3 Complete ==="
