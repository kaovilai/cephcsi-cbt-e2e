// Package rbd provides RBD introspection helpers that execute commands
// in the Ceph toolbox pod to inspect RBD image and snapshot state.
package rbd

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cephcsi-cbt-e2e/pkg/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Inspector provides RBD introspection via the Ceph toolbox pod.
type Inspector struct {
	clientset kubernetes.Interface
	config    *rest.Config
	namespace string
	pool      string
}

// NewInspector creates a new RBD inspector.
func NewInspector(clientset kubernetes.Interface, config *rest.Config, namespace, pool string) *Inspector {
	return &Inspector{
		clientset: clientset,
		config:    config,
		namespace: namespace,
		pool:      pool,
	}
}

// execInToolbox executes a command in the Ceph toolbox pod.
func (r *Inspector) execInToolbox(ctx context.Context, command []string) (string, error) {
	pod, err := k8s.GetToolboxPod(ctx, r.clientset, r.namespace)
	if err != nil {
		return "", err
	}

	// Use the first container in the toolbox pod (container name varies by ODF version)
	containerName := ""
	if len(pod.Spec.Containers) > 0 {
		containerName = pod.Spec.Containers[0].Name
	}
	stdout, stderr, err := k8s.ExecInPod(ctx, r.clientset, r.config, r.namespace, pod.Name, containerName, command)
	if err != nil {
		return "", fmt.Errorf("toolbox exec failed (stderr: %s): %w", stderr, err)
	}

	return strings.TrimSpace(stdout), nil
}

// rbdImageInfo holds the JSON output of `rbd info --format json`.
type rbdImageInfo struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Features []string `json:"features"`
	Parent   *struct {
		Pool     string `json:"pool"`
		Image    string `json:"image"`
		Snapshot string `json:"snapshot"`
	} `json:"parent,omitempty"`
}

// IsImageFlattened checks whether an RBD image has been flattened (has no parent).
func (r *Inspector) IsImageFlattened(ctx context.Context, imageName string) (bool, error) {
	output, err := r.execInToolbox(ctx, []string{
		"rbd", "info", fmt.Sprintf("%s/%s", r.pool, imageName), "--format", "json",
	})
	if err != nil {
		return false, fmt.Errorf("rbd info failed: %w", err)
	}

	var info rbdImageInfo
	if err := json.Unmarshal([]byte(output), &info); err != nil {
		return false, fmt.Errorf("failed to parse rbd info output: %w", err)
	}

	// An image is flattened if it has no parent
	return info.Parent == nil, nil
}

// GetImageParent returns the parent image/snapshot of an RBD image, or empty if flattened.
func (r *Inspector) GetImageParent(ctx context.Context, imageName string) (string, error) {
	output, err := r.execInToolbox(ctx, []string{
		"rbd", "info", fmt.Sprintf("%s/%s", r.pool, imageName), "--format", "json",
	})
	if err != nil {
		return "", fmt.Errorf("rbd info failed: %w", err)
	}

	var info rbdImageInfo
	if err := json.Unmarshal([]byte(output), &info); err != nil {
		return "", fmt.Errorf("failed to parse rbd info output: %w", err)
	}

	if info.Parent == nil {
		return "", nil
	}

	return fmt.Sprintf("%s/%s@%s", info.Parent.Pool, info.Parent.Image, info.Parent.Snapshot), nil
}

// GetCloneDepth returns the depth of the clone chain for an image.
func (r *Inspector) GetCloneDepth(ctx context.Context, imageName string) (int, error) {
	depth := 0
	current := imageName

	for {
		parent, err := r.GetImageParent(ctx, current)
		if err != nil {
			return 0, err
		}
		if parent == "" {
			break
		}
		depth++
		// Extract image name from parent string (pool/image@snapshot)
		parts := strings.Split(parent, "/")
		if len(parts) < 2 {
			break
		}
		imageSnap := strings.Split(parts[len(parts)-1], "@")
		current = imageSnap[0]
	}

	return depth, nil
}

