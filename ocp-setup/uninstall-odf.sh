#!/bin/bash
# Uninstall OpenShift Data Foundation (ODF) from the cluster
#
# Based on official Red Hat ODF documentation:
#   ocp-setup/Uninstalling OpenShift Data Foundation in Internal mode - Red Hat Customer Portal.pdf
#
# Usage: ./uninstall-odf.sh [--force] [--skip-crd-cleanup] [--keep-namespace]
#
# Options:
#   --force            Use forced uninstall mode (skip waiting for PVC deletion)
#   --skip-crd-cleanup Skip CRD deletion (useful if other clusters share the CRDs)
#   --keep-namespace   Don't delete the openshift-storage namespace
set -euo pipefail

NAMESPACE="openshift-storage"
FORCE_MODE=false
SKIP_CRD_CLEANUP=false
KEEP_NAMESPACE=false

for arg in "$@"; do
    case "$arg" in
        --force) FORCE_MODE=true ;;
        --skip-crd-cleanup) SKIP_CRD_CLEANUP=true ;;
        --keep-namespace) KEEP_NAMESPACE=true ;;
    esac
done

echo "=== ODF Uninstall ==="
echo ""

# --- Preflight ---
echo "--- Preflight ---"
if ! oc whoami &>/dev/null; then
    echo "ERROR: Not logged in to cluster."
    exit 1
fi

if ! oc get ns "$NAMESPACE" &>/dev/null; then
    echo "Namespace '$NAMESPACE' does not exist. ODF may already be uninstalled."
    echo "Checking for leftover resources..."
    # Still run cleanup for node labels, storage classes, CRDs
else
    echo "Namespace '$NAMESPACE' exists. Proceeding with uninstall."
fi

# --- Phase 1: Check for PVCs/OBCs using ODF storage classes ---
echo ""
echo "--- Phase 1: Check for consumers of ODF storage ---"

ODF_STORAGE_CLASSES="ocs-storagecluster-ceph-rbd ocs-storagecluster-cephfs ocs-storagecluster-ceph-rgw openshift-storage.noobaa.io"
HAS_CONSUMERS=false

CRITICAL_NS="openshift-monitoring openshift-logging openshift-image-registry"

for sc in $ODF_STORAGE_CLASSES; do
    PVCS=$(oc get pvc -A -o jsonpath="{range .items[?(@.spec.storageClassName==\"$sc\")]}{.metadata.namespace}/{.metadata.name}{\"\\n\"}{end}" 2>/dev/null || echo "")
    if [ -n "$PVCS" ]; then
        echo "  PVCs using '$sc':"
        echo "$PVCS" | sed 's/^/    /'
        HAS_CONSUMERS=true
        # Warn about cluster-critical namespace PVCs with remediation steps
        if echo "$PVCS" | grep -q "^openshift-monitoring/"; then
            echo ""
            echo "  ** WARNING: Monitoring PVCs use ODF storage."
            echo "     Before uninstalling, remove ODF references from monitoring config:"
            echo "       oc -n openshift-monitoring edit configmap cluster-monitoring-config"
            echo "       # Remove volumeClaimTemplate sections referencing ODF storage classes"
            echo "       # Wait for prometheus/alertmanager pods to restart"
            echo "       oc -n openshift-monitoring delete pvc -l app.kubernetes.io/name=prometheus"
            echo "       oc -n openshift-monitoring delete pvc -l app.kubernetes.io/name=alertmanager"
        fi
        if echo "$PVCS" | grep -q "^openshift-image-registry/"; then
            echo ""
            echo "  ** WARNING: Image registry PVCs use ODF storage."
            echo "     Before uninstalling, remove ODF references from registry config:"
            echo "       oc patch configs.imageregistry.operator.openshift.io cluster --type=json -p '[{\"op\":\"remove\",\"path\":\"/spec/storage/pvc\"}]'"
            echo "       oc -n openshift-image-registry delete pvc --all"
        fi
        if echo "$PVCS" | grep -q "^openshift-logging/"; then
            echo ""
            echo "  ** WARNING: Logging PVCs use ODF storage."
            echo "     Before uninstalling, remove the ClusterLogging instance:"
            echo "       oc -n openshift-logging delete clusterlogging instance"
            echo "       oc -n openshift-logging delete pvc --all"
        fi
    fi
