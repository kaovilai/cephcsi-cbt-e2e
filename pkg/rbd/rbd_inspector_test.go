package rbd

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// newTestInspector returns an Inspector with a fake toolbox executor for unit tests.
func newTestInspector(execFn func(ctx context.Context, command []string) (string, error)) *Inspector {
	return &Inspector{
		pool:         "replicapool",
		execOverride: execFn,
	}
}

// newTestInspectorWithClient returns an Inspector with a fake Kubernetes clientset.
func newTestInspectorWithClient(clientset *fake.Clientset) *Inspector {
	return &Inspector{
		pool:      "replicapool",
		clientset: clientset,
	}
}

func TestIsImageFlattened(t *testing.T) {
	tests := []struct {
		name     string
		infoJSON string
		execErr  error
		want     bool
		wantErr  bool
	}{
		{
			name:     "no parent — flattened",
			infoJSON: `{"name":"csi-vol-abc","size":1073741824,"features":[]}`,
			want:     true,
		},
		{
			name:     "has parent — not flattened",
			infoJSON: `{"name":"csi-vol-abc","size":1073741824,"features":[],"parent":{"pool":"replicapool","image":"csi-vol-parent","snapshot":"csi-snap-123"}}`,
			want:     false,
		},
		{
			name:    "exec error",
			execErr: fmt.Errorf("toolbox unavailable"),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestInspector(func(_ context.Context, _ []string) (string, error) {
				return tc.infoJSON, tc.execErr
			})
			got, err := r.IsImageFlattened(context.Background(), "csi-vol-abc")
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got flattened=%v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("IsImageFlattened() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetImageParent(t *testing.T) {
	tests := []struct {
		name     string
		infoJSON string
		execErr  error
		want     string
		wantErr  bool
	}{
		{
			name:     "no parent",
			infoJSON: `{"name":"csi-vol-abc","size":1073741824,"features":[]}`,
			want:     "",
		},
		{
			name:     "has parent",
			infoJSON: `{"name":"csi-vol-abc","size":1073741824,"features":[],"parent":{"pool":"replicapool","image":"csi-vol-parent","snapshot":"csi-snap-123"}}`,
			want:     "replicapool/csi-vol-parent@csi-snap-123",
		},
		{
			name:    "exec error",
			execErr: fmt.Errorf("toolbox unavailable"),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestInspector(func(_ context.Context, _ []string) (string, error) {
				return tc.infoJSON, tc.execErr
			})
			got, err := r.GetImageParent(context.Background(), "csi-vol-abc")
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("GetImageParent() = %q, want %q", got, tc.want)
			}
		})
	}
}

// rbdInfoChainExec builds an execOverride that simulates a clone chain of the
// given depth. Each call to rbd info returns a parent pointing to the previous
// image in the chain; the root image has no parent.
//
// Chain layout (depth=3):
//
//	csi-vol-root  (no parent)
//	  └─ csi-vol-depth1  parent=csi-vol-root@snap
//	       └─ csi-vol-depth2  parent=csi-vol-depth1@snap
//	            └─ csi-vol-depth3  parent=csi-vol-depth2@snap
func rbdInfoChainExec(depth int) func(context.Context, []string) (string, error) {
	imageInfo := func(name, parentImage string) string {
		if parentImage == "" {
			return `{"name":"` + name + `","size":1073741824,"features":[]}`
		}
		return `{"name":"` + name + `","size":1073741824,"features":[],"parent":{"pool":"replicapool","image":"` + parentImage + `","snapshot":"snap"}}`
	}

	names := make([]string, depth+1)
	for i := range depth + 1 {
		if i == 0 {
			names[i] = "csi-vol-root"
		} else {
			names[i] = fmt.Sprintf("csi-vol-depth%d", i)
		}
	}
	entries := make(map[string]string, depth+1)
	for i := range depth + 1 {
		parent := ""
		if i > 0 {
			parent = names[i-1]
		}
		entries[names[i]] = imageInfo(names[i], parent)
	}

	return func(_ context.Context, command []string) (string, error) {
		if len(command) < 3 {
			return "", fmt.Errorf("unexpected command: %v", command)
		}
		// command is ["rbd", "info", "pool/image", "--format", "json"]
		poolImage := command[2]
		parts := strings.SplitN(poolImage, "/", 2)
		imageName := parts[len(parts)-1]
		info, ok := entries[imageName]
		if !ok {
			return "", fmt.Errorf("image not found: %s", imageName)
		}
		return info, nil
	}
}

func TestGetCloneDepth(t *testing.T) {
	tests := []struct {
		name      string
		imageName string
		execFn    func(context.Context, []string) (string, error)
		want      int
		wantErr   bool
	}{
		{
			name:      "root image — depth 0",
			imageName: "csi-vol-root",
			execFn:    rbdInfoChainExec(0),
			want:      0,
		},
		{
			name:      "single clone — depth 1",
			imageName: "csi-vol-depth1",
			execFn:    rbdInfoChainExec(1),
			want:      1,
		},
		{
			name:      "chain depth 4",
			imageName: "csi-vol-depth4",
			execFn:    rbdInfoChainExec(4),
			want:      4,
		},
		{
			name:      "chain depth 8 (hardMaxCloneDepth default)",
			imageName: "csi-vol-depth8",
			execFn:    rbdInfoChainExec(8),
			want:      8,
		},
		{
			name:      "exec error on first call",
			imageName: "csi-vol-abc",
			execFn: func(_ context.Context, _ []string) (string, error) {
				return "", fmt.Errorf("toolbox unavailable")
			},
			wantErr: true,
		},
		{
			name:      "max depth exceeded — cycle guard",
			imageName: "csi-vol-cyclic",
			execFn: func(_ context.Context, _ []string) (string, error) {
				// Always return a self-referential parent to simulate a corrupt chain.
				return `{"name":"csi-vol-cyclic","size":1073741824,"features":[],"parent":{"pool":"replicapool","image":"csi-vol-cyclic","snapshot":"snap"}}`, nil
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestInspector(tc.execFn)
			got, err := r.GetCloneDepth(context.Background(), tc.imageName)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got depth=%d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("GetCloneDepth(%q) = %d, want %d", tc.imageName, got, tc.want)
			}
		})
	}
}

