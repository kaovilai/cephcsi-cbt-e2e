package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetToolboxPod_FoundByLabel(t *testing.T) {
	ctx := context.Background()
	ns := "rook-ceph"

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rook-ceph-tools-abc",
			Namespace: ns,
			Labels:    map[string]string{"app": toolboxPodNameFragment},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	client := fake.NewClientset(pod)

	got, err := GetToolboxPod(ctx, client, ns)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != pod.Name {
		t.Errorf("expected pod %q, got %q", pod.Name, got.Name)
	}
}

func TestGetToolboxPod_PrefersRunning(t *testing.T) {
	ctx := context.Background()
	ns := "rook-ceph"

	pending := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rook-ceph-tools-pending",
			Namespace: ns,
			Labels:    map[string]string{"app": toolboxPodNameFragment},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	running := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rook-ceph-tools-running",
			Namespace: ns,
			Labels:    map[string]string{"app": toolboxPodNameFragment},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	client := fake.NewClientset(pending, running)

	got, err := GetToolboxPod(ctx, client, ns)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != running.Name {
		t.Errorf("expected running pod %q, got %q", running.Name, got.Name)
	}
}

func TestGetToolboxPod_FoundByName(t *testing.T) {
	ctx := context.Background()
	ns := "rook-ceph"

	// Pod name contains the toolbox fragment; no toolbox label.
	// Note: the fake client does not enforce label selectors, so a pod without
	// the expected label may still be returned by the label-selector path.
	// This test verifies that a pod whose name contains the fragment is found.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      toolboxPodNameFragment + "-xyz",
			Namespace: ns,
		},
	}
	client := fake.NewClientset(pod)

	got, err := GetToolboxPod(ctx, client, ns)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != pod.Name {
		t.Errorf("expected pod %q, got %q", pod.Name, got.Name)
	}
}

func TestGetToolboxPod_NotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	_, err := GetToolboxPod(ctx, client, "rook-ceph")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreateNamespace_NewNamespace(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	if err := CreateNamespace(ctx, client, "test-ns"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ns, err := client.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace not found after creation: %v", err)
	}
	if ns.Labels["pod-security.kubernetes.io/enforce"] != "privileged" {
		t.Error("expected pod-security enforce=privileged label")
	}
}

func TestCreateNamespace_AlreadyExists(t *testing.T) {
	ctx := context.Background()
	existing := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ns"},
	}
	client := fake.NewClientset(existing)

	// Should not return an error when namespace already exists.
	if err := CreateNamespace(ctx, client, "test-ns"); err != nil {
		t.Fatalf("unexpected error for AlreadyExists: %v", err)
	}
}

func TestDeleteNamespace_Exists(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-ns"}}
	client := fake.NewClientset(ns)

	if err := DeleteNamespace(ctx, client, "test-ns"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteNamespace_NotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	// Should not return an error when namespace does not exist.
	if err := DeleteNamespace(ctx, client, "missing-ns"); err != nil {
		t.Fatalf("unexpected error for NotFound: %v", err)
	}
}

func TestDeletePod_Exists(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"}}
	client := fake.NewClientset(pod)

	if err := DeletePod(ctx, client, "test-ns", "my-pod"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeletePV_Exists(t *testing.T) {
	ctx := context.Background()
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "my-pv"}}
	client := fake.NewClientset(pv)

	if err := DeletePV(ctx, client, "my-pv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err := client.CoreV1().PersistentVolumes().Get(ctx, "my-pv", metav1.GetOptions{})
	if err == nil {
		t.Error("expected PV to be deleted, but it still exists")
	}
}

func TestDeletePV_NotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	// Should not return an error when PV does not exist.
	if err := DeletePV(ctx, client, "missing-pv"); err != nil {
		t.Fatalf("unexpected error for NotFound: %v", err)
	}
}

func TestRebindPVWithVolumeMode_Success(t *testing.T) {
	ctx := context.Background()

	blockMode := corev1.PersistentVolumeBlock
	sourcePV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "source-pv"},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: "my-sc",
			VolumeMode:       &blockMode,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "rbd.csi.ceph.com",
					VolumeHandle: "csi-vol-abc123",
					VolumeAttributes: map[string]string{
						"imageName": "csi-vol-abc123",
					},
				},
			},
		},
	}
	client := fake.NewClientset(sourcePV)

	fsMode := corev1.PersistentVolumeFilesystem
	err := RebindPVWithVolumeMode(ctx, client, "source-pv", "new-pv", "new-pvc", "test-ns",
		fsMode, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newPV, err := client.CoreV1().PersistentVolumes().Get(ctx, "new-pv", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("new PV not found: %v", err)
	}
	if newPV.Spec.VolumeMode == nil || *newPV.Spec.VolumeMode != fsMode {
		t.Errorf("expected Filesystem volume mode, got %v", newPV.Spec.VolumeMode)
	}
	if newPV.Spec.CSI.VolumeHandle != "csi-vol-abc123" {
		t.Errorf("expected VolumeHandle %q, got %q", "csi-vol-abc123", newPV.Spec.CSI.VolumeHandle)
	}

	newPVC, err := client.CoreV1().PersistentVolumeClaims("test-ns").Get(ctx, "new-pvc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("new PVC not found: %v", err)
	}
	if newPVC.Spec.VolumeName != "new-pv" {
		t.Errorf("expected PVC bound to %q, got %q", "new-pv", newPVC.Spec.VolumeName)
	}
}

func TestRebindPVWithVolumeMode_SourcePVNotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	fsMode := corev1.PersistentVolumeFilesystem
	err := RebindPVWithVolumeMode(ctx, client, "missing-pv", "new-pv", "new-pvc", "test-ns",
		fsMode, nil)
	if err == nil {
		t.Fatal("expected error for missing source PV, got nil")
	}
}

func TestRebindPVWithVolumeMode_NotCSI(t *testing.T) {
	ctx := context.Background()

	sourcePV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "hostpath-pv"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: "/tmp"},
			},
		},
	}
	client := fake.NewClientset(sourcePV)

	fsMode := corev1.PersistentVolumeFilesystem
	err := RebindPVWithVolumeMode(ctx, client, "hostpath-pv", "new-pv", "new-pvc", "test-ns",
		fsMode, nil)
	if err == nil {
		t.Fatal("expected error for non-CSI source PV, got nil")
	}
}
