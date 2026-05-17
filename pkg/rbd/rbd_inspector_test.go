package rbd

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// newTestInspector returns an Inspector with a fake toolbox executor for unit tests.
func newTestInspector(execFn func(ctx context.Context, command []string) (string, error)) *Inspector {
	return &Inspector{
		pool:         "replicapool",
		execOverride: execFn,
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
	for i := 0; i <= depth; i++ {
		if i == 0 {
			names[i] = "csi-vol-root"
		} else {
			names[i] = fmt.Sprintf("csi-vol-depth%d", i)
		}
	}
	entries := make(map[string]string, depth+1)
	for i := 0; i <= depth; i++ {
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
