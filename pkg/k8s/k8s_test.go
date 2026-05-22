package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	snapfake "github.com/kubernetes-csi/external-snapshotter/client/v8/clientset/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clientgotesting "k8s.io/client-go/testing"
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

// TestGetToolboxPod_NonRunningPodByLabel verifies that a pod found by label
// selector but not in Running phase is still returned (non-running fallback).
func TestGetToolboxPod_NonRunningPodByLabel(t *testing.T) {
	ctx := context.Background()
	ns := "rook-ceph"

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rook-ceph-tools-pending",
			Namespace: ns,
			Labels:    map[string]string{"app": toolboxPodNameFragment},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
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

// TestGetToolboxPod_LabelListError_FallsBackToName verifies that when label-selector
// list calls fail, GetToolboxPod falls back to name-based matching and succeeds.
func TestGetToolboxPod_LabelListError_FallsBackToName(t *testing.T) {
	ctx := context.Background()
	ns := "rook-ceph"
	gr := schema.GroupResource{Group: "", Resource: "pods"}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      toolboxPodNameFragment + "-xyz",
			Namespace: ns,
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	client := fake.NewClientset(pod)

	// Fail the first two list calls (label-selector paths); let the third
	// (all-pods fallback) proceed normally so the pod is found by name.
	callCount := 0
	client.Fake.PrependReactor("list", "pods", func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		callCount++
		if callCount <= 2 {
			return true, nil, k8serrors.NewServerTimeout(gr, "list", 0)
		}
		return false, nil, nil // fall through to default
	})

	got, err := GetToolboxPod(ctx, client, ns)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != pod.Name {
		t.Errorf("expected pod %q, got %q", pod.Name, got.Name)
	}
}

// TestGetToolboxPod_AllPodsListError verifies that when the all-pods fallback
// list call fails (with no prior label errors), GetToolboxPod returns an error.
func TestGetToolboxPod_AllPodsListError(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "", Resource: "pods"}
	client := fake.NewClientset()

	// Let the first two list calls (label selectors) succeed with empty results,
	// then fail the third all-pods call.
	callCount := 0
	client.Fake.PrependReactor("list", "pods", func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		callCount++
		if callCount == 3 {
			return true, nil, k8serrors.NewServerTimeout(gr, "list", 0)
		}
		return false, nil, nil
	})

	_, err := GetToolboxPod(ctx, client, "rook-ceph")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestGetToolboxPod_AllListsFail verifies that when all list calls fail,