done

OBC_COUNT=$(oc get obc -A --no-headers 2>/dev/null | wc -l | tr -d ' ')
if [ "$OBC_COUNT" -gt 0 ]; then
    echo "  ObjectBucketClaims found: $OBC_COUNT"
    oc get obc -A --no-headers 2>/dev/null | sed 's/^/    /'
    HAS_CONSUMERS=true
fi

if [ "$HAS_CONSUMERS" = "true" ] && [ "$FORCE_MODE" = "false" ]; then
    echo ""
    echo "WARNING: PVCs or OBCs are still using ODF storage classes."
    echo "  Delete them first, or re-run with --force to skip this check."
    echo "  --force will orphan these PVCs (data will be lost when Ceph is removed)."
    exit 1
elif [ "$HAS_CONSUMERS" = "true" ]; then
    echo ""
    echo "WARNING: Proceeding with --force despite existing consumers. PVCs will be orphaned."
fi

if [ "$HAS_CONSUMERS" = "false" ]; then
    echo "  No PVCs or OBCs using ODF storage classes found."
fi

# --- Phase 2: Delete VolumeSnapshots using ODF ---
echo ""
echo "--- Phase 2: Delete VolumeSnapshots using ODF snapshot classes ---"
for snapclass in ocs-storagecluster-rbdplugin-snapclass ocs-storagecluster-cephfsplugin-snapclass; do
    SNAPS=$(oc get volumesnapshot -A -o jsonpath="{range .items[?(@.spec.volumeSnapshotClassName==\"$snapclass\")]}{.metadata.namespace}/{.metadata.name}{\"\\n\"}{end}" 2>/dev/null || echo "")
    if [ -n "$SNAPS" ]; then
        echo "  Deleting snapshots using '$snapclass':"
        echo "$SNAPS" | while read -r snap; do
            ns="${snap%%/*}"
            name="${snap##*/}"
            echo "    Deleting $ns/$name..."
            oc delete volumesnapshot "$name" -n "$ns" --wait=false 2>/dev/null || true
        done
    fi
done
echo "  Done."

# --- Phase 3: Set uninstall annotations on StorageCluster ---
echo ""
echo "--- Phase 3: Set uninstall annotations ---"
if oc get storagecluster -n "$NAMESPACE" ocs-storagecluster &>/dev/null; then
    UNINSTALL_MODE="graceful"
    if [ "$FORCE_MODE" = "true" ]; then
        UNINSTALL_MODE="forced"
    fi

    oc annotate storagecluster -n "$NAMESPACE" ocs-storagecluster \
        uninstall.ocs.openshift.io/cleanup-policy="delete" \
        uninstall.ocs.openshift.io/mode="$UNINSTALL_MODE" \
        --overwrite
    echo "  Set cleanup-policy=delete, mode=$UNINSTALL_MODE"
else
    echo "  StorageCluster not found, skipping."
fi

# --- Phase 4: Delete StorageCluster ---
echo ""
echo "--- Phase 4: Delete StorageCluster ---"
if oc get storagecluster -n "$NAMESPACE" ocs-storagecluster &>/dev/null; then
    echo "  Deleting StorageCluster..."
    oc delete storagecluster -n "$NAMESPACE" --all --wait=true --timeout=10m || {
        echo "  StorageCluster deletion timed out. Attempting finalizer removal..."
        for sc in $(oc get storagecluster -n "$NAMESPACE" -o name 2>/dev/null); do
            oc patch -n "$NAMESPACE" "$sc" --type=merge -p '{"metadata":{"finalizers":null}}'
        done
        oc delete storagecluster -n "$NAMESPACE" --all --wait=true --timeout=2m || true
    }
    echo "  StorageCluster deleted."
else
    echo "  No StorageCluster found."
fi

# Also delete StorageSystem if present (ODF <= 4.18)
if oc get storagesystem -n "$NAMESPACE" &>/dev/null 2>&1; then
    echo "  Deleting StorageSystem (ODF <= 4.18)..."
    oc delete storagesystem -n "$NAMESPACE" --all --wait=true --timeout=5m || true
fi

