package rbd

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestParseCephMajorVersion(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{
			name:  "quincy 17",
			input: "ceph version 17.2.6 (810db68029296a72ef3cff2443e59c810b66b0a2) quincy (stable)",
			want:  17,
		},
		{
			name:  "reef 18",
			input: "ceph version 18.2.4 (e7ad5345525c7aa95470c26863873b581076945d) reef (stable)",
			want:  18,
		},
		{
			name:  "squid 19",
			input: "ceph version 19.0.0 (deadbeef) squid (dev)",
			want:  19,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "too short",
			input:   "ceph version",
			wantErr: true,
		},
		{
			name:    "non-numeric major",
			input:   "ceph version x.2.6 (abc) quincy (stable)",
			wantErr: true,
		},
		{
			name:  "tentacle 20",
			input: "ceph version 20.0.0 (abc123) tentacle (dev)",
			want:  20,
		},
		{
			name:  "no patch version",
			input: "ceph version 17 (abc) quincy (stable)",
			want:  17,
		},
		{
			name:    "only two words",
			input:   "ceph 17",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCephMajorVersion(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if got != tc.want {
				t.Errorf("expected %d, got %d", tc.want, got)
			}
		})
	}
}

func newTestInspector(client *fake.Clientset) *Inspector {
	return NewInspector(client, nil, "rook-ceph", "replicapool")
}

func csiPV(name, imageName string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "rbd.csi.ceph.com",
					VolumeHandle: "handle-" + name,
					VolumeAttributes: map[string]string{
						"imageName": imageName,
					},
				},
			},
		},
	}
}

func TestGetRBDImageNameFromPV_Found(t *testing.T) {
	pv := csiPV("my-pv", "csi-vol-abc123")
	client := fake.NewClientset(pv)
	insp := newTestInspector(client)

	got, err := insp.GetRBDImageNameFromPV(context.Background(), "my-pv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "csi-vol-abc123" {
		t.Errorf("expected %q, got %q", "csi-vol-abc123", got)
	}
}

func TestGetRBDImageNameFromPV_PVNotFound(t *testing.T) {
	client := fake.NewClientset()
	insp := newTestInspector(client)

	_, err := insp.GetRBDImageNameFromPV(context.Background(), "missing-pv")
	if err == nil {
		t.Fatal("expected error for missing PV, got nil")
	}
}

func TestGetRBDImageNameFromPV_NotCSI(t *testing.T) {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "hostpath-pv"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: "/tmp"},
			},
		},
	}
	client := fake.NewClientset(pv)
	insp := newTestInspector(client)

	_, err := insp.GetRBDImageNameFromPV(context.Background(), "hostpath-pv")
	if err == nil {
		t.Fatal("expected error for non-CSI PV, got nil")
	}
}

func TestGetRBDImageNameFromPV_NoImageNameAttr(t *testing.T) {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "no-attr-pv"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:           "rbd.csi.ceph.com",
					VolumeHandle:     "some-handle",
					VolumeAttributes: map[string]string{"pool": "replicapool"},
				},
			},
		},
	}
	client := fake.NewClientset(pv)
	insp := newTestInspector(client)

	_, err := insp.GetRBDImageNameFromPV(context.Background(), "no-attr-pv")
	if err == nil {
		t.Fatal("expected error when imageName attribute is missing, got nil")
	}
}
