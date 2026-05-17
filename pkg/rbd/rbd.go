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
	clientset        kubernetes.Interface
	config           *rest.Config
	namespace        string
	pool             string
	toolboxPodName   string
	toolboxContainer string
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
// The toolbox pod name and container are cached after the first successful lookup
// to avoid repeated pod-list API calls in operations that call execInToolbox in a loop
// (e.g. GetCloneDepth traversing the clone chain).
func (r *Inspector) execInToolbox(ctx context.Context, command []string) (string, error) {
	if r.toolboxPodName == "" {
		pod, err := k8s.GetToolboxPod(ctx, r.clientset, r.namespace)
		if err != nil {
			return "", fmt.Errorf("get toolbox pod for %v: %w", command, err)
		}
		r.toolboxPodName = pod.Name
		if len(pod.Spec.Containers) > 0 {
			r.toolboxContainer = pod.Spec.Containers[0].Name
		}
	}

	stdout, stderr, err := k8s.ExecInPod(ctx, r.clientset, r.config, r.namespace, r.toolboxPodName, r.toolboxContainer, command)
	if err != nil {
		return "", fmt.Errorf("toolbox exec failed (stderr: %s): %w", stderr, err)
	}

	return strings.TrimSpace(stdout), nil
}

// rbdImageInfo holds the JSON output of `rbd info --format json`.
type rbdImageInfo struct {
	Name     string   `json:"name"`
	Size     int64    `json:"size"`
	Features []string `json:"features"`
	Parent   *struct {
		Pool     string `json:"pool"`
		Image    string `json:"image"`
		Snapshot string `json:"snapshot"`
	} `json:"parent,omitempty"`
}

// rbd and rados commands return this string when the requested image or snapshot
// does not exist. Callers that want to treat "not found" as empty use this constant
// instead of duplicating the literal string across multiple call sites.
const rbdErrNotFound = "No such file"

// csiImageNameAttr is the key in a CSI PV's VolumeAttributes that holds the RBD image name.
// CephCSI sets this when provisioning a volume.
const csiImageNameAttr = "imageName"

// radosCsiVolumePrefix is the RADOS object name prefix used by CephCSI for per-volume omap metadata.
const radosCsiVolumePrefix = "csi.volume."

// poolImage returns the pool/image path string for use in rbd commands.
func (r *Inspector) poolImage(imageName string) string {
	return fmt.Sprintf("%s/%s", r.pool, imageName)
}

// getRBDInfo fetches and parses the rbd info JSON for an image.
func (r *Inspector) getRBDInfo(ctx context.Context, imageName string) (*rbdImageInfo, error) {
	output, err := r.execInToolbox(ctx, []string{
		"rbd", "info", r.poolImage(imageName), "--format", "json",
	})
	if err != nil {
		return nil, fmt.Errorf("rbd info failed for %s: %w", imageName, err)
	}

	var info rbdImageInfo
	if err := json.Unmarshal([]byte(output), &info); err != nil {
		return nil, fmt.Errorf("parse rbd info output for %s: %w", imageName, err)
	}
	return &info, nil
}

// IsImageFlattened checks whether an RBD image has been flattened (has no parent).
func (r *Inspector) IsImageFlattened(ctx context.Context, imageName string) (bool, error) {
	info, err := r.getRBDInfo(ctx, imageName)
	if err != nil {
		return false, fmt.Errorf("check if image %s is flattened: %w", imageName, err)
	}

	// An image is flattened if it has no parent
	return info.Parent == nil, nil
}

// GetImageParent returns the parent image/snapshot of an RBD image, or empty if flattened.
func (r *Inspector) GetImageParent(ctx context.Context, imageName string) (string, error) {
	info, err := r.getRBDInfo(ctx, imageName)
	if err != nil {
		return "", fmt.Errorf("get parent of image %s: %w", imageName, err)
	}

	if info.Parent == nil {
		return "", nil
	}

	return fmt.Sprintf("%s/%s@%s", info.Parent.Pool, info.Parent.Image, info.Parent.Snapshot), nil
}