// GetSnapshotCount returns the number of snapshots for an RBD image.
func (r *Inspector) GetSnapshotCount(ctx context.Context, imageName string) (int, error) {
	output, err := r.execInToolbox(ctx, []string{
		"rbd", "snap", "ls", fmt.Sprintf("%s/%s", r.pool, imageName), "--format", "json",
	})
	if err != nil {
		// If no snapshots exist, rbd snap ls may return an error or empty output
		if strings.Contains(err.Error(), "No such file") {
			return 0, nil
		}
		return 0, fmt.Errorf("rbd snap ls failed: %w", err)
	}

	if output == "" || output == "[]" {
		return 0, nil
	}

	var snapshots []json.RawMessage
	if err := json.Unmarshal([]byte(output), &snapshots); err != nil {
		return 0, fmt.Errorf("failed to parse rbd snap ls output: %w", err)
	}

	return len(snapshots), nil
}

// GetOmapData retrieves omap key-value data for an RBD image.
func (r *Inspector) GetOmapData(ctx context.Context, imageName, key string) (string, error) {
	// Use rados getomapval to read a specific key
	output, err := r.execInToolbox(ctx, []string{
		"rados", "-p", r.pool, "getomapval", fmt.Sprintf("csi.volume.%s", imageName), key,
	})
	if err != nil {
		return "", fmt.Errorf("rados getomapval failed for %s/%s: %w", imageName, key, err)
	}

	return output, nil
}

// ListOmapKeys lists all omap keys for an RBD image's CSI metadata.
func (r *Inspector) ListOmapKeys(ctx context.Context, imageName string) ([]string, error) {
	output, err := r.execInToolbox(ctx, []string{
		"rados", "-p", r.pool, "listomapkeys", fmt.Sprintf("csi.volume.%s", imageName),
	})
	if err != nil {
		return nil, fmt.Errorf("rados listomapkeys failed: %w", err)
	}

	if output == "" {
		return nil, nil
	}

	return strings.Split(output, "\n"), nil
}

// GetCephVersion returns the Ceph version string from the cluster.
func (r *Inspector) GetCephVersion(ctx context.Context) (string, error) {
	return r.execInToolbox(ctx, []string{"ceph", "version"})
}

// GetCephMajorVersion parses and returns the major version number.
func (r *Inspector) GetCephMajorVersion(ctx context.Context) (int, error) {
	version, err := r.GetCephVersion(ctx)
	if err != nil {
		return 0, err
	}

	// Parse "ceph version 17.2.6 (xxx) quincy (stable)"
	parts := strings.Fields(version)
	if len(parts) < 3 {
		return 0, fmt.Errorf("unexpected ceph version format: %s", version)
	}

	versionParts := strings.Split(parts[2], ".")
	if len(versionParts) < 1 {
		return 0, fmt.Errorf("unexpected version number format: %s", parts[2])
	}

	major, err := strconv.Atoi(versionParts[0])
	if err != nil {
		return 0, fmt.Errorf("failed to parse major version: %w", err)
	}

	return major, nil
}

// GetRBDImageNameFromPV extracts the RBD image name from a PV's CSI volume handle.
// The CSI volume handle for Ceph RBD typically has the format:
// <clusterID>-<pool>-<unique-id>
func (r *Inspector) GetRBDImageNameFromPV(ctx context.Context, clientset kubernetes.Interface, pvName string) (string, error) {
	pv, err := clientset.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get PV %s: %w", pvName, err)
	}

	if pv.Spec.CSI == nil {
		return "", fmt.Errorf("PV %s is not a CSI volume", pvName)
	}

	// The image name is stored in the volumeAttributes
	if imageName, ok := pv.Spec.CSI.VolumeAttributes["imageName"]; ok {
		return imageName, nil
	}

	return "", fmt.Errorf("PV %s has no imageName attribute", pvName)
}