# Delete StorageClient if present (ODF 4.19+)
if oc get storageclient -n "$NAMESPACE" &>/dev/null 2>&1; then
    echo "  Deleting StorageClient (ODF 4.19+)..."
    oc delete storageclient -n "$NAMESPACE" --all --wait=true --timeout=5m || true
fi

# --- Phase 5: Wait for cleanup pods ---
echo ""
echo "--- Phase 5: Wait for cleanup pods ---"
for i in $(seq 1 30); do
    CLEANUP_PODS=$(oc get pods -n "$NAMESPACE" 2>/dev/null | grep -i cleanup || echo "")
    if [ -z "$CLEANUP_PODS" ]; then
        echo "  No cleanup pods running."
        break
    fi
    RUNNING=$(echo "$CLEANUP_PODS" | grep -cv "Completed" || echo "0")
    if [ "$RUNNING" -eq 0 ]; then
        echo "  All cleanup pods completed."
        break
    fi
    echo "  Waiting for cleanup pods... ($i/30)"
    sleep 10
done

# --- Phase 6: Remove stuck finalizers ---
echo ""
echo "--- Phase 6: Remove stuck finalizers from known resources ---"

# Resources commonly stuck with finalizers during uninstall
FINALIZER_TARGETS=(
    "cephcluster/ocs-storagecluster-cephcluster"
    "cephblockpool/ocs-storagecluster-cephblockpool"
    "cephfilesystem/ocs-storagecluster-cephfilesystem"
    "cephobjectstore/ocs-storagecluster-cephobjectstore"
    "cephobjectstoreuser/noobaa-ceph-objectstore-user"
    "noobaa/noobaa"
    "configmap/rook-ceph-mon-endpoints"
    "secret/rook-ceph-mon"
    "configmap/ocs-client-operator-config"
)

for target in "${FINALIZER_TARGETS[@]}"; do
    if oc get -n "$NAMESPACE" "$target" &>/dev/null 2>&1; then
        echo "  Removing finalizers from $target..."
        oc patch -n "$NAMESPACE" "$target" --type=merge \
            -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
    fi
done

# Dynamically find and clean all remaining Ceph/NooBaa/OCS custom resources
CEPH_RESOURCE_TYPES="cephcluster cephblockpool cephfilesystem cephobjectstore cephobjectstoreuser cephclient cephnfs cephrbdmirror cephfilesystemmirror cephfilesystemsubvolumegroup cephblockpoolradosnamespace noobaa backingstore bucketclass storageprofile clientprofile storageconsumer"
for rtype in $CEPH_RESOURCE_TYPES; do
    for resource in $(oc get "$rtype" -n "$NAMESPACE" -o name 2>/dev/null); do
        echo "  Removing finalizers from $resource..."
        oc patch -n "$NAMESPACE" "$resource" --type=merge \
            -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
    done
done

# Remove finalizers from csiaddonsnodes (survives namespace deletion)
for resource in $(oc get csiaddonsnodes -n "$NAMESPACE" -o name 2>/dev/null); do
    echo "  Removing finalizers from $resource..."
    oc patch -n "$NAMESPACE" "$resource" --type=merge \
        -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
done

# Remove finalizers from any remaining PVCs in the namespace
for pvc in $(oc get pvc -n "$NAMESPACE" -o name 2>/dev/null); do
    echo "  Removing finalizers from $pvc..."
    oc patch -n "$NAMESPACE" "$pvc" --type=merge \
        -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
done

echo "  Done."

# --- Phase 7: Delete operator subscription, CSVs, and OperatorGroup ---
echo ""
echo "--- Phase 7: Delete operator subscription, CSVs, and OperatorGroup ---"
oc delete subscription -n "$NAMESPACE" odf-operator --ignore-not-found 2>/dev/null || true
for csv in $(oc get csv -n "$NAMESPACE" -o name 2>/dev/null); do
    echo "  Deleting $csv..."
    oc delete -n "$NAMESPACE" "$csv" --ignore-not-found 2>/dev/null || true
done
oc delete operatorgroup -n "$NAMESPACE" --all --ignore-not-found 2>/dev/null || true
# Delete ocs-client-operator-config ConfigMap (required for ODF 4.19.7+, doc step 7)
oc delete cm ocs-client-operator-config -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true
echo "  Done."