// imageNameFromParentRef extracts the RBD image name from a parent reference string
// in the form "pool/image@snapshot". Returns an empty string if the ref is malformed.
func imageNameFromParentRef(parentRef string) string {
	parts := strings.Split(parentRef, "/")
	if len(parts) < 2 {
		return ""
	}
	imageSnap := strings.Split(parts[len(parts)-1], "@")
	return imageSnap[0]
}

// maxCloneDepth is a safety limit for GetCloneDepth to prevent infinite loops
// in case of a pathological or corrupt clone chain. CephCSI's hardMaxCloneDepth
// default is 8, so 100 is well beyond any expected chain length.
const maxCloneDepth = 100

// GetCloneDepth returns the depth of the clone chain for an image.
func (r *Inspector) GetCloneDepth(ctx context.Context, imageName string) (int, error) {
	depth := 0
	current := imageName

	for {
		if depth > maxCloneDepth {
			return 0, fmt.Errorf("clone chain depth exceeds maximum %d starting from %s (possible cycle or corrupt chain)", maxCloneDepth, imageName)
		}
		parent, err := r.GetImageParent(ctx, current)
		if err != nil {
			return 0, fmt.Errorf("GetCloneDepth at depth %d image %s: %w", depth, current, err)
		}
		if parent == "" {
			break
		}
		depth++
		next := imageNameFromParentRef(parent)
		if next == "" {
			break
		}
		current = next
	}

	return depth, nil
}

// GetSnapshotCount returns the number of snapshots for an RBD image.
func (r *Inspector) GetSnapshotCount(ctx context.Context, imageName string) (int, error) {
	snaps, err := r.ListSnapshots(ctx, imageName)
	if err != nil {
		return 0, fmt.Errorf("GetSnapshotCount %s: %w", imageName, err)
	}
	return len(snaps), nil
}

// FlattenImage flattens an RBD image, removing its parent reference.
// This is destructive: the clone chain is permanently broken.
func (r *Inspector) FlattenImage(ctx context.Context, imageName string) error {
	_, err := r.execInToolbox(ctx, []string{
		"rbd", "flatten", r.poolImage(imageName),
	})
	if err != nil {
		return fmt.Errorf("rbd flatten failed for %s: %w", imageName, err)
	}
	return nil
}

// RBDSnapshot holds snapshot info from rbd snap ls.
type RBDSnapshot struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Protected string `json:"protected"`
}

// parseSnapshotsJSON parses the JSON output of "rbd snap ls --format json"
// into a slice of RBDSnapshot. Returns nil for empty or empty-array output.
func parseSnapshotsJSON(output string) ([]RBDSnapshot, error) {
	if output == "" || output == "[]" {
		return nil, nil
	}
	var snapshots []RBDSnapshot
	if err := json.Unmarshal([]byte(output), &snapshots); err != nil {
		return nil, fmt.Errorf("parse rbd snap ls: %w", err)
	}
	return snapshots, nil
}

