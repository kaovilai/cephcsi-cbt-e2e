#!/bin/bash
# Shared helpers for ocp-setup scripts

NAMESPACE="${NAMESPACE:-openshift-storage}"
CSI_DRIVER_NAME="${CSI_DRIVER_NAME:-openshift-storage.rbd.csi.ceph.com}"

# Detect the correct pod label selector for the RBD ctrlplugin across ODF versions:
#   ODF < 4.18:  app=csi-rbdplugin-provisioner
#   ODF 4.18+:   app.kubernetes.io/name=csi-rbdplugin,app.kubernetes.io/component=ctrlplugin
#   ODF 4.21+:   app=openshift-storage.rbd.csi.ceph.com-ctrlplugin
detect_ctrlplugin_label() {
    for label in \
        "app=${CSI_DRIVER_NAME}-ctrlplugin" \
        "app.kubernetes.io/name=csi-rbdplugin,app.kubernetes.io/component=ctrlplugin" \
        "app=csi-rbdplugin-provisioner"; do
        if oc get pods -n "$NAMESPACE" -l "$label" --no-headers 2>/dev/null | grep -q .; then
            echo "$label"
            return
        fi
    done
    echo "app=${CSI_DRIVER_NAME}-ctrlplugin"
}

# ODF performance profile requirements (cluster-wide totals across workers)
# See: ODF docs 7.3.6 "Resource requirements for performance profiles"
#   lean:        24 CPU, 72 GiB
#   balanced:    30 CPU, 72 GiB
#   performance: 45 CPU, 96 GiB
#
# detect_odf_profile TOTAL_CPU TOTAL_MEM_GI
#   Picks the best profile the cluster can support.
#   Can be overridden by setting ODF_PROFILE env var.
detect_odf_profile() {
    local total_cpu="$1"
    local total_mem="$2"

    # Allow explicit override
    if [ -n "${ODF_PROFILE:-}" ]; then
        echo "$ODF_PROFILE"
        return
    fi

    if [ "$total_cpu" -ge 45 ] && [ "$total_mem" -ge 96 ]; then
        echo "performance"
    elif [ "$total_cpu" -ge 30 ] && [ "$total_mem" -ge 72 ]; then
        echo "balanced"
    elif [ "$total_cpu" -ge 24 ] && [ "$total_mem" -ge 72 ]; then
        echo "lean"
    else
        echo "lean"
    fi
}

# Get the CPU and memory requirements for a given profile
# Returns: "CPU MEM" (e.g., "24 72")
odf_profile_requirements() {
    local profile="$1"
    case "$profile" in
        performance) echo "45 96" ;;
        balanced)    echo "30 72" ;;
        *)           echo "24 72" ;;
    esac
}