func TestGetSnapshotCount(t *testing.T) {
	tests := []struct {
		name    string
		execOut string
		execErr error
		want    int
		wantErr bool
	}{
		{
			name:    "no snapshots",
			execOut: "[]",
			want:    0,
		},
		{
			name:    "two snapshots",
			execOut: `[{"id":1,"name":"snap-1","size":1073741824,"protected":"false"},{"id":2,"name":"snap-2","size":1073741824,"protected":"false"}]`,
			want:    2,
		},
		{
			name:    "exec error",
			execErr: fmt.Errorf("toolbox unavailable"),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestInspector(func(_ context.Context, _ []string) (string, error) {
				return tc.execOut, tc.execErr
			})
			got, err := r.GetSnapshotCount(context.Background(), "csi-vol-abc")
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got count=%d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("GetSnapshotCount() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestListImages(t *testing.T) {
	tests := []struct {
		name    string
		execOut string
		execErr error
		want    []string
		wantErr bool
	}{
		{
			name:    "empty pool",
			execOut: "",
			want:    nil,
		},
		{
			name:    "two images",
			execOut: "csi-vol-abc\ncsi-vol-xyz\n",
			want:    []string{"csi-vol-abc", "csi-vol-xyz"},
		},
		{
			name:    "exec error",
			execErr: fmt.Errorf("toolbox unavailable"),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestInspector(func(_ context.Context, _ []string) (string, error) {
				return tc.execOut, tc.execErr
			})
			got, err := r.ListImages(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ListImages() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListSnapshots(t *testing.T) {
	tests := []struct {
		name    string
		execOut string
		execErr error
		want    []RBDSnapshot
		wantErr bool
	}{
		{
			name:    "empty — no snapshots",
			execOut: "[]",
			want:    nil,
		},
		{
			name:    "two snapshots",
			execOut: `[{"id":1,"name":"snap-1","size":1073741824,"protected":"false"},{"id":2,"name":"snap-2","size":1073741824,"protected":"true"}]`,
			want: []RBDSnapshot{
				{ID: 1, Name: "snap-1", Size: 1073741824, Protected: "false"},
				{ID: 2, Name: "snap-2", Size: 1073741824, Protected: "true"},
			},
		},
		{
			// rbdErrNotFound in the error string → treated as empty, no error.
			name:    "not-found error treated as empty",
			execErr: fmt.Errorf("toolbox exec failed (stderr: rbd: No such file or directory): exit status 22"),
			want:    nil,
		},
		{
			name:    "non-not-found exec error propagated",
			execErr: fmt.Errorf("toolbox unavailable"),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestInspector(func(_ context.Context, _ []string) (string, error) {
				return tc.execOut, tc.execErr
			})
			got, err := r.ListSnapshots(context.Background(), "csi-vol-abc")
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ListSnapshots() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetChildren(t *testing.T) {
	tests := []struct {
		name    string
		execOut string
		execErr error
		want    []string
		wantErr bool
	}{
		{
			name:    "no children — empty output",
			execOut: "",
			want:    nil,
		},
		{
			name:    "two children",
			execOut: "replicapool/csi-vol-child1\nreplicapool/csi-vol-child2\n",
			want:    []string{"csi-vol-child1", "csi-vol-child2"},
		},
		{
			// rbdErrNotFound in the error string → treated as no children.
			name:    "not-found error treated as no children",
			execErr: fmt.Errorf("toolbox exec failed (stderr: rbd: No such file or directory): exit status 2"),
			want:    nil,
		},
		{
			name:    "other exec error propagated",
			execErr: fmt.Errorf("toolbox unavailable"),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestInspector(func(_ context.Context, _ []string) (string, error) {
				return tc.execOut, tc.execErr
			})
			got, err := r.GetChildren(context.Background(), "csi-vol-parent", "csi-snap-123")
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("GetChildren() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetCephMajorVersion(t *testing.T) {
	tests := []struct {
		name    string
		execOut string
		execErr error
		want    int
		wantErr bool
	}{
		{
			name:    "quincy 17",
			execOut: "ceph version 17.2.6 (d7ff0d10654d2280e08f1ab989c7cefa3064e5a1) quincy (stable)",
			want:    17,
		},
		{
			name:    "reef 18",
			execOut: "ceph version 18.2.0 (abc123) reef (stable)",
			want:    18,
		},
		{
			name:    "exec error propagated",
			execErr: fmt.Errorf("toolbox unavailable"),
			wantErr: true,
		},
		{
			name:    "malformed version string",
			execOut: "not a version",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestInspector(func(_ context.Context, _ []string) (string, error) {
				return tc.execOut, tc.execErr
			})
			got, err := r.GetCephMajorVersion(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("GetCephMajorVersion() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestGetOmapData(t *testing.T) {
	tests := []struct {
		name      string
		imageName string
		key       string
		execOut   string
		execErr   error
		want      string
		wantErr   bool
	}{
		{
			name:      "key found",
			imageName: "csi-vol-abc",
			key:       "csi.volname",
			execOut:   "pvc-123",
			want:      "pvc-123",
		},
		{
			name:      "empty value",
			imageName: "csi-vol-abc",
			key:       "csi.volname",
			execOut:   "",
			want:      "",
		},
		{
			name:      "exec error",
			imageName: "csi-vol-abc",
			key:       "csi.volname",
			execErr:   fmt.Errorf("toolbox unavailable"),
			wantErr:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestInspector(func(_ context.Context, _ []string) (string, error) {
				return tc.execOut, tc.execErr
			})
			got, err := r.GetOmapData(context.Background(), tc.imageName, tc.key)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("GetOmapData() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListOmapKeys(t *testing.T) {
	tests := []struct {
		name      string
		imageName string
		execOut   string
		execErr   error
		want      []string
		wantErr   bool
	}{
		{
			name:      "no keys",
			imageName: "csi-vol-abc",
			execOut:   "",
			want:      nil,
		},
		{
			name:      "single key",
			imageName: "csi-vol-abc",
			execOut:   "csi.volname",
			want:      []string{"csi.volname"},
		},
		{
			name:      "multiple keys",
			imageName: "csi-vol-abc",
			execOut:   "csi.volname\ncsi.volid\ncsi.snapshotID\n",
			want:      []string{"csi.volname", "csi.volid", "csi.snapshotID"},
		},
		{
			name:      "blank lines skipped",
			imageName: "csi-vol-abc",
			execOut:   "csi.volname\n\ncsi.volid\n",
			want:      []string{"csi.volname", "csi.volid"},
		},
		{
			name:      "exec error",
			imageName: "csi-vol-abc",
			execErr:   fmt.Errorf("toolbox unavailable"),
			wantErr:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestInspector(func(_ context.Context, _ []string) (string, error) {
				return tc.execOut, tc.execErr
			})
			got, err := r.ListOmapKeys(context.Background(), tc.imageName)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ListOmapKeys() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFlattenImage(t *testing.T) {
	tests := []struct {
		name      string
		imageName string
		execErr   error
		wantErr   bool
	}{
		{
			name:      "flatten succeeds",
			imageName: "csi-vol-abc",
		},
		{
			name:      "flatten exec error",
			imageName: "csi-vol-abc",
			execErr:   fmt.Errorf("rbd flatten failed: exit status 1"),
			wantErr:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestInspector(func(_ context.Context, _ []string) (string, error) {
				return "", tc.execErr
			})
			err := r.FlattenImage(context.Background(), tc.imageName)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func makePV(name string, csiDriver string, volumeHandle string, attrs map[string]string) *corev1.PersistentVolume {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	if csiDriver != "" {
		pv.Spec.CSI = &corev1.CSIPersistentVolumeSource{
			Driver:           csiDriver,
			VolumeHandle:     volumeHandle,
			VolumeAttributes: attrs,
		}
	}
	return pv
}

func makeBoundPVC(name, namespace, pvName string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: pvName},
	}
}

func TestGetRBDImageNameFromPV(t *testing.T) {
	tests := []struct {
		name    string
		pv      *corev1.PersistentVolume
		pvName  string
		want    string
		wantErr bool
	}{
		{
			name:   "success: PV with imageName attribute",
			pvName: "pv-abc",
			pv:     makePV("pv-abc", "rbd.csi.ceph.com", "handle-1", map[string]string{"imageName": "csi-vol-abc"}),
			want:   "csi-vol-abc",
		},
		{
			name:    "error: PV not found",
			pvName:  "pv-missing",
			pv:      makePV("pv-other", "rbd.csi.ceph.com", "handle-1", map[string]string{"imageName": "x"}),
			wantErr: true,
		},
		{
			name:    "error: PV is not a CSI volume",
			pvName:  "pv-noncsi",
			pv:      makePV("pv-noncsi", "", "", nil),
			wantErr: true,
		},
		{
			name:    "error: PV has no imageName attribute",
			pvName:  "pv-noattr",
			pv:      makePV("pv-noattr", "rbd.csi.ceph.com", "handle-1", map[string]string{"other": "value"}),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewClientset(tc.pv)
			r := newTestInspectorWithClient(client)
			got, err := r.GetRBDImageNameFromPV(context.Background(), tc.pvName)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("GetRBDImageNameFromPV() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetRBDImageNameFromPVC(t *testing.T) {
	const ns = "test-ns"
	pv := makePV("pv-abc", "rbd.csi.ceph.com", "handle-1", map[string]string{"imageName": "csi-vol-abc"})

	tests := []struct {
		name    string
		pvc     *corev1.PersistentVolumeClaim
		pvcName string
		want    string
		wantErr bool
	}{
		{
			name:    "success: PVC bound to PV with imageName",
			pvcName: "pvc-abc",
			pvc:     makeBoundPVC("pvc-abc", ns, "pv-abc"),
			want:    "csi-vol-abc",
		},
		{
			name:    "error: PVC not found",
			pvcName: "pvc-missing",
			pvc:     makeBoundPVC("pvc-other", ns, "pv-abc"),
			wantErr: true,
		},
		{
			name:    "error: PVC not yet bound",
			pvcName: "pvc-unbound",
			pvc:     makeBoundPVC("pvc-unbound", ns, ""),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewClientset(pv, tc.pvc)
			r := newTestInspectorWithClient(client)
			got, err := r.GetRBDImageNameFromPVC(context.Background(), ns, tc.pvcName)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("GetRBDImageNameFromPVC() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGetRBDImageNameFromPVC_PVErrors verifies that PV-level errors are propagated
// correctly through GetRBDImageNameFromPVC. The PVC-level tests above only cover PVC
// lookup failures; these tests cover cases where the PVC is found and bound but the
// referenced PV has its own problems.
func TestGetRBDImageNameFromPVC_PVErrors(t *testing.T) {
	const ns = "test-ns"

	tests := []struct {
		name    string
		pvs     []*corev1.PersistentVolume
		pvc     *corev1.PersistentVolumeClaim
		pvcName string
		wantErr bool
	}{
		{
			name:    "error: bound PV not found in cluster",
			pvs:     nil,
			pvc:     makeBoundPVC("pvc-dangling", ns, "pv-missing"),
			pvcName: "pvc-dangling",
			wantErr: true,
		},
		{
			name:    "error: bound PV is not a CSI volume",
			pvs:     []*corev1.PersistentVolume{makePV("pv-noncsi", "", "", nil)},
			pvc:     makeBoundPVC("pvc-noncsi", ns, "pv-noncsi"),
			pvcName: "pvc-noncsi",
			wantErr: true,
		},
		{
			name:    "error: bound PV has no imageName attribute",
			pvs:     []*corev1.PersistentVolume{makePV("pv-noattr", "rbd.csi.ceph.com", "handle-1", map[string]string{"other": "value"})},
			pvc:     makeBoundPVC("pvc-noattr", ns, "pv-noattr"),
			pvcName: "pvc-noattr",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := make([]runtime.Object, 0, len(tc.pvs)+1)
			for _, pv := range tc.pvs {
				objs = append(objs, pv)
			}
			objs = append(objs, tc.pvc)
			client := fake.NewClientset(objs...)
			r := newTestInspectorWithClient(client)
			_, err := r.GetRBDImageNameFromPVC(context.Background(), ns, tc.pvcName)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