// ListSnapshots returns the snapshots for an RBD image.
func (r *Inspector) ListSnapshots(ctx context.Context, imageName string) ([]RBDSnapshot, error) {
	output, err := r.execInToolbox(ctx, []string{
		"rbd", "snap", "ls", r.poolImage(imageName), "--format", "json",
	})
	if err != nil {
		if strings.Contains(err.Error(), rbdErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("rbd snap ls failed: %w", err)
	}
	return parseSnapshotsJSON(output)
}

// splitLines splits newline-delimited command output into trimmed, non-empty lines.
// This is the canonical helper for all line-based parsing in this package.
func splitLines(output string) []string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// parseChildrenOutput parses the newline-delimited output of "rbd children".
// Each line has the form "pool/image"; only the image name is returned.
// Lines without a "/" are returned as-is.
func parseChildrenOutput(output string) []string {
	var children []string
	for _, line := range splitLines(output) {
		parts := strings.SplitN(line, "/", 2)
		if len(parts) == 2 {
			children = append(children, parts[1])
		} else {
			children = append(children, line)
		}
	}
	return children
}

// GetChildren returns child image names cloned from a specific snapshot.
// Uses rbd children which outputs "pool/image" per line.
func (r *Inspector) GetChildren(ctx context.Context, imageName, snapName string) ([]string, error) {
	output, err := r.execInToolbox(ctx, []string{
		"rbd", "children", fmt.Sprintf("%s/%s@%s", r.pool, imageName, snapName),
	})
	if err != nil {
		// No children is not an error
		if strings.Contains(err.Error(), rbdErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("rbd children failed: %w", err)
	}

	if output == "" {
		return nil, nil
	}

	return parseChildrenOutput(output), nil
}

// parseImagesOutput parses the newline-delimited output of "rbd ls".
func parseImagesOutput(output string) []string {
	return splitLines(output)
}

// ListImages returns all RBD image names in the pool.
func (r *Inspector) ListImages(ctx context.Context) ([]string, error) {
	output, err := r.execInToolbox(ctx, []string{
		"rbd", "ls", r.pool,
	})
	if err != nil {
		return nil, fmt.Errorf("rbd ls failed: %w", err)
	}

	if output == "" {
		return nil, nil
	}

	return parseImagesOutput(output), nil
}

// GetOmapData retrieves omap key-value data for an RBD image.
func (r *Inspector) GetOmapData(ctx context.Context, imageName, key string) (string, error) {
	// Use rados getomapval to read a specific key
	output, err := r.execInToolbox(ctx, []string{
		"rados", "-p", r.pool, "getomapval", fmt.Sprintf("%s%s", radosCsiVolumePrefix, imageName), key,
	})
	if err != nil {
		return "", fmt.Errorf("rados getomapval failed for %s/%s: %w", imageName, key, err)
	}

	return output, nil
}

// ListOmapKeys lists all omap keys for an RBD image's CSI metadata.
func (r *Inspector) ListOmapKeys(ctx context.Context, imageName string) ([]string, error) {
	output, err := r.execInToolbox(ctx, []string{
		"rados", "-p", r.pool, "listomapkeys", fmt.Sprintf("%s%s", radosCsiVolumePrefix, imageName),
	})
	if err != nil {
		return nil, fmt.Errorf("rados listomapkeys failed: %w", err)
	}

	lines := splitLines(output)
	if len(lines) == 0 {
		return nil, nil
	}

	return lines, nil
}

// GetCephVersion returns the raw "ceph version X.Y.Z ..." string from the toolbox pod.
func (r *Inspector) GetCephVersion(ctx context.Context) (string, error) {
	return r.execInToolbox(ctx, []string{"ceph", "version"})
}

// GetCephMajorVersion parses and returns the major version number.
func (r *Inspector) GetCephMajorVersion(ctx context.Context) (int, error) {
	version, err := r.GetCephVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("get ceph version string: %w", err)
	}
	major, err := parseCephMajorVersion(version)
	if err != nil {
		return 0, fmt.Errorf("parse ceph major version from %q: %w", version, err)
	}
	return major, nil
}

// parseCephMajorVersion parses the major version from a "ceph version X.Y.Z ..." string.
func parseCephMajorVersion(version string) (int, error) {
	// Parse "ceph version 17.2.6 (xxx) quincy (stable)"
	parts := strings.Fields(version)
	if len(parts) < 3 {
		return 0, fmt.Errorf("unexpected ceph version format: %s", version)
	}

	versionParts := strings.Split(parts[2], ".")
	major, err := strconv.Atoi(versionParts[0])
	if err != nil {
		return 0, fmt.Errorf("parse major version: %w", err)
	}

	return major, nil
}

// GetRBDImageNameFromPV extracts the RBD image name from a PV's CSI volume attributes.
func (r *Inspector) GetRBDImageNameFromPV(ctx context.Context, pvName string) (string, error) {
	pv, err := r.clientset.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get PV %s: %w", pvName, err)
	}

	if pv.Spec.CSI == nil {
		return "", fmt.Errorf("PV %s is not a CSI volume", pvName)
	}

	// The image name is stored in the volumeAttributes
	if imageName, ok := pv.Spec.CSI.VolumeAttributes[csiImageNameAttr]; ok {
		return imageName, nil
	}

	return "", fmt.Errorf("PV %s has no %s attribute", pvName, csiImageNameAttr)
}