# --- Phase 8: Clean up node labels and taints ---
echo ""
echo "--- Phase 8: Clean up node labels and taints ---"
oc label nodes --all cluster.ocs.openshift.io/openshift-storage- 2>/dev/null || true
oc label nodes --all topology.rook.io/rack- 2>/dev/null || true
oc adm taint nodes --all node.ocs.openshift.io/storage- 2>/dev/null || true
echo "  Node labels and taints removed."

# --- Phase 9: Clean up /var/lib/rook on worker nodes ---
echo ""
echo "--- Phase 9: Clean up /var/lib/rook on nodes ---"
WORKERS=$(oc get nodes -l node-role.kubernetes.io/worker -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || echo "")
if [ -n "$WORKERS" ]; then
    for node in $WORKERS; do
        echo "  Cleaning /var/lib/rook on $node..."
        oc debug "node/$node" -- chroot /host rm -rfv /var/lib/rook 2>/dev/null | tail -1 || true
    done
else
    echo "  No worker nodes found."
fi
echo "  Done."

# --- Phase 10: Delete the namespace ---
echo ""
echo "--- Phase 10: Delete namespace ---"
if [ "$KEEP_NAMESPACE" = "true" ]; then
    echo "  Skipping namespace deletion (--keep-namespace)."
elif oc get ns "$NAMESPACE" &>/dev/null; then
    echo "  Deleting namespace '$NAMESPACE'..."
    # Use || true to make namespace deletion failure non-fatal (phases 11-18 must still run)
    oc delete project "$NAMESPACE" --wait=true --timeout=5m || {
        echo "  Namespace deletion timed out. Checking for remaining resources..."
        # Enumerate all remaining resources
        echo "  Remaining resources in $NAMESPACE:"
        oc api-resources --verbs=list --namespaced -o name 2>/dev/null | \
            xargs -n1 -I{} sh -c "oc get {} -n $NAMESPACE --no-headers 2>/dev/null" | \
            head -30 || true
        echo ""
        echo "  Attempting to remove all remaining finalizers..."
        oc api-resources --verbs=list --namespaced -o name 2>/dev/null | \
            xargs -n1 -I{} sh -c "oc get {} -n $NAMESPACE -o name 2>/dev/null" | \
            while read -r resource; do
                oc patch -n "$NAMESPACE" "$resource" --type=merge \
                    -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
            done
        echo "  Waiting for namespace deletion..."
        oc delete project "$NAMESPACE" --wait=true --timeout=2m || {
            echo "  WARNING: Namespace still stuck. Continuing with cleanup..."
            echo "  You may need manual intervention: oc get namespace $NAMESPACE -o yaml"
            true  # Ensure we continue to phases 11-18
        }
    }
    if oc get ns "$NAMESPACE" &>/dev/null; then
        echo "  Namespace still exists (stuck in Terminating). Continuing cleanup..."
    else
        echo "  Namespace deleted."
    fi
else
    echo "  Namespace '$NAMESPACE' already deleted."
fi

# --- Phase 11: Patch StorageClient finalizer (ODF 4.19.0-7) ---
# The storageclient resource may lack a namespace in its YAML, so it can survive
# project deletion and block reinstallation (doc step 15).
echo ""
echo "--- Phase 11: Patch StorageClient finalizer ---"
for sc in $(oc get storageclient -o name 2>/dev/null); do
    echo "  Removing finalizers from $sc..."
    oc patch "$sc" --type=merge -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
    oc delete "$sc" --wait=false 2>/dev/null || true
done
echo "  Done."

# --- Phase 12: Delete StorageClasses ---
echo ""
echo "--- Phase 12: Delete ODF StorageClasses ---"
for sc in $ODF_STORAGE_CLASSES; do
    if oc get sc "$sc" &>/dev/null; then
        echo "  Deleting StorageClass $sc..."
        oc delete sc "$sc" --wait=true --timeout=30s 2>/dev/null || true
    fi
done
echo "  Done."

# --- Phase 13: Delete VolumeSnapshotClasses ---
echo ""
echo "--- Phase 13: Delete ODF VolumeSnapshotClasses ---"
for vsc in ocs-storagecluster-rbdplugin-snapclass ocs-storagecluster-cephfsplugin-snapclass; do
    if oc get volumesnapshotclass "$vsc" &>/dev/null; then
        echo "  Deleting VolumeSnapshotClass $vsc..."
        oc delete volumesnapshotclass "$vsc" --wait=true --timeout=30s 2>/dev/null || true
    fi