// the error message mentions the prior label-selector errors.
func TestGetToolboxPod_AllListsFail(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "", Resource: "pods"}
	client := fake.NewClientset()

	client.Fake.PrependReactor("list", "pods", func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewServerTimeout(gr, "list", 0)
	})

	_, err := GetToolboxPod(ctx, client, "rook-ceph")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "prior label errors") {
		t.Errorf("expected error to mention prior label errors, got: %v", err)
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

// TestCreateNamespace_AlreadyExists_PatchesLabels verifies that when a namespace
// already exists without the PodSecurity labels (e.g. leftover from a prior run),
// CreateNamespace patches them onto the namespace so privileged pods can run.
func TestCreateNamespace_AlreadyExists_PatchesLabels(t *testing.T) {
	ctx := context.Background()
	existing := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-ns",
			Labels: map[string]string{"some-other-label": "value"},
		},
	}
	client := fake.NewClientset(existing)

	if err := CreateNamespace(ctx, client, "test-ns"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ns, err := client.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace not found: %v", err)
	}
	for _, key := range []string{
		"pod-security.kubernetes.io/enforce",
		"pod-security.kubernetes.io/audit",
		"pod-security.kubernetes.io/warn",
	} {
		if ns.Labels[key] != "privileged" {
			t.Errorf("expected label %s=privileged after patch, got %q", key, ns.Labels[key])
		}
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

// TestDeletePod_Error verifies that a non-NotFound delete error is propagated.
func TestDeletePod_Error(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	gr := schema.GroupResource{Group: "", Resource: "pods"}
	client.Fake.PrependReactor("delete", "pods", func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewServerTimeout(gr, "delete", 0)
	})

	if err := DeletePod(ctx, client, "test-ns", "my-pod"); err == nil {
		t.Fatal("expected error from Delete failure, got nil")
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

func TestCreatePVC_WithDataSource(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	apiGroup := "snapshot.storage.k8s.io"
	pvc, err := CreatePVC(ctx, client, PVCOptions{
		Name:         "ds-pvc",
		Namespace:    "test-ns",
		StorageClass: "test-sc",
		DataSource: &corev1.TypedLocalObjectReference{
			APIGroup: &apiGroup,
			Kind:     "VolumeSnapshot",
			Name:     "direct-snap",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pvc.Spec.DataSource == nil {
		t.Fatal("expected DataSource to be set")
	}
	if pvc.Spec.DataSource.Name != "direct-snap" {
		t.Errorf("expected DataSource.Name=direct-snap, got %q", pvc.Spec.DataSource.Name)
	}
}

func TestCreatePVC_WithDataSourceRef(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	apiGroup := "snapshot.storage.k8s.io"
	pvc, err := CreatePVC(ctx, client, PVCOptions{
		Name:         "restored-pvc",
		Namespace:    "test-ns",
		StorageClass: "test-sc",
		Size:         "5Gi",
		DataSourceRef: &corev1.TypedObjectReference{
			APIGroup: &apiGroup,
			Kind:     "VolumeSnapshot",
			Name:     "my-snapshot",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pvc.Spec.DataSourceRef == nil {
		t.Fatal("expected DataSourceRef to be set")
	}
	if pvc.Spec.DataSourceRef.Kind != "VolumeSnapshot" {
		t.Errorf("expected Kind=VolumeSnapshot, got %q", pvc.Spec.DataSourceRef.Kind)
	}
	if pvc.Spec.DataSourceRef.Name != "my-snapshot" {
		t.Errorf("expected DataSourceRef.Name=my-snapshot, got %q", pvc.Spec.DataSourceRef.Name)
	}
}

// TestCreatePVC_CreateError verifies that a Create failure is returned as a wrapped error.
func TestCreatePVC_CreateError(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	gr := schema.GroupResource{Group: "", Resource: "persistentvolumeclaims"}
	client.Fake.PrependReactor("create", "persistentvolumeclaims", func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewServerTimeout(gr, "create", 0)
	})

	_, err := CreatePVC(ctx, client, PVCOptions{Name: "fail-pvc", Namespace: "test-ns"})
	if err == nil {
		t.Fatal("expected error from Create failure, got nil")
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

// TestDeletePVC_Error verifies that a non-NotFound delete error is propagated.
func TestDeletePVC_Error(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	gr := schema.GroupResource{Group: "", Resource: "persistentvolumeclaims"}
	client.Fake.PrependReactor("delete", "persistentvolumeclaims", func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewServerTimeout(gr, "delete", 0)
	})

	if err := DeletePVC(ctx, client, "test-ns", "my-pvc"); err == nil {
		t.Fatal("expected error from Delete failure, got nil")
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

// TestCreatePodWithPVC_CreateError verifies that a Create failure is returned as an error.
func TestCreatePodWithPVC_CreateError(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	gr := schema.GroupResource{Group: "", Resource: "pods"}
	client.Fake.PrependReactor("create", "pods", func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewServerTimeout(gr, "create", 0)
	})

	_, err := CreatePodWithPVC(ctx, client, PodOptions{
		Name:       "fail-pod",
		Namespace:  "test-ns",
		PVCName:    "my-pvc",
		VolumeMode: corev1.PersistentVolumeBlock,
	})
	if err == nil {
		t.Fatal("expected error from Create failure, got nil")
	}
}

// TestResizePVC_Error verifies that a Patch failure is returned as a wrapped error.
func TestResizePVC_Error(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	gr := schema.GroupResource{Group: "", Resource: "persistentvolumeclaims"}
	client.Fake.PrependReactor("patch", "persistentvolumeclaims", func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewServerTimeout(gr, "patch", 0)
	})

	err := ResizePVC(ctx, client, "test-ns", "missing-pvc", "10Gi")
	if err == nil {
		t.Fatal("expected error from Patch failure, got nil")
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

// TestWaitForPVDeleted_AlreadyGone verifies that WaitForPVDeleted succeeds immediately
// when the PV does not exist.
func TestWaitForPVDeleted_AlreadyGone(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	if err := WaitForPVDeleted(ctx, client, "gone-pv", 5*time.Second); err != nil {
		t.Fatalf("unexpected error when PV already gone: %v", err)
	}
}

// TestWaitForPVDeleted_Timeout verifies that WaitForPVDeleted returns an error when
// the PV still exists after the timeout.
func TestWaitForPVDeleted_Timeout(t *testing.T) {
	ctx := context.Background()
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "stuck-pv"},
	}
	client := fake.NewClientset(pv)

	err := WaitForPVDeleted(ctx, client, "stuck-pv", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "stuck-pv") {
		t.Errorf("error should mention PV name, got: %v", err)
	}
}

// TestWaitForPVDeleted_RetryableError verifies that a transient server-timeout error
// during WaitForPVDeleted is retried rather than immediately failing.
func TestWaitForPVDeleted_RetryableError(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "", Resource: "persistentvolumes"}
	client := fake.NewClientset() // PV does not exist after the first (failing) call
	prependOnceReactor(client, "get", "persistentvolumes", k8serrors.NewServerTimeout(gr, "get", 0))

	if err := WaitForPVDeleted(ctx, client, "retry-pv", 10*time.Second); err != nil {
		t.Fatalf("expected success after transient error, got: %v", err)
	}
}

func TestCreateROXPVCFromSnapshot(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	pvc, err := CreateROXPVCFromSnapshot(ctx, client, "rox-pvc", "test-ns", "rbd-sc", "my-snap", "5Gi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pvc.Name != "rox-pvc" {
		t.Errorf("expected name %q, got %q", "rox-pvc", pvc.Name)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadOnlyMany {
		t.Errorf("expected AccessMode=ReadOnlyMany, got %v", pvc.Spec.AccessModes)
	}
	if pvc.Spec.DataSource == nil || pvc.Spec.DataSource.Kind != "VolumeSnapshot" {
		t.Error("expected DataSource.Kind=VolumeSnapshot")
	}
	if pvc.Spec.DataSource.Name != "my-snap" {
		t.Errorf("expected DataSource.Name=%q, got %q", "my-snap", pvc.Spec.DataSource.Name)
	}
}

func TestCreateSnapshot_Success(t *testing.T) {
	ctx := context.Background()
	client := snapfake.NewSimpleClientset()

	vs, err := CreateSnapshot(ctx, client, "my-snap", "test-ns", "my-pvc", "csi-snapclass")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vs.Name != "my-snap" {
		t.Errorf("expected name %q, got %q", "my-snap", vs.Name)
	}
	if vs.Spec.VolumeSnapshotClassName == nil || *vs.Spec.VolumeSnapshotClassName != "csi-snapclass" {
		t.Errorf("expected VolumeSnapshotClassName=%q", "csi-snapclass")
	}
	if vs.Spec.Source.PersistentVolumeClaimName == nil || *vs.Spec.Source.PersistentVolumeClaimName != "my-pvc" {
		t.Errorf("expected PersistentVolumeClaimName=%q", "my-pvc")
	}
}

func TestDeleteSnapshot_Exists(t *testing.T) {
	ctx := context.Background()
	existing := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "my-snap", Namespace: "test-ns"},
	}
	client := snapfake.NewSimpleClientset(existing)

	if err := DeleteSnapshot(ctx, client, "test-ns", "my-snap"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	snaps, _ := client.SnapshotV1().VolumeSnapshots("test-ns").List(ctx, metav1.ListOptions{})
	if len(snaps.Items) != 0 {
		t.Errorf("expected snapshot to be deleted, still found %d items", len(snaps.Items))
	}
}

func TestDeleteSnapshot_NotFound(t *testing.T) {
	ctx := context.Background()
	client := snapfake.NewSimpleClientset()

	// Should not return an error when snapshot does not exist.
	if err := DeleteSnapshot(ctx, client, "test-ns", "missing-snap"); err != nil {
		t.Fatalf("unexpected error for NotFound: %v", err)
	}
}

// TestCreateSnapshot_Error verifies that a Create failure is returned as a wrapped error.
func TestCreateSnapshot_Error(t *testing.T) {
	ctx := context.Background()
	client := snapfake.NewSimpleClientset()
	gr := schema.GroupResource{Group: "snapshot.storage.k8s.io", Resource: "volumesnapshots"}
	client.Fake.PrependReactor("create", "volumesnapshots", func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewServerTimeout(gr, "create", 0)
	})

	_, err := CreateSnapshot(ctx, client, "fail-snap", "test-ns", "my-pvc", "csi-snapclass")
	if err == nil {
		t.Fatal("expected error from Create failure, got nil")
	}
}

func TestResizePVC_Success(t *testing.T) {
	ctx := context.Background()
	existing := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pvc", Namespace: "test-ns"},
	}
	client := fake.NewClientset(existing)

	if err := ResizePVC(ctx, client, "test-ns", "my-pvc", "10Gi"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pvc, err := client.CoreV1().PersistentVolumeClaims("test-ns").Get(ctx, "my-pvc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get PVC after resize: %v", err)
	}
	storage := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if storage.String() != "10Gi" {
		t.Errorf("expected storage=10Gi after resize, got %s", storage.String())
	}
}

func ptr[T any](v T) *T { return &v }

func mustParseQuantity(s string) resource.Quantity {
	return resource.MustParse(s)
}

func TestGetSnapshotContentName_Success(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "my-snap", Namespace: "test-ns"},
		Status: &snapshotv1.VolumeSnapshotStatus{
			BoundVolumeSnapshotContentName: ptr("my-content"),
		},
	}
	client := snapfake.NewSimpleClientset(vs)

	got, err := GetSnapshotContentName(ctx, client, "test-ns", "my-snap")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "my-content" {
		t.Errorf("got %q, want %q", got, "my-content")
	}
}

func TestGetSnapshotContentName_NotFound(t *testing.T) {
	ctx := context.Background()
	client := snapfake.NewSimpleClientset()

	_, err := GetSnapshotContentName(ctx, client, "test-ns", "missing-snap")
	if err == nil {
		t.Fatal("expected error for missing snapshot, got nil")
	}
}

func TestGetSnapshotContentName_NilStatus(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "my-snap", Namespace: "test-ns"},
		// Status is nil — snapshot not yet bound
	}
	client := snapfake.NewSimpleClientset(vs)

	_, err := GetSnapshotContentName(ctx, client, "test-ns", "my-snap")
	if err == nil {
		t.Fatal("expected error for unbound snapshot, got nil")
	}
}

func TestGetSnapshotContentName_NilContentName(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "my-snap", Namespace: "test-ns"},
		Status: &snapshotv1.VolumeSnapshotStatus{
			BoundVolumeSnapshotContentName: nil,
		},
	}
	client := snapfake.NewSimpleClientset(vs)

	_, err := GetSnapshotContentName(ctx, client, "test-ns", "my-snap")
	if err == nil {
		t.Fatal("expected error when BoundVolumeSnapshotContentName is nil, got nil")
	}
}

