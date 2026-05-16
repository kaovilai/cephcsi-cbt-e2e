package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
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

func TestDeletePod_NotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	// Should not return an error when pod does not exist.
	if err := DeletePod(ctx, client, "test-ns", "missing-pod"); err != nil {
		t.Fatalf("unexpected error for NotFound: %v", err)
	}
}

func TestCreatePVC_Defaults(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	pvc, err := CreatePVC(ctx, client, PVCOptions{
		Name:         "test-pvc",
		Namespace:    "test-ns",
		StorageClass: "test-sc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pvc.Name != "test-pvc" {
		t.Errorf("expected name %q, got %q", "test-pvc", pvc.Name)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "test-sc" {
		t.Errorf("expected storage class %q", "test-sc")
	}
	// Default VolumeMode is Block
	if pvc.Spec.VolumeMode == nil || *pvc.Spec.VolumeMode != corev1.PersistentVolumeBlock {
		t.Errorf("expected default VolumeMode=Block")
	}
	// Default AccessMode is ReadWriteOnce
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("expected default AccessMode=ReadWriteOnce, got %v", pvc.Spec.AccessModes)
	}
	// Default Size is 1Gi
	storage := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if storage.String() != "1Gi" {
		t.Errorf("expected default Size=1Gi, got %s", storage.String())
	}
}

func TestCreatePVC_WithSnapshotSource(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	pvc, err := CreatePVC(ctx, client, PVCOptions{
		Name:           "restored-pvc",
		Namespace:      "test-ns",
		StorageClass:   "test-sc",
		Size:           "5Gi",
		SnapshotSource: "my-snapshot",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pvc.Spec.DataSource == nil {
		t.Fatal("expected DataSource to be set from SnapshotSource")
	}
	if pvc.Spec.DataSource.Kind != "VolumeSnapshot" {
		t.Errorf("expected Kind=VolumeSnapshot, got %q", pvc.Spec.DataSource.Kind)
	}
	if pvc.Spec.DataSource.Name != "my-snapshot" {
		t.Errorf("expected DataSource.Name=my-snapshot, got %q", pvc.Spec.DataSource.Name)
	}
}

func TestCreatePVC_WithPVCCloneSource(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	pvc, err := CreatePVC(ctx, client, PVCOptions{
		Name:           "cloned-pvc",
		Namespace:      "test-ns",
		StorageClass:   "test-sc",
		PVCCloneSource: "source-pvc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pvc.Spec.DataSource == nil {
		t.Fatal("expected DataSource to be set from PVCCloneSource")
	}
	if pvc.Spec.DataSource.Kind != "PersistentVolumeClaim" {
		t.Errorf("expected Kind=PersistentVolumeClaim, got %q", pvc.Spec.DataSource.Kind)
	}
	if pvc.Spec.DataSource.Name != "source-pvc" {
		t.Errorf("expected DataSource.Name=source-pvc, got %q", pvc.Spec.DataSource.Name)
	}
}

func TestDeletePVC_Exists(t *testing.T) {
	ctx := context.Background()
	existing := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pvc", Namespace: "test-ns"},
	}
	client := fake.NewClientset(existing)

	if err := DeletePVC(ctx, client, "test-ns", "my-pvc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify deletion
	pvcs, _ := client.CoreV1().PersistentVolumeClaims("test-ns").List(ctx, metav1.ListOptions{})
	if len(pvcs.Items) != 0 {
		t.Errorf("expected PVC to be deleted, still found %d items", len(pvcs.Items))
	}
}

func TestDeletePVC_NotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	// Should not return an error when PVC does not exist.
	if err := DeletePVC(ctx, client, "test-ns", "missing-pvc"); err != nil {
		t.Fatalf("unexpected error for NotFound: %v", err)
	}
}

func TestCreatePodWithPVC_BlockMode(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	pod, err := CreatePodWithPVC(ctx, client, PodOptions{
		Name:       "test-pod",
		Namespace:  "test-ns",
		PVCName:    "test-pvc",
		VolumeMode: corev1.PersistentVolumeBlock,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pod.Name != "test-pod" {
		t.Errorf("expected name %q, got %q", "test-pod", pod.Name)
	}
	container := pod.Spec.Containers[0]
	if len(container.VolumeDevices) != 1 {
		t.Fatalf("expected 1 VolumeDevice, got %d", len(container.VolumeDevices))
	}
	if container.VolumeDevices[0].DevicePath != DefaultBlockDevicePath {
		t.Errorf("expected device path %q, got %q", DefaultBlockDevicePath, container.VolumeDevices[0].DevicePath)
	}
	if len(container.VolumeMounts) != 0 {
		t.Errorf("expected no VolumeMounts for Block mode, got %d", len(container.VolumeMounts))
	}
}

func TestCreatePodWithPVC_FilesystemMode(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	pod, err := CreatePodWithPVC(ctx, client, PodOptions{
		Name:       "test-pod",
		Namespace:  "test-ns",
		PVCName:    "test-pvc",
		VolumeMode: corev1.PersistentVolumeFilesystem,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	container := pod.Spec.Containers[0]
	if len(container.VolumeMounts) != 1 {
		t.Fatalf("expected 1 VolumeMount, got %d", len(container.VolumeMounts))
	}
	if container.VolumeMounts[0].MountPath != DefaultFilesystemMountPath {
		t.Errorf("expected mount path %q, got %q", DefaultFilesystemMountPath, container.VolumeMounts[0].MountPath)
	}
	if len(container.VolumeDevices) != 0 {
		t.Errorf("expected no VolumeDevices for Filesystem mode, got %d", len(container.VolumeDevices))
	}
}

func TestDeletePV_Exists(t *testing.T) {
	ctx := context.Background()
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pv"},
	}
	client := fake.NewClientset(pv)

	if err := DeletePV(ctx, client, "my-pv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
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
