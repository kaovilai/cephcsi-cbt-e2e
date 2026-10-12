#!/bin/bash
# Shared helpers for ocp-setup scripts
set -euo pipefail

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

# Detect the installed ODF version as "MAJOR.MINOR" (e.g. "4.21", "5.0") from the
# odf-operator / ocs-operator CSV name in $NAMESPACE. Prints nothing if not found.
# Note: ODF 5.0 follows 4.22 (there is no ODF 4.23), so always compare MAJOR first.
detect_odf_version() {
    oc get csv -n "$NAMESPACE" -o jsonpath='{.items[*].metadata.name}' 2>/dev/null | \
        tr ' ' '\n' | grep -E '^(odf-operator|ocs-operator)\.' | head -1 | \
        grep -oE '[0-9]+\.[0-9]+' | head -1 || true
}

# version_ge VERSION MAJOR MINOR
#   Returns 0 if VERSION ("MAJOR.MINOR", e.g. "5.0") is >= MAJOR.MINOR, comparing the
#   major component before the minor one. Returns 1 if VERSION is empty or unparsable.
version_ge() {
    local version="$1" want_major="$2" want_minor="$3"
    [[ "$version" =~ ^([0-9]+)\.([0-9]+) ]] || return 1
    local major="${BASH_REMATCH[1]}" minor="${BASH_REMATCH[2]}"
    [ "$major" -gt "$want_major" ] || { [ "$major" -eq "$want_major" ] && [ "$minor" -ge "$want_minor" ]; }
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