func TestGetSnapshotContentName_EmptyContentName(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "my-snap", Namespace: "test-ns"},
		Status: &snapshotv1.VolumeSnapshotStatus{
			BoundVolumeSnapshotContentName: ptr(""),
		},
	}
	client := snapfake.NewSimpleClientset(vs)

	_, err := GetSnapshotContentName(ctx, client, "test-ns", "my-snap")
	if err == nil {
		t.Fatal("expected error when BoundVolumeSnapshotContentName is empty string, got nil")
	}
}

func TestGetSnapshotHandle_Success(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "my-snap", Namespace: "test-ns"},
		Status: &snapshotv1.VolumeSnapshotStatus{
			BoundVolumeSnapshotContentName: ptr("my-content"),
		},
	}
	vsc := &snapshotv1.VolumeSnapshotContent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-content"},
		Status: &snapshotv1.VolumeSnapshotContentStatus{
			SnapshotHandle: ptr("ceph-handle-abc123"),
		},
	}
	client := snapfake.NewSimpleClientset(vs, vsc)

	got, err := GetSnapshotHandle(ctx, client, "test-ns", "my-snap")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ceph-handle-abc123" {
		t.Errorf("got %q, want %q", got, "ceph-handle-abc123")
	}
}

func TestGetSnapshotHandle_SnapshotNotFound(t *testing.T) {
	ctx := context.Background()
	client := snapfake.NewSimpleClientset()

	_, err := GetSnapshotHandle(ctx, client, "test-ns", "missing-snap")
	if err == nil {
		t.Fatal("expected error for missing snapshot, got nil")
	}
}

func TestGetSnapshotHandle_SnapshotNotBound(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "my-snap", Namespace: "test-ns"},
		// Status nil — not bound yet
	}
	client := snapfake.NewSimpleClientset(vs)

	_, err := GetSnapshotHandle(ctx, client, "test-ns", "my-snap")
	if err == nil {
		t.Fatal("expected error for unbound snapshot, got nil")
	}
}

func TestGetSnapshotHandle_ContentNotFound(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "my-snap", Namespace: "test-ns"},
		Status: &snapshotv1.VolumeSnapshotStatus{
			BoundVolumeSnapshotContentName: ptr("missing-content"),
		},
	}
	// VolumeSnapshotContent is not added to the fake client
	client := snapfake.NewSimpleClientset(vs)

	_, err := GetSnapshotHandle(ctx, client, "test-ns", "my-snap")
	if err == nil {
		t.Fatal("expected error for missing VolumeSnapshotContent, got nil")
	}
}

func TestGetSnapshotHandle_NoHandle(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "my-snap", Namespace: "test-ns"},
		Status: &snapshotv1.VolumeSnapshotStatus{
			BoundVolumeSnapshotContentName: ptr("my-content"),
		},
	}
	vsc := &snapshotv1.VolumeSnapshotContent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-content"},
		// Status nil — no handle yet
	}
	client := snapfake.NewSimpleClientset(vs, vsc)

	_, err := GetSnapshotHandle(ctx, client, "test-ns", "my-snap")
	if err == nil {
		t.Fatal("expected error for missing snapshot handle, got nil")
	}
}

func TestGetSnapshotHandle_EmptyHandle(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "my-snap", Namespace: "test-ns"},
		Status: &snapshotv1.VolumeSnapshotStatus{
			BoundVolumeSnapshotContentName: ptr("my-content"),
		},
	}
	vsc := &snapshotv1.VolumeSnapshotContent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-content"},
		Status: &snapshotv1.VolumeSnapshotContentStatus{
			SnapshotHandle: ptr(""),
		},
	}
	client := snapfake.NewSimpleClientset(vs, vsc)

	_, err := GetSnapshotHandle(ctx, client, "test-ns", "my-snap")
	if err == nil {
		t.Fatal("expected error for empty snapshot handle, got nil")
	}
}

func makeCSIPV(name string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: "rbd-sc",
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: mustParseQuantity("1Gi"),
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "rbd.csi.ceph.com",
					VolumeHandle: "0001-0011-rook-ceph-0000000000000001-abc123",
					VolumeAttributes: map[string]string{
						"imageName": "csi-vol-abc123",
					},
				},
			},
		},
	}
}

func TestRebindPVWithVolumeMode_Success(t *testing.T) {
	ctx := context.Background()
	sourcePV := makeCSIPV("source-pv")
	client := fake.NewClientset(sourcePV)

	blockMode := corev1.PersistentVolumeBlock
	err := RebindPVWithVolumeMode(ctx, client, "source-pv", "new-pv", "new-pvc", "test-ns",
		blockMode, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the new PV was created with the correct VolumeMode
	newPV, err := client.CoreV1().PersistentVolumes().Get(ctx, "new-pv", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("new PV not found: %v", err)
	}
	if newPV.Spec.VolumeMode == nil || *newPV.Spec.VolumeMode != corev1.PersistentVolumeBlock {
		t.Errorf("expected VolumeMode Block, got %v", newPV.Spec.VolumeMode)
	}
	if newPV.Spec.CSI.VolumeHandle != sourcePV.Spec.CSI.VolumeHandle {
		t.Errorf("expected VolumeHandle %q, got %q", sourcePV.Spec.CSI.VolumeHandle, newPV.Spec.CSI.VolumeHandle)
	}
	if newPV.Spec.ClaimRef == nil || newPV.Spec.ClaimRef.Name != "new-pvc" {
		t.Errorf("expected ClaimRef to new-pvc, got %v", newPV.Spec.ClaimRef)
	}

	// Verify the new PVC was created
	newPVC, err := client.CoreV1().PersistentVolumeClaims("test-ns").Get(ctx, "new-pvc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("new PVC not found: %v", err)
	}
	if newPVC.Spec.VolumeName != "new-pv" {
		t.Errorf("expected PVC bound to new-pv, got %q", newPVC.Spec.VolumeName)
	}
	if newPVC.Spec.VolumeMode == nil || *newPVC.Spec.VolumeMode != corev1.PersistentVolumeBlock {
		t.Errorf("expected PVC VolumeMode Block, got %v", newPVC.Spec.VolumeMode)
	}
}

func TestRebindPVWithVolumeMode_DefaultsAccessMode(t *testing.T) {
	ctx := context.Background()
	sourcePV := makeCSIPV("source-pv")
	client := fake.NewClientset(sourcePV)

	filesystemMode := corev1.PersistentVolumeFilesystem
	// Pass nil access modes to exercise the default-to-RWO path
	err := RebindPVWithVolumeMode(ctx, client, "source-pv", "new-pv", "new-pvc", "test-ns",
		filesystemMode, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newPV, err := client.CoreV1().PersistentVolumes().Get(ctx, "new-pv", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("new PV not found: %v", err)
	}
	if len(newPV.Spec.AccessModes) != 1 || newPV.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("expected default RWO access mode, got %v", newPV.Spec.AccessModes)
	}
}

func TestRebindPVWithVolumeMode_SourceNotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	blockMode := corev1.PersistentVolumeBlock
	err := RebindPVWithVolumeMode(ctx, client, "missing-pv", "new-pv", "new-pvc", "test-ns",
		blockMode, nil)
	if err == nil {
		t.Fatal("expected error for missing source PV, got nil")
	}
}

func TestResizePVC_NotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	// ResizePVC on a non-existent PVC should return an error.
	if err := ResizePVC(ctx, client, "test-ns", "missing-pvc", "10Gi"); err == nil {
		t.Fatal("expected error for non-existent PVC, got nil")
	}
}