done
echo "  Done."

# --- Phase 14: Delete orphaned PVs ---
echo ""
echo "--- Phase 14: Clean up orphaned PVs ---"
ORPHANED_PVS=$(oc get pv -o jsonpath='{range .items[?(@.spec.claimRef.namespace=="openshift-storage")]}{.metadata.name}{"\n"}{end}' 2>/dev/null || echo "")
if [ -n "$ORPHANED_PVS" ]; then
    echo "$ORPHANED_PVS" | while read -r pv; do
        echo "  Deleting orphaned PV $pv..."
        oc delete pv "$pv" --wait=false 2>/dev/null || true
    done
else
    echo "  No orphaned PVs found."
fi

# --- Phase 15: Delete remaining CR instances (required before CRD deletion) ---
echo ""
echo "--- Phase 15: Delete remaining ODF custom resource instances ---"
for rtype in $CEPH_RESOURCE_TYPES storagecluster storagesystem storageclient ocsinitialization storageclassrequest storageconsumer; do
    INSTANCES=$(oc get "$rtype" -A -o name 2>/dev/null || echo "")
    if [ -n "$INSTANCES" ]; then
        echo "$INSTANCES" | while read -r instance; do
            echo "  Deleting $rtype instance: $instance..."
            oc delete "$instance" -A --wait=false 2>/dev/null || true
        done
    fi
done
echo "  Done."

# --- Phase 16: Delete CRDs ---
echo ""
echo "--- Phase 16: Delete ODF CRDs ---"
if [ "$SKIP_CRD_CLEANUP" = "true" ]; then
    echo "  Skipping CRD cleanup (--skip-crd-cleanup)."
