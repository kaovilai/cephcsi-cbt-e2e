#!/bin/bash
set -euo pipefail

NS="${NS:-cbt-debug}"
SC="${SC:-ocs-storagecluster-ceph-rbd}"
SNAPCLASS="${SNAPCLASS:-ocs-storagecluster-rbdplugin-snapclass}"
POOL="${POOL:-ocs-storagecluster-cephblockpool}"
CEPHCSI_NS="${CEPHCSI_NS:-openshift-storage}"
TOOLBOX=""

find_toolbox() {
    TOOLBOX=$(oc get pods -n "$CEPHCSI_NS" -l app=rook-ceph-tools -o jsonpath='{.items[0].metadata.name}')
}

echo "=== Setting up debug namespace ==="
oc create ns "$NS" --dry-run=client -o yaml | oc apply -f -

echo "=== Creating PVC ==="
oc apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: debug-pvc
  namespace: ${NS}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: ${SC}
  volumeMode: Block
  resources:
    requests:
      storage: 1Gi
EOF

echo "Waiting for PVC to be bound..."
oc wait -n "$NS" pvc/debug-pvc --for=jsonpath='{.status.phase}'=Bound --timeout=120s

echo "=== Creating pod to write data ==="
oc apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: debug-writer
  namespace: ${NS}
spec:
  containers:
  - name: writer
    image: registry.access.redhat.com/ubi9/ubi:latest
    command: ["sleep", "infinity"]
    volumeDevices:
    - name: data
      devicePath: /dev/xvda
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: debug-pvc
  restartPolicy: Never
EOF

echo "Waiting for pod to be ready..."
oc wait -n "$NS" pod/debug-writer --for=condition=Ready --timeout=120s

echo "=== Writing 1MiB of data at offset 0 ==="
oc exec -n "$NS" debug-writer -- dd if=/dev/urandom of=/dev/xvda bs=1M count=1 conv=notrunc 2>&1

echo "=== Deleting pod before snapshot ==="
oc delete pod -n "$NS" debug-writer --wait=true

echo "=== Creating snapshot ==="
oc apply -f - <<EOF
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: debug-snap1
  namespace: ${NS}
spec:
  volumeSnapshotClassName: ${SNAPCLASS}
  source:
    persistentVolumeClaimName: debug-pvc
EOF

echo "Waiting for snapshot to be ready..."
READY="false"
for _ in {1..60}; do
    READY=$(oc get volumesnapshot -n "$NS" debug-snap1 -o jsonpath='{.status.readyToUse}' 2>/dev/null || echo "false")
    if [ "$READY" = "true" ]; then
        echo "Snapshot ready!"
        break
    fi
    sleep 2
done
if [ "$READY" != "true" ]; then
    echo "ERROR: Snapshot did not become ready within 120s."
    exit 1
fi

echo ""
echo "=== Snapshot details ==="
oc get volumesnapshot -n "$NS" debug-snap1 -o jsonpath='{.status}' | python3 -m json.tool 2>/dev/null || oc get volumesnapshot -n "$NS" debug-snap1 -o yaml | grep -A20 'status:'

echo ""
echo "=== PV details (get RBD image name) ==="
PV_NAME=$(oc get pvc -n "$NS" debug-pvc -o jsonpath='{.spec.volumeName}')
echo "PV: $PV_NAME"
RBD_IMAGE=$(oc get pv "$PV_NAME" -o jsonpath='{.spec.csi.volumeAttributes.imageName}')
echo "RBD image: $RBD_IMAGE"

echo ""
echo "=== Snapshot content details ==="
VSC_NAME=$(oc get volumesnapshot -n "$NS" debug-snap1 -o jsonpath='{.status.boundVolumeSnapshotContentName}')
echo "VolumeSnapshotContent: $VSC_NAME"
SNAP_HANDLE=$(oc get volumesnapshotcontent "$VSC_NAME" -o jsonpath='{.status.snapshotHandle}')
echo "Snapshot handle: $SNAP_HANDLE"

find_toolbox