func TestCreatePodWithPVC_ReadOnly(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	pod, err := CreatePodWithPVC(ctx, client, PodOptions{
		Name:       "test-pod",
		Namespace:  "test-ns",
		PVCName:    "test-pvc",
		ReadOnly:   true,
		VolumeMode: corev1.PersistentVolumeFilesystem,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pod.Spec.Containers[0].VolumeMounts) != 1 {
		t.Fatalf("expected 1 VolumeMount, got %d", len(pod.Spec.Containers[0].VolumeMounts))
	}
	if !pod.Spec.Containers[0].VolumeMounts[0].ReadOnly {
		t.Error("expected VolumeMount.ReadOnly=true")
	}
	if !pod.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly {
		t.Error("expected PersistentVolumeClaimVolumeSource.ReadOnly=true")
	}
}

func TestCreatePodWithPVC_CustomImageAndCommand(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	customImage := "custom-image:latest"
	customCmd := []string{"/bin/sh", "-c", "echo hello"}

	pod, err := CreatePodWithPVC(ctx, client, PodOptions{
		Name:       "test-pod",
		Namespace:  "test-ns",
		PVCName:    "test-pvc",
		Image:      customImage,
		Command:    customCmd,
		VolumeMode: corev1.PersistentVolumeBlock,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pod.Spec.Containers[0].Image != customImage {
		t.Errorf("expected image %q, got %q", customImage, pod.Spec.Containers[0].Image)
	}
	if len(pod.Spec.Containers[0].Command) != len(customCmd) {
		t.Errorf("expected command len %d, got %d", len(customCmd), len(pod.Spec.Containers[0].Command))
	}
	for i, c := range customCmd {
		if pod.Spec.Containers[0].Command[i] != c {
			t.Errorf("command[%d] = %q, want %q", i, pod.Spec.Containers[0].Command[i], c)
		}
	}
}

func TestWaitForPVCBound_ImmediatelyBound(t *testing.T) {
	ctx := context.Background()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pvc", Namespace: "test-ns"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	client := fake.NewClientset(pvc)
	if err := WaitForPVCBound(ctx, client, "test-ns", "my-pvc", 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForPVCResized_ImmediatelyResized(t *testing.T) {
	ctx := context.Background()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pvc", Namespace: "test-ns"},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: mustParseQuantity("10Gi"),
			},
		},
	}
	client := fake.NewClientset(pvc)
	if err := WaitForPVCResized(ctx, client, "test-ns", "my-pvc", mustParseQuantity("10Gi"), 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForNamespaceDeleted_AlreadyGone(t *testing.T) {
	ctx := context.Background()
	// Empty clientset — namespace never existed; Get returns NotFound immediately.
	client := fake.NewClientset()
	if err := WaitForNamespaceDeleted(ctx, client, "gone-ns", 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForSnapshotReady_ImmediatelyReady(t *testing.T) {
	ctx := context.Background()
	readyToUse := true
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "my-snap", Namespace: "test-ns"},
		Status: &snapshotv1.VolumeSnapshotStatus{
			ReadyToUse: &readyToUse,
		},
	}
	client := snapfake.NewSimpleClientset(vs)
	got, err := WaitForSnapshotReady(ctx, client, "test-ns", "my-snap", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Name != "my-snap" {
		t.Errorf("expected snapshot %q, got %v", "my-snap", got)
	}
}

// TestWaitForSnapshotReady_ErrorStatus verifies that WaitForSnapshotReady fails fast
// when the VolumeSnapshot has an error in its status, mirroring the fast-fail
// behaviour of WaitForPVCBound on the Lost phase.
func TestWaitForSnapshotReady_ErrorStatus(t *testing.T) {
	ctx := context.Background()
	errMsg := "failed to create snapshot on storage backend"
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "broken-snap", Namespace: "test-ns"},
		Status: &snapshotv1.VolumeSnapshotStatus{
			Error: &snapshotv1.VolumeSnapshotError{
				Message: &errMsg,
			},
		},
	}
	client := snapfake.NewSimpleClientset(vs)
	_, err := WaitForSnapshotReady(ctx, client, "test-ns", "broken-snap", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for snapshot with error status, got nil")
	}
	if !strings.Contains(err.Error(), errMsg) {
		t.Errorf("error %q does not contain driver error message %q", err.Error(), errMsg)
	}
}

// TestWaitForSnapshotReady_ErrorStatusNilMessage verifies that WaitForSnapshotReady
// fails fast when Status.Error is non-nil but Message is nil (defensive check: the
// error condition is real but the driver omitted the message string).
func TestWaitForSnapshotReady_ErrorStatusNilMessage(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "broken-snap-no-msg", Namespace: "test-ns"},
		Status: &snapshotv1.VolumeSnapshotStatus{
			Error: &snapshotv1.VolumeSnapshotError{
				// Message deliberately left nil
			},
		},
	}
	client := snapfake.NewSimpleClientset(vs)
	_, err := WaitForSnapshotReady(ctx, client, "test-ns", "broken-snap-no-msg", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for snapshot with non-nil Error but nil Message, got nil")
	}
	if !strings.Contains(err.Error(), "error condition") {
		t.Errorf("error %q should mention 'error condition'", err.Error())
	}
}

func TestWaitForSnapshotDeleted_AlreadyGone(t *testing.T) {
	ctx := context.Background()
	client := snapfake.NewSimpleClientset()
	if err := WaitForSnapshotDeleted(ctx, client, "test-ns", "gone-snap", 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForSnapshotContentDeleted_AlreadyGone(t *testing.T) {
	ctx := context.Background()
	client := snapfake.NewSimpleClientset()
	if err := WaitForSnapshotContentDeleted(ctx, client, "gone-content", 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForPodRunning_ImmediatelyRunning(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	client := fake.NewClientset(pod)
	if err := WaitForPodRunning(ctx, client, "test-ns", "my-pod", 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForPodRunning_ImmediatelyFailed(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"},
		Status: corev1.PodStatus{
			Phase:   corev1.PodFailed,
			Reason:  "OOMKilled",
			Message: "memory limit exceeded",
		},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodRunning(ctx, client, "test-ns", "my-pod", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for Failed pod, got nil")
	}
}

func TestWaitForPodRunning_ImmediatelySucceeded(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodRunning(ctx, client, "test-ns", "my-pod", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for Succeeded pod, got nil")
	}
}

// TestWaitForPodRunning_ErrImagePull verifies that WaitForPodRunning fails
// immediately when a container reports ErrImagePull, rather than waiting for
// the full timeout.
func TestWaitForPodRunning_ErrImagePull(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ErrImagePull",
							Message: "failed to pull image: not found",
						},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodRunning(ctx, client, "test-ns", "my-pod", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for ErrImagePull container, got nil")
	}
	if !strings.Contains(err.Error(), "ErrImagePull") {
		t.Errorf("expected error to mention ErrImagePull, got: %v", err)
	}
}

// TestWaitForPodRunning_ImagePullBackOff verifies that WaitForPodRunning fails
// immediately when a container reports ImagePullBackOff.
func TestWaitForPodRunning_ImagePullBackOff(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ImagePullBackOff",
							Message: "Back-off pulling image",
						},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodRunning(ctx, client, "test-ns", "my-pod", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for ImagePullBackOff container, got nil")
	}
	if !strings.Contains(err.Error(), "ImagePullBackOff") {
		t.Errorf("expected error to mention ImagePullBackOff, got: %v", err)
	}
}

// TestWaitForPodRunning_InitContainerErrImagePull verifies that ErrImagePull
// on an init container also triggers a fast failure.
func TestWaitForPodRunning_InitContainerErrImagePull(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "init",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ErrImagePull",
							Message: "manifest unknown",
						},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodRunning(ctx, client, "test-ns", "my-pod", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for init container ErrImagePull, got nil")
	}
	if !strings.Contains(err.Error(), "ErrImagePull") {
		t.Errorf("expected error to mention ErrImagePull, got: %v", err)
	}
}

// TestWaitForPodRunning_InitContainerImagePullBackOff verifies that ImagePullBackOff
// on an init container also triggers a fast failure.
func TestWaitForPodRunning_InitContainerImagePullBackOff(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "init",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ImagePullBackOff",
							Message: "Back-off pulling image",
						},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodRunning(ctx, client, "test-ns", "my-pod", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for init container ImagePullBackOff, got nil")
	}
	if !strings.Contains(err.Error(), "ImagePullBackOff") {
		t.Errorf("expected error to mention ImagePullBackOff, got: %v", err)
	}
}

// TestWaitForPodRunning_InvalidImageName verifies that InvalidImageName (a terminal
// image reference error) on a regular container triggers a fast failure.
func TestWaitForPodRunning_InvalidImageName(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "InvalidImageName",
							Message: "invalid reference format",
						},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodRunning(ctx, client, "test-ns", "my-pod", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for InvalidImageName container, got nil")
	}
	if !strings.Contains(err.Error(), "InvalidImageName") {
		t.Errorf("expected error to mention InvalidImageName, got: %v", err)
	}
}

// TestWaitForPodRunning_InitContainerInvalidImageName verifies that InvalidImageName
// on an init container also triggers a fast failure.
func TestWaitForPodRunning_InitContainerInvalidImageName(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "init",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "InvalidImageName",
							Message: "invalid reference format",
						},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodRunning(ctx, client, "test-ns", "my-pod", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for init container InvalidImageName, got nil")
	}
	if !strings.Contains(err.Error(), "InvalidImageName") {
		t.Errorf("expected error to mention InvalidImageName, got: %v", err)
	}
}

// TestWaitForPodRunning_CrashLoopBackOff verifies that WaitForPodRunning fails
// immediately when a container reports CrashLoopBackOff, rather than waiting for
// the full timeout.
func TestWaitForPodRunning_CrashLoopBackOff(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "CrashLoopBackOff",
							Message: "back-off 5m0s restarting failed container",
						},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodRunning(ctx, client, "test-ns", "my-pod", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for CrashLoopBackOff container, got nil")
	}
	if !strings.Contains(err.Error(), "CrashLoopBackOff") {
		t.Errorf("expected error to mention CrashLoopBackOff, got: %v", err)
	}
}