else
    ODF_CRDS=(
        # ceph.rook.io
        cephblockpools.ceph.rook.io
        cephblockpoolradosnamespaces.ceph.rook.io
        cephbucketnotifications.ceph.rook.io
        cephbuckettopics.ceph.rook.io
        cephclients.ceph.rook.io
        cephclusters.ceph.rook.io
        cephcosidrivers.ceph.rook.io
        cephfilesystemmirrors.ceph.rook.io
        cephfilesystems.ceph.rook.io
        cephfilesystemsubvolumegroups.ceph.rook.io
        cephnfses.ceph.rook.io
        cephobjectrealms.ceph.rook.io
        cephobjectstores.ceph.rook.io
        cephobjectstoreusers.ceph.rook.io
        cephobjectzonegroups.ceph.rook.io
        cephobjectzones.ceph.rook.io
        cephrbdmirrors.ceph.rook.io
        # ocs.openshift.io
        ocsinitializations.ocs.openshift.io
        storageclassrequests.ocs.openshift.io
        storageclusters.ocs.openshift.io
        storageclients.ocs.openshift.io
        storageconsumers.ocs.openshift.io
        storageprofiles.ocs.openshift.io
        storageautoscalers.ocs.openshift.io
        storageclusterpeers.ocs.openshift.io
        # odf.openshift.io
        storagesystems.odf.openshift.io
        # noobaa.io
        backingstores.noobaa.io
        bucketclasses.noobaa.io
        noobaas.noobaa.io
        namespacestores.noobaa.io
        noobaaaccounts.noobaa.io
        # csiaddons.openshift.io
        csiaddonsnodes.csiaddons.openshift.io
        networkfences.csiaddons.openshift.io
        reclaimspacecronjobs.csiaddons.openshift.io
        reclaimspacejobs.csiaddons.openshift.io
        # replication.storage.openshift.io
        volumereplicationclasses.replication.storage.openshift.io
        volumereplications.replication.storage.openshift.io
        # csi.ceph.io (CephCSI operator CRDs)
        cephconnections.csi.ceph.io
        clientprofilemappings.csi.ceph.io
        clientprofiles.csi.ceph.io
        drivers.csi.ceph.io
        operatorconfigs.csi.ceph.io
        # postgresql.cnpg.noobaa.io (CloudNativePG for NooBaa)
        backups.postgresql.cnpg.noobaa.io
        clusterimagecatalogs.postgresql.cnpg.noobaa.io
        clusters.postgresql.cnpg.noobaa.io
        databases.postgresql.cnpg.noobaa.io
        failoverquorums.postgresql.cnpg.noobaa.io
        imagecatalogs.postgresql.cnpg.noobaa.io
        poolers.postgresql.cnpg.noobaa.io
        publications.postgresql.cnpg.noobaa.io
        scheduledbackups.postgresql.cnpg.noobaa.io
        subscriptions.postgresql.cnpg.noobaa.io
    )

    DELETED=0
    for crd in "${ODF_CRDS[@]}"; do
        if oc get crd "$crd" &>/dev/null; then
            oc delete crd "$crd" --wait=false 2>/dev/null || true
            DELETED=$((DELETED + 1))
        fi
    done
    echo "  Deleted $DELETED CRDs (out of ${#ODF_CRDS[@]} from static list)."

    # Dynamic cleanup: catch any ODF-related CRDs not in the static list
    echo "  Scanning for additional ODF-related CRDs..."
    DYNAMIC_CRDS=$(oc get crd -o name 2>/dev/null | grep -E 'ceph\.rook\.io|ocs\.openshift\.io|odf\.openshift\.io|noobaa\.io|csiaddons\.openshift\.io|replication\.storage\.openshift\.io|csi\.ceph\.io|postgresql\.cnpg\.noobaa\.io' || true)
    if [ -n "$DYNAMIC_CRDS" ]; then
        DYNAMIC_COUNT=0
        echo "$DYNAMIC_CRDS" | while read -r crd; do
            echo "  Deleting additional CRD: $crd..."
            oc delete "$crd" --wait=false 2>/dev/null || true
            DYNAMIC_COUNT=$((DYNAMIC_COUNT + 1))
        done
        echo "  Deleted additional CRDs found by pattern matching."
    fi

    # Wait briefly for CRD deletion
    if [ "$DELETED" -gt 0 ]; then
        echo "  Waiting for CRD deletion to complete..."
        sleep 15
        REMAINING=0
        for crd in "${ODF_CRDS[@]}"; do
            if oc get crd "$crd" &>/dev/null 2>&1; then
                REMAINING=$((REMAINING + 1))
            fi
        done
        if [ "$REMAINING" -gt 0 ]; then
            echo "  WARNING: $REMAINING CRDs still exist. They may have stuck finalizers."
            echo "  Run: oc get crd | grep -E 'ceph|ocs|odf|noobaa|csiaddons|replication.storage|csi.ceph|postgresql.cnpg.noobaa'"
        fi
    fi
fi

# --- Phase 17: Clean up webhook configurations ---
echo ""
echo "--- Phase 17: Clean up webhook configurations ---"
# Use specific patterns to avoid matching unrelated webhooks (e.g., "storage" is too broad)
for wh in $(oc get validatingwebhookconfigurations -o name 2>/dev/null | grep -iE 'ocs\.|odf\.|noobaa|rook-ceph|openshift-storage' || true); do
    echo "  Deleting $wh..."
    oc delete "$wh" 2>/dev/null || true
done
for wh in $(oc get mutatingwebhookconfigurations -o name 2>/dev/null | grep -iE 'ocs\.|odf\.|noobaa|rook-ceph|openshift-storage' || true); do
    echo "  Deleting $wh..."
    oc delete "$wh" 2>/dev/null || true
done
echo "  Done."

# --- Phase 18: Clean up SCCs ---
echo ""
echo "--- Phase 18: Clean up ODF SecurityContextConstraints ---"
for scc in rook-ceph-csi rook-ceph ocs-metrics-exporter noobaa-endpoint noobaa noobaa-db ceph-csi-op-scc; do
    if oc get scc "$scc" &>/dev/null; then
        echo "  Deleting SCC $scc..."
        oc delete scc "$scc" 2>/dev/null || true
    fi
done
echo "  Done."

# --- Summary ---
echo ""
echo "============================="
echo "ODF uninstall complete."
echo ""
echo "Verify:"
echo "  oc get ns $NAMESPACE                    # should be NotFound"
echo "  oc get sc | grep ocs                     # should be empty"
echo "  oc get crd | grep -E 'ceph|ocs|odf|noobaa'  # should be empty"
echo "  oc get nodes --show-labels | grep ocs    # should be empty"
echo "============================="