echo ""
echo "=== RBD images in pool ==="
oc exec -n "$CEPHCSI_NS" "$TOOLBOX" -- rbd ls "$POOL"

echo ""
echo "=== RBD info + snap ls for each image ==="
for img in $(oc exec -n "$CEPHCSI_NS" "$TOOLBOX" -- rbd ls "$POOL"); do
    echo ""
    echo "--- Image: $img ---"
    oc exec -n "$CEPHCSI_NS" "$TOOLBOX" -- rbd info "$POOL/$img" 2>/dev/null | grep -E 'size|features|parent|block_name_prefix'
    echo "Snapshots:"
    oc exec -n "$CEPHCSI_NS" "$TOOLBOX" -- rbd snap ls "$POOL/$img" 2>/dev/null || echo "(none)"
done

echo ""
echo "=== rbd snap diff on source image ==="
SNAP_JSON=$(oc exec -n "$CEPHCSI_NS" "$TOOLBOX" -- rbd snap ls "$POOL/$RBD_IMAGE" --format json 2>/dev/null)
echo "Snap list JSON: $SNAP_JSON"
SNAP_NAME=$(echo "$SNAP_JSON" | python3 -c "import json,sys; snaps=json.load(sys.stdin); print(snaps[-1]['name'])" 2>/dev/null || echo "")
if [ -n "$SNAP_NAME" ]; then
    echo ""
    echo "Running: rbd snap diff $POOL/$RBD_IMAGE@$SNAP_NAME --format json"
    DIFF_OUT=$(oc exec -n "$CEPHCSI_NS" "$TOOLBOX" -- rbd snap diff "$POOL/$RBD_IMAGE@$SNAP_NAME" --format json 2>&1)
    echo "$DIFF_OUT" | head -c 3000
    echo ""
    echo "Diff entry count: $(echo "$DIFF_OUT" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "parse error")"
else
    echo "No snapshots found on source image $RBD_IMAGE"
fi

echo ""
echo "=== Check snapshot clone image for snap diff ==="
# CephCSI creates a clone image for the snapshot (csi-snap-*)
for img in $(oc exec -n "$CEPHCSI_NS" "$TOOLBOX" -- rbd ls "$POOL"); do
    if [[ "$img" == csi-snap-* ]]; then
        echo ""
        echo "--- Snapshot clone: $img ---"
        oc exec -n "$CEPHCSI_NS" "$TOOLBOX" -- rbd info "$POOL/$img" 2>/dev/null | grep -E 'size|features|parent'
        CLONE_SNAPS=$(oc exec -n "$CEPHCSI_NS" "$TOOLBOX" -- rbd snap ls "$POOL/$img" --format json 2>/dev/null)
        echo "Snapshots: $CLONE_SNAPS"
        CLONE_SNAP=$(echo "$CLONE_SNAPS" | python3 -c "import json,sys; snaps=json.load(sys.stdin); print(snaps[-1]['name'])" 2>/dev/null || echo "")
        if [ -n "$CLONE_SNAP" ]; then
            echo "Running: rbd snap diff $POOL/$img@$CLONE_SNAP --format json"
            oc exec -n "$CEPHCSI_NS" "$TOOLBOX" -- rbd snap diff "$POOL/$img@$CLONE_SNAP" --format json 2>&1 | head -c 3000
            echo ""
        fi
    fi
done

echo ""
echo "=== Done. Now check CephCSI driver version ==="
oc exec -n "$CEPHCSI_NS" "$TOOLBOX" -- ceph version
echo ""
echo "=== CephCSI image version ==="
CTRLPLUGIN_POD=$(oc get pods -n "$CEPHCSI_NS" -l "app=openshift-storage.rbd.csi.ceph.com-ctrlplugin" --no-headers -o custom-columns=NAME:.metadata.name 2>/dev/null | head -1)
if [ -n "$CTRLPLUGIN_POD" ]; then
    oc get pod -n "$CEPHCSI_NS" "$CTRLPLUGIN_POD" -o jsonpath='{range .spec.containers[*]}{.name}: {.image}{"\n"}{end}' 2>/dev/null
else
    echo "(ctrlplugin pod not found)"
fi