// TestWaitForPodRunning_CreateContainerError verifies that WaitForPodRunning
// fails immediately when a container reports CreateContainerError.
func TestWaitForPodRunning_CreateContainerError(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "CreateContainerError",
							Message: "failed to create container: OCI runtime error",
						},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodRunning(ctx, client, "test-ns", "my-pod", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for CreateContainerError container, got nil")
	}
	if !strings.Contains(err.Error(), "CreateContainerError") {
		t.Errorf("expected error to mention CreateContainerError, got: %v", err)
	}
}

// TestWaitForPodRunning_InitContainerCrashLoopBackOff verifies that CrashLoopBackOff
// on an init container also triggers a fast failure.
func TestWaitForPodRunning_InitContainerCrashLoopBackOff(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "test-ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "init",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "CrashLoopBackOff",
							Message: "back-off 5m0s restarting failed container",
						},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodRunning(ctx, client, "test-ns", "my-pod", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for init container CrashLoopBackOff, got nil")
	}
	if !strings.Contains(err.Error(), "CrashLoopBackOff") {
		t.Errorf("expected error to mention CrashLoopBackOff, got: %v", err)
	}
}

func TestWaitForPodDeleted_AlreadyGone(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	if err := WaitForPodDeleted(ctx, client, "test-ns", "gone-pod", 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForSnapshotContentDeleted_Timeout(t *testing.T) {
	ctx := context.Background()
	// Content exists and is not deleted — should time out.
	vsc := &snapshotv1.VolumeSnapshotContent{
		ObjectMeta: metav1.ObjectMeta{Name: "live-content"},
	}
	client := snapfake.NewSimpleClientset(vsc)
	err := WaitForSnapshotContentDeleted(ctx, client, "live-content", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for existing snapshot content, got nil")
	}
}

func TestWaitForPVCBound_Timeout(t *testing.T) {
	ctx := context.Background()
	// PVC exists but stays in Pending — should time out.
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "pending-pvc", Namespace: "test-ns"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
	client := fake.NewClientset(pvc)
	err := WaitForPVCBound(ctx, client, "test-ns", "pending-pvc", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for Pending PVC, got nil")
	}
}

func TestWaitForPodRunning_Timeout(t *testing.T) {
	ctx := context.Background()
	// Pod exists but stays in Pending — should time out.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pending-pod", Namespace: "test-ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodRunning(ctx, client, "test-ns", "pending-pod", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for Pending pod, got nil")
	}
}

func TestWaitForSnapshotReady_Timeout(t *testing.T) {
	ctx := context.Background()
	// Snapshot exists but ReadyToUse is false — should time out.
	notReady := false
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "not-ready-snap", Namespace: "test-ns"},
		Status: &snapshotv1.VolumeSnapshotStatus{
			ReadyToUse: &notReady,
		},
	}
	client := snapfake.NewSimpleClientset(vs)
	_, err := WaitForSnapshotReady(ctx, client, "test-ns", "not-ready-snap", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for not-ready snapshot, got nil")
	}
}

func TestWaitForPodDeleted_Timeout(t *testing.T) {
	ctx := context.Background()
	// Pod exists and is not deleted — should time out.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "live-pod", Namespace: "test-ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	client := fake.NewClientset(pod)
	err := WaitForPodDeleted(ctx, client, "test-ns", "live-pod", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for existing pod, got nil")
	}
}

func TestWaitForNamespaceDeleted_Timeout(t *testing.T) {
	ctx := context.Background()
	// Namespace exists and is not deleted — should time out.
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "live-ns"},
	}
	client := fake.NewClientset(ns)
	err := WaitForNamespaceDeleted(ctx, client, "live-ns", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for existing namespace, got nil")
	}
}

func TestWaitForSnapshotDeleted_Timeout(t *testing.T) {
	ctx := context.Background()
	// Snapshot exists and is not deleted — should time out.
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "live-snap", Namespace: "test-ns"},
	}
	client := snapfake.NewSimpleClientset(vs)
	err := WaitForSnapshotDeleted(ctx, client, "test-ns", "live-snap", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for existing snapshot, got nil")
	}
}

func TestWaitForPVCResized_Timeout(t *testing.T) {
	ctx := context.Background()
	// PVC exists but capacity is below the expected size — should time out.
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "small-pvc", Namespace: "test-ns"},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: mustParseQuantity("1Gi"),
			},
		},
	}
	client := fake.NewClientset(pvc)
	err := WaitForPVCResized(ctx, client, "test-ns", "small-pvc", mustParseQuantity("10Gi"), 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for under-sized PVC, got nil")
	}
}

func TestCreatePVC_FilesystemMode(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()

	filesystemMode := corev1.PersistentVolumeFilesystem
	pvc, err := CreatePVC(ctx, client, PVCOptions{
		Name:         "fs-pvc",
		Namespace:    "test-ns",
		StorageClass: "test-sc",
		VolumeMode:   &filesystemMode,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pvc.Spec.VolumeMode == nil || *pvc.Spec.VolumeMode != corev1.PersistentVolumeFilesystem {
		t.Errorf("expected VolumeMode=Filesystem, got %v", pvc.Spec.VolumeMode)
	}
}

func TestCreatePodWithPVC_DefaultCommand(t *testing.T) {
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
	cmd := pod.Spec.Containers[0].Command
	if len(cmd) != 2 || cmd[0] != "sleep" || cmd[1] != "infinity" {
		t.Errorf("expected default command [sleep infinity], got %v", cmd)
	}
}

func TestRebindPVWithVolumeMode_NonCSISource(t *testing.T) {
	ctx := context.Background()
	nonCSIPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "hostpath-pv"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: "/data"},
			},
		},
	}
	client := fake.NewClientset(nonCSIPV)

	blockMode := corev1.PersistentVolumeBlock
	err := RebindPVWithVolumeMode(ctx, client, "hostpath-pv", "new-pv", "new-pvc", "test-ns",
		blockMode, nil)
	if err == nil {
		t.Fatal("expected error for non-CSI source PV, got nil")
	}
}

// TestRebindPVWithVolumeMode_PVCCreateFailCleansUpPV verifies that when PVC creation
// fails after the PV was already created, the PV is cleaned up to avoid leaking it.
func TestRebindPVWithVolumeMode_PVCCreateFailCleansUpPV(t *testing.T) {
	ctx := context.Background()
	sourcePV := makeCSIPV("source-pv")
	client := fake.NewClientset(sourcePV)

	// Inject a failure for PVC create only.
	client.Fake.PrependReactor("create", "persistentvolumeclaims",
		func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
			return true, nil, k8serrors.NewInternalError(errors.New("injected PVC create failure"))
		})

	blockMode := corev1.PersistentVolumeBlock
	err := RebindPVWithVolumeMode(ctx, client, "source-pv", "new-pv", "new-pvc", "test-ns",
		blockMode, nil)
	if err == nil {
		t.Fatal("expected error when PVC creation fails, got nil")
	}

	// The PV that was created before the PVC failure should have been deleted.
	_, getErr := client.CoreV1().PersistentVolumes().Get(ctx, "new-pv", metav1.GetOptions{})
	if !k8serrors.IsNotFound(getErr) {
		t.Errorf("expected orphaned PV to be cleaned up after PVC create failure, got: %v", getErr)
	}
}

func TestIsRetryableAPIError(t *testing.T) {
	gr := schema.GroupResource{Group: "test", Resource: "pods"}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "server timeout",
			err:  k8serrors.NewServerTimeout(gr, "get", 5),
			want: true,
		},
		{
			name: "service unavailable",
			err:  k8serrors.NewServiceUnavailable("service unavailable"),
			want: true,
		},
		{
			name: "too many requests",
			err:  k8serrors.NewTooManyRequests("too many requests", 5),
			want: true,
		},
		{
			name: "timeout error",
			err:  k8serrors.NewTimeoutError("operation timed out", 5),
			want: true,
		},
		{
			name: "not found — not retryable",
			err:  k8serrors.NewNotFound(gr, "my-pod"),
			want: false,
		},
		{
			name: "already exists — not retryable",
			err:  k8serrors.NewAlreadyExists(gr, "my-pod"),
			want: false,
		},
		{
			name: "forbidden — not retryable",
			err:  k8serrors.NewForbidden(gr, "my-pod", errors.New("access denied")),
			want: false,
		},
		{
			name: "nil error — not retryable",
			err:  nil,
			want: false,
		},
		{
			name: "plain error — not retryable",
			err:  errors.New("some generic error"),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isRetryableAPIError(tc.err)
			if got != tc.want {
				t.Errorf("isRetryableAPIError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestWaitForPVCBound_NotFound verifies that WaitForPVCBound fails immediately
// (not after timeout) when the PVC does not exist, because NotFound is not retryable.
func TestWaitForPVCBound_NotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	err := WaitForPVCBound(ctx, client, "test-ns", "nonexistent-pvc", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for nonexistent PVC, got nil")
	}
}

// TestWaitForPVCBound_LostPhase verifies that WaitForPVCBound fails immediately
// (not after timeout) when the PVC enters Lost phase, which is a terminal state.
func TestWaitForPVCBound_LostPhase(t *testing.T) {
	ctx := context.Background()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "lost-pvc", Namespace: "test-ns"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimLost},
	}
	client := fake.NewClientset(pvc)
	err := WaitForPVCBound(ctx, client, "test-ns", "lost-pvc", 30*time.Second)
	if err == nil {
		t.Fatal("expected error for PVC in Lost phase, got nil")
	}
	if !strings.Contains(err.Error(), "Lost") {
		t.Errorf("expected error message to mention Lost phase, got: %v", err)
	}
}

// TestWaitForSnapshotReady_NotFound verifies that WaitForSnapshotReady fails immediately
// when the VolumeSnapshot does not exist, because NotFound is not retryable.
func TestWaitForSnapshotReady_NotFound(t *testing.T) {
	ctx := context.Background()
	client := snapfake.NewSimpleClientset()
	_, err := WaitForSnapshotReady(ctx, client, "test-ns", "nonexistent-snap", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for nonexistent VolumeSnapshot, got nil")
	}
}

// TestWaitForPodRunning_NotFound verifies that WaitForPodRunning fails immediately
// when the pod does not exist, because NotFound is not retryable.
func TestWaitForPodRunning_NotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	err := WaitForPodRunning(ctx, client, "test-ns", "nonexistent-pod", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for nonexistent pod, got nil")
	}
}

// TestWaitForPVCResized_NotFound verifies that WaitForPVCResized fails immediately
// when the PVC does not exist, because NotFound is not retryable.
func TestWaitForPVCResized_NotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	err := WaitForPVCResized(ctx, client, "test-ns", "nonexistent-pvc", mustParseQuantity("10Gi"), 5*time.Second)
	if err == nil {
		t.Fatal("expected error for nonexistent PVC, got nil")
	}
}

// prependOnceReactor injects err on the first matching action, then falls through
// to the default fake behaviour for all subsequent calls.
func prependOnceReactor(client *fake.Clientset, verb, resource string, err error) {
	called := false
	client.Fake.PrependReactor(verb, resource, func(action clientgotesting.Action) (bool, runtime.Object, error) {
		if !called {
			called = true
			return true, nil, err
		}
		return false, nil, nil // fall through to default
	})
}

// TestWaitForNamespaceDeleted_RetryableError verifies that a transient server-timeout
// error does not abort WaitForNamespaceDeleted; the function should retry and succeed
// once the namespace is gone.
func TestWaitForNamespaceDeleted_RetryableError(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "", Resource: "namespaces"}
	client := fake.NewClientset() // namespace does not exist after the first (failing) call
	prependOnceReactor(client, "get", "namespaces", k8serrors.NewServerTimeout(gr, "get", 0))

	err := WaitForNamespaceDeleted(ctx, client, "test-ns", 10*time.Second)
	if err != nil {
		t.Fatalf("expected success after transient error, got: %v", err)
	}
}

// TestWaitForPVCBound_RetryableError verifies that a transient server-timeout error
// does not abort WaitForPVCBound; the function should retry and succeed once the
// PVC is bound.
func TestWaitForPVCBound_RetryableError(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "", Resource: "persistentvolumeclaims"}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pvc", Namespace: "test-ns"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	client := fake.NewClientset(pvc)
	prependOnceReactor(client, "get", "persistentvolumeclaims", k8serrors.NewServerTimeout(gr, "get", 0))

	err := WaitForPVCBound(ctx, client, "test-ns", "test-pvc", 10*time.Second)
	if err != nil {
		t.Fatalf("expected success after transient error, got: %v", err)
	}
}

// TestWaitForPodDeleted_RetryableError verifies that a transient server-timeout error
// does not abort WaitForPodDeleted; the function should retry and succeed once the
// pod is gone.
func TestWaitForPodDeleted_RetryableError(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "", Resource: "pods"}
	client := fake.NewClientset() // pod does not exist after the first (failing) call
	prependOnceReactor(client, "get", "pods", k8serrors.NewServerTimeout(gr, "get", 0))

	err := WaitForPodDeleted(ctx, client, "test-ns", "test-pod", 10*time.Second)
	if err != nil {
		t.Fatalf("expected success after transient error, got: %v", err)
	}
}

// TestWaitForSnapshotDeleted_RetryableError verifies that a transient server-timeout
// error does not abort WaitForSnapshotDeleted; the function should retry and succeed
// once the VolumeSnapshot is gone.
func TestWaitForSnapshotDeleted_RetryableError(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "snapshot.storage.k8s.io", Resource: "volumesnapshots"}
	snapClient := snapfake.NewSimpleClientset() // snapshot absent after the first (failing) call
	called := false
	snapClient.Fake.PrependReactor("get", "volumesnapshots",
		func(_ clientgotesting.Action) (bool, runtime.Object, error) {
			if !called {
				called = true
				return true, nil, k8serrors.NewServerTimeout(gr, "get", 0)
			}
			return false, nil, nil
		},
	)

	err := WaitForSnapshotDeleted(ctx, snapClient, "test-ns", "test-snap", 10*time.Second)
	if err != nil {
		t.Fatalf("expected success after transient error, got: %v", err)
	}
}

// TestWaitForSnapshotContentDeleted_RetryableError verifies that a transient
// server-timeout error does not abort WaitForSnapshotContentDeleted.
func TestWaitForSnapshotContentDeleted_RetryableError(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "snapshot.storage.k8s.io", Resource: "volumesnapshotcontents"}
	snapClient := snapfake.NewSimpleClientset()
	called := false
	snapClient.Fake.PrependReactor("get", "volumesnapshotcontents",
		func(_ clientgotesting.Action) (bool, runtime.Object, error) {
			if !called {
				called = true
				return true, nil, k8serrors.NewServerTimeout(gr, "get", 0)
			}
			return false, nil, nil
		},
	)

	err := WaitForSnapshotContentDeleted(ctx, snapClient, "test-content", 10*time.Second)
	if err != nil {
		t.Fatalf("expected success after transient error, got: %v", err)
	}
}

// TestWaitForSnapshotReady_RetryableError verifies that a transient server-timeout
// error does not abort WaitForSnapshotReady; the function should retry and succeed
// once the VolumeSnapshot becomes ready.
func TestWaitForSnapshotReady_RetryableError(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "snapshot.storage.k8s.io", Resource: "volumesnapshots"}
	ready := true
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "test-snap", Namespace: "test-ns"},
		Status:     &snapshotv1.VolumeSnapshotStatus{ReadyToUse: &ready},
	}
	snapClient := snapfake.NewSimpleClientset(vs)
	called := false
	snapClient.Fake.PrependReactor("get", "volumesnapshots",
		func(_ clientgotesting.Action) (bool, runtime.Object, error) {
			if !called {
				called = true
				return true, nil, k8serrors.NewServerTimeout(gr, "get", 0)
			}
			return false, nil, nil
		},
	)

	if _, err := WaitForSnapshotReady(ctx, snapClient, "test-ns", "test-snap", 10*time.Second); err != nil {
		t.Fatalf("expected success after transient error, got: %v", err)
	}
}

// TestWaitForPodRunning_RetryableError verifies that a transient server-timeout
// error does not abort WaitForPodRunning; the function should retry and succeed
// once the pod enters Running phase.
func TestWaitForPodRunning_RetryableError(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "", Resource: "pods"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "test-ns"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	client := fake.NewClientset(pod)
	prependOnceReactor(client, "get", "pods", k8serrors.NewServerTimeout(gr, "get", 0))

	if err := WaitForPodRunning(ctx, client, "test-ns", "test-pod", 10*time.Second); err != nil {
		t.Fatalf("expected success after transient error, got: %v", err)
	}
}

// TestWaitForPVCDeleted_AlreadyGone verifies that WaitForPVCDeleted succeeds immediately
// when the PVC does not exist.
func TestWaitForPVCDeleted_AlreadyGone(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	if err := WaitForPVCDeleted(ctx, client, "test-ns", "gone-pvc", 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestWaitForPVCDeleted_Timeout verifies that WaitForPVCDeleted returns an error
// when the PVC still exists after the timeout elapses.
func TestWaitForPVCDeleted_Timeout(t *testing.T) {
	ctx := context.Background()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "live-pvc", Namespace: "test-ns"},
	}
	client := fake.NewClientset(pvc)
	err := WaitForPVCDeleted(ctx, client, "test-ns", "live-pvc", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for existing PVC, got nil")
	}
}

// TestWaitForPVCDeleted_RetryableError verifies that a transient server-timeout error
// does not abort WaitForPVCDeleted; the function should retry and succeed once the
// PVC is gone.
func TestWaitForPVCDeleted_RetryableError(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "", Resource: "persistentvolumeclaims"}
	client := fake.NewClientset() // PVC does not exist after the first (failing) call
	prependOnceReactor(client, "get", "persistentvolumeclaims", k8serrors.NewServerTimeout(gr, "get", 0))

	if err := WaitForPVCDeleted(ctx, client, "test-ns", "test-pvc", 10*time.Second); err != nil {
		t.Fatalf("expected success after transient error, got: %v", err)
	}
}

// TestWaitForPVCResized_RetryableError verifies that a transient server-timeout
// error does not abort WaitForPVCResized; the function should retry and succeed
// once the PVC capacity reflects the expected size.
func TestWaitForPVCResized_RetryableError(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "", Resource: "persistentvolumeclaims"}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pvc", Namespace: "test-ns"},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: mustParseQuantity("10Gi"),
			},
		},
	}
	client := fake.NewClientset(pvc)
	prependOnceReactor(client, "get", "persistentvolumeclaims", k8serrors.NewServerTimeout(gr, "get", 0))

	if err := WaitForPVCResized(ctx, client, "test-ns", "test-pvc", mustParseQuantity("10Gi"), 10*time.Second); err != nil {
		t.Fatalf("expected success after transient error, got: %v", err)
	}
}

// TestWaitForSnapshotReady_NilStatus verifies that a snapshot whose Status is nil
// is treated as not-yet-ready and the function eventually times out.
func TestWaitForSnapshotReady_NilStatus(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "nil-status-snap", Namespace: "test-ns"},
		// Status deliberately left nil
	}
	client := snapfake.NewSimpleClientset(vs)
	_, err := WaitForSnapshotReady(ctx, client, "test-ns", "nil-status-snap", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for snapshot with nil Status, got nil")
	}
}

// TestWaitForSnapshotReady_NilReadyToUse verifies that a snapshot whose Status is
// present but ReadyToUse is nil is treated as not-yet-ready and times out.
func TestWaitForSnapshotReady_NilReadyToUse(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "nil-ready-snap", Namespace: "test-ns"},
		Status:     &snapshotv1.VolumeSnapshotStatus{
			// ReadyToUse deliberately left nil
		},
	}
	client := snapfake.NewSimpleClientset(vs)
	_, err := WaitForSnapshotReady(ctx, client, "test-ns", "nil-ready-snap", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for snapshot with nil ReadyToUse, got nil")
	}
}

// TestGetSnapshotHandle_NonNilStatusNilHandle verifies that a VSC with a non-nil
// Status but a nil SnapshotHandle pointer is correctly detected and returns an error.
func TestGetSnapshotHandle_NonNilStatusNilHandle(t *testing.T) {
	ctx := context.Background()
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: "my-snap", Namespace: "test-ns"},
		Status: &snapshotv1.VolumeSnapshotStatus{
			BoundVolumeSnapshotContentName: ptr("my-content"),
		},
	}
	vsc := &snapshotv1.VolumeSnapshotContent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-content"},
		Status:     &snapshotv1.VolumeSnapshotContentStatus{
			// SnapshotHandle deliberately left nil (pointer is nil)
		},
	}
	client := snapfake.NewSimpleClientset(vs, vsc)

	_, err := GetSnapshotHandle(ctx, client, "test-ns", "my-snap")
	if err == nil {
		t.Fatal("expected error for VSC with non-nil Status but nil SnapshotHandle, got nil")
	}
}

// TestWaitForPVCResized_LostPhase verifies that WaitForPVCResized fails immediately
// when the PVC enters the Lost phase, consistent with the fast-fail in WaitForPVCBound.
func TestWaitForPVCResized_LostPhase(t *testing.T) {
	ctx := context.Background()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "lost-pvc", Namespace: "test-ns"},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase: corev1.ClaimLost,
		},
	}
	client := fake.NewClientset(pvc)
	err := WaitForPVCResized(ctx, client, "test-ns", "lost-pvc", mustParseQuantity("10Gi"), 10*time.Second)
	if err == nil {
		t.Fatal("expected error for PVC in Lost phase, got nil")
	}
	if !strings.Contains(err.Error(), "Lost") {
		t.Errorf("error %q does not mention Lost phase", err.Error())
	}
}

// TestDeleteNamespace_Error verifies that a non-NotFound delete error is propagated.
func TestDeleteNamespace_Error(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	gr := schema.GroupResource{Group: "", Resource: "namespaces"}
	client.Fake.PrependReactor("delete", "namespaces", func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewServerTimeout(gr, "delete", 0)
	})

	if err := DeleteNamespace(ctx, client, "test-ns"); err == nil {
		t.Fatal("expected error from Delete failure, got nil")
	}
}

// TestDeletePV_Error verifies that a non-NotFound delete error is propagated.
func TestDeletePV_Error(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	gr := schema.GroupResource{Group: "", Resource: "persistentvolumes"}
	client.Fake.PrependReactor("delete", "persistentvolumes", func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewServerTimeout(gr, "delete", 0)
	})

	if err := DeletePV(ctx, client, "my-pv"); err == nil {
		t.Fatal("expected error from Delete failure, got nil")
	}
}

// TestDeleteSnapshot_Error verifies that a non-NotFound delete error is propagated.
func TestDeleteSnapshot_Error(t *testing.T) {
	ctx := context.Background()
	client := snapfake.NewSimpleClientset()
	gr := schema.GroupResource{Group: "snapshot.storage.k8s.io", Resource: "volumesnapshots"}
	client.Fake.PrependReactor("delete", "volumesnapshots", func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewServerTimeout(gr, "delete", 0)
	})

	if err := DeleteSnapshot(ctx, client, "test-ns", "my-snap"); err == nil {
		t.Fatal("expected error from Delete failure, got nil")
	}
}

// TestWaitForPVCResized_NilCapacity verifies that a PVC whose Status.Capacity is nil
// is treated as not-yet-resized and the function eventually times out.
func TestWaitForPVCResized_NilCapacity(t *testing.T) {
	ctx := context.Background()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "nil-cap-pvc", Namespace: "test-ns"},
		Status:     corev1.PersistentVolumeClaimStatus{
			// Capacity deliberately left nil
		},
	}
	client := fake.NewClientset(pvc)
	err := WaitForPVCResized(ctx, client, "test-ns", "nil-cap-pvc", mustParseQuantity("10Gi"), 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for PVC with nil Status.Capacity, got nil")
	}
}

// TestRebindPVWithVolumeMode_PVCreateFail verifies that when the new PV creation
// fails, the error is propagated immediately with no cleanup side-effects (since
// the PV was never created, there is nothing to delete).
func TestRebindPVWithVolumeMode_PVCreateFail(t *testing.T) {
	ctx := context.Background()
	sourcePV := makeCSIPV("source-pv")
	client := fake.NewClientset(sourcePV)

	// Inject a failure for PV create only.
	client.Fake.PrependReactor("create", "persistentvolumes",
		func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
			return true, nil, k8serrors.NewInternalError(errors.New("injected PV create failure"))
		})

	blockMode := corev1.PersistentVolumeBlock
	err := RebindPVWithVolumeMode(ctx, client, "source-pv", "new-pv", "new-pvc", "test-ns",
		blockMode, nil)
	if err == nil {
		t.Fatal("expected error when PV creation fails, got nil")
	}

	// The PVC should not have been created, since we failed before that step.
	_, getErr := client.CoreV1().PersistentVolumeClaims("test-ns").Get(ctx, "new-pvc", metav1.GetOptions{})
	if !k8serrors.IsNotFound(getErr) {
		t.Errorf("expected PVC to not exist after PV create failure, got: %v", getErr)
	}
}

// TestRebindPVWithVolumeMode_CSIFieldsCopied verifies that Driver, VolumeAttributes,
// NodeStageSecretRef, and NodePublishSecretRef are all copied from the source PV to
// the new PV, ensuring the new PV can be mounted by the CSI driver.
func TestRebindPVWithVolumeMode_CSIFieldsCopied(t *testing.T) {
	ctx := context.Background()
	secretRef := &corev1.SecretReference{Name: "csi-secret", Namespace: "rook-ceph"}
	sourcePV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "source-pv"},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: "rbd-sc",
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: mustParseQuantity("1Gi"),
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "rbd.csi.ceph.com",
					VolumeHandle: "csi-handle-abc123",
					VolumeAttributes: map[string]string{
						"imageName": "csi-vol-abc123",
						"pool":      "ocs-storagecluster-cephblockpool",
					},
					NodeStageSecretRef:   secretRef,
					NodePublishSecretRef: secretRef,
				},
			},
		},
	}
	client := fake.NewClientset(sourcePV)

	blockMode := corev1.PersistentVolumeBlock
	err := RebindPVWithVolumeMode(ctx, client, "source-pv", "new-pv", "new-pvc", "test-ns",
		blockMode, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newPV, err := client.CoreV1().PersistentVolumes().Get(ctx, "new-pv", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("new PV not found: %v", err)
	}
	csi := newPV.Spec.CSI
	if csi == nil {
		t.Fatal("new PV has no CSI source")
	}
	if csi.Driver != sourcePV.Spec.CSI.Driver {
		t.Errorf("Driver: got %q, want %q", csi.Driver, sourcePV.Spec.CSI.Driver)
	}
	if csi.VolumeHandle != sourcePV.Spec.CSI.VolumeHandle {
		t.Errorf("VolumeHandle: got %q, want %q", csi.VolumeHandle, sourcePV.Spec.CSI.VolumeHandle)
	}
	if csi.VolumeAttributes["imageName"] != "csi-vol-abc123" {
		t.Errorf("VolumeAttributes[imageName]: got %q, want %q", csi.VolumeAttributes["imageName"], "csi-vol-abc123")
	}
	if csi.NodeStageSecretRef == nil || csi.NodeStageSecretRef.Name != secretRef.Name {
		t.Errorf("NodeStageSecretRef: got %v, want %v", csi.NodeStageSecretRef, secretRef)
	}
	if csi.NodePublishSecretRef == nil || csi.NodePublishSecretRef.Name != secretRef.Name {
		t.Errorf("NodePublishSecretRef: got %v, want %v", csi.NodePublishSecretRef, secretRef)
	}
}
