// Package k8s provides Kubernetes resource lifecycle helpers for E2E tests.
package k8s

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	snapclient "github.com/kubernetes-csi/external-snapshotter/client/v8/clientset/versioned"
)

// CreateNamespace creates a namespace, ignoring AlreadyExists.
// Sets PodSecurity labels to "privileged" to allow block device access in test pods.
func CreateNamespace(ctx context.Context, clientset kubernetes.Interface, name string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			"pod-security.kubernetes.io/enforce": "privileged",
			"pod-security.kubernetes.io/audit":   "privileged",
			"pod-security.kubernetes.io/warn":    "privileged",
		},
	}}
	_, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// DeleteNamespace deletes a namespace, ignoring NotFound.
func DeleteNamespace(ctx context.Context, clientset kubernetes.Interface, name string) error {
	err := clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// WaitForNamespaceDeleted waits until the namespace is fully removed.
func WaitForNamespaceDeleted(ctx context.Context, clientset kubernetes.Interface, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
}

// PVCOptions configures PVC creation.
type PVCOptions struct {
	Name             string
	Namespace        string
	StorageClass     string
	Size             string
	AccessModes      []corev1.PersistentVolumeAccessMode
	VolumeMode       *corev1.PersistentVolumeMode
	DataSource       *corev1.TypedLocalObjectReference
	DataSourceRef    *corev1.TypedObjectReference
	SnapshotSource   string // VolumeSnapshot name (shorthand for DataSource)
	PVCCloneSource   string // PVC name to clone from (shorthand for DataSource)
}

// CreatePVC creates a PersistentVolumeClaim.
func CreatePVC(ctx context.Context, clientset kubernetes.Interface, opts PVCOptions) (*corev1.PersistentVolumeClaim, error) {
	if len(opts.AccessModes) == 0 {
		opts.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}
	if opts.Size == "" {
		opts.Size = "1Gi"
	}

	blockMode := corev1.PersistentVolumeBlock
	if opts.VolumeMode == nil {
		opts.VolumeMode = &blockMode
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: opts.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: opts.AccessModes,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(opts.Size),
				},
			},
			StorageClassName: &opts.StorageClass,
			VolumeMode:       opts.VolumeMode,
		},
	}

	// Set DataSource from snapshot or clone shorthand
	if opts.SnapshotSource != "" {
		apiGroup := "snapshot.storage.k8s.io"
		pvc.Spec.DataSource = &corev1.TypedLocalObjectReference{
			APIGroup: &apiGroup,
			Kind:     "VolumeSnapshot",
			Name:     opts.SnapshotSource,
		}
	} else if opts.PVCCloneSource != "" {
		pvc.Spec.DataSource = &corev1.TypedLocalObjectReference{
			Kind: "PersistentVolumeClaim",
			Name: opts.PVCCloneSource,
		}
	} else if opts.DataSource != nil {
		pvc.Spec.DataSource = opts.DataSource
	}

	return clientset.CoreV1().PersistentVolumeClaims(opts.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
}

// CreateROXPVCFromSnapshot creates a ReadOnlyMany PVC from a VolumeSnapshot.
func CreateROXPVCFromSnapshot(ctx context.Context, clientset kubernetes.Interface, name, namespace, storageClass, snapshotName, size string) (*corev1.PersistentVolumeClaim, error) {
	return CreatePVC(ctx, clientset, PVCOptions{
		Name:           name,
		Namespace:      namespace,
		StorageClass:   storageClass,
		Size:           size,
		AccessModes:    []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany},
		SnapshotSource: snapshotName,
	})
}

// WaitForPVCBound waits until a PVC reaches Bound phase.
func WaitForPVCBound(ctx context.Context, clientset kubernetes.Interface, namespace, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pvc, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return pvc.Status.Phase == corev1.ClaimBound, nil
	})
}

// DeletePVC deletes a PVC, ignoring NotFound.
func DeletePVC(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
	err := clientset.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// ResizePVC patches a PVC to request a new storage size.
func ResizePVC(ctx context.Context, clientset kubernetes.Interface, namespace, name, newSize string) error {
	patchData := fmt.Sprintf(`{"spec":{"resources":{"requests":{"storage":"%s"}}}}`, newSize)
	_, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patchData), metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("resize PVC %s/%s to %s: %w", namespace, name, newSize, err)
	}
	return nil
}

// WaitForPVCResized waits until a PVC's status capacity reaches the expected size.
func WaitForPVCResized(ctx context.Context, clientset kubernetes.Interface, namespace, name string, expectedSize resource.Quantity, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pvc, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if pvc.Status.Capacity == nil {
			return false, nil
		}
		currentSize := pvc.Status.Capacity[corev1.ResourceStorage]
		return currentSize.Cmp(expectedSize) >= 0, nil
	})
}

// CreateSnapshot creates a VolumeSnapshot for a given PVC.
func CreateSnapshot(ctx context.Context, snapClient snapclient.Interface, name, namespace, pvcName, snapshotClass string) (*snapshotv1.VolumeSnapshot, error) {
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: snapshotv1.VolumeSnapshotSpec{
			VolumeSnapshotClassName: &snapshotClass,
			Source: snapshotv1.VolumeSnapshotSource{
				PersistentVolumeClaimName: &pvcName,
			},
		},
	}

	return snapClient.SnapshotV1().VolumeSnapshots(namespace).Create(ctx, vs, metav1.CreateOptions{})
}

// WaitForSnapshotReady waits until a VolumeSnapshot is ReadyToUse.
func WaitForSnapshotReady(ctx context.Context, snapClient snapclient.Interface, namespace, name string, timeout time.Duration) (*snapshotv1.VolumeSnapshot, error) {
	var result *snapshotv1.VolumeSnapshot
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		vs, err := snapClient.SnapshotV1().VolumeSnapshots(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if vs.Status == nil || vs.Status.ReadyToUse == nil {
			return false, nil
		}
		if *vs.Status.ReadyToUse {
			result = vs
			return true, nil
		}
		return false, nil
	})
	return result, err
}

// DeleteSnapshot deletes a VolumeSnapshot, ignoring NotFound.
func DeleteSnapshot(ctx context.Context, snapClient snapclient.Interface, namespace, name string) error {
	err := snapClient.SnapshotV1().VolumeSnapshots(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// WaitForSnapshotDeleted waits until a VolumeSnapshot is fully removed.
func WaitForSnapshotDeleted(ctx context.Context, snapClient snapclient.Interface, namespace, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := snapClient.SnapshotV1().VolumeSnapshots(namespace).Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
}

// GetSnapshotHandle returns the CSI snapshot handle from a VolumeSnapshot.
func GetSnapshotHandle(ctx context.Context, snapClient snapclient.Interface, namespace, name string) (string, error) {
	vs, err := snapClient.SnapshotV1().VolumeSnapshots(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get VolumeSnapshot %s/%s: %w", namespace, name, err)
	}

	if vs.Status == nil || vs.Status.BoundVolumeSnapshotContentName == nil {
		return "", fmt.Errorf("VolumeSnapshot %s/%s not bound", namespace, name)
	}

	vsc, err := snapClient.SnapshotV1().VolumeSnapshotContents().Get(ctx, *vs.Status.BoundVolumeSnapshotContentName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get VolumeSnapshotContent: %w", err)
	}

	if vsc.Status == nil || vsc.Status.SnapshotHandle == nil {
		return "", fmt.Errorf("VolumeSnapshotContent has no snapshot handle")
	}

	return *vsc.Status.SnapshotHandle, nil
}

// PodOptions configures pod creation.
type PodOptions struct {
	Name      string
	Namespace string
	Image     string
	PVCName   string
	ReadOnly  bool
	Command   []string
	MountPath string
	VolumeMode corev1.PersistentVolumeMode
}

// CreatePodWithPVC creates a pod that mounts a PVC.
func CreatePodWithPVC(ctx context.Context, clientset kubernetes.Interface, opts PodOptions) (*corev1.Pod, error) {
	if opts.Image == "" {
		opts.Image = "registry.access.redhat.com/ubi9/ubi-minimal:latest"
	}
	if opts.MountPath == "" {
		if opts.VolumeMode == corev1.PersistentVolumeBlock {
			opts.MountPath = "/dev/xvda"
		} else {
			opts.MountPath = "/mnt/data"
		}
	}
	if opts.Command == nil {
		opts.Command = []string{"sleep", "3600"}
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: opts.Namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "main",
					Image:   opts.Image,
					Command: opts.Command,
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: opts.PVCName,
							ReadOnly:  opts.ReadOnly,
						},
					},
				},
			},
		},
	}

	if opts.VolumeMode == corev1.PersistentVolumeBlock {
		pod.Spec.Containers[0].VolumeDevices = []corev1.VolumeDevice{
			{
				Name:       "data",
				DevicePath: opts.MountPath,
			},
		}
	} else {
		pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
			{
				Name:      "data",
				MountPath: opts.MountPath,
				ReadOnly:  opts.ReadOnly,
			},
		}
	}

	return clientset.CoreV1().Pods(opts.Namespace).Create(ctx, pod, metav1.CreateOptions{})
}

// WaitForPodRunning waits until a pod reaches Running phase.
func WaitForPodRunning(ctx context.Context, clientset kubernetes.Interface, namespace, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return pod.Status.Phase == corev1.PodRunning, nil
	})
}

// DeletePod deletes a pod, ignoring NotFound.
func DeletePod(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
	err := clientset.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// WaitForPodDeleted waits until a pod no longer exists.
func WaitForPodDeleted(ctx context.Context, clientset kubernetes.Interface, namespace, name string, timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		_, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return nil
		}
		select {
		case <-deadline:
			return fmt.Errorf("pod %s/%s still exists after %v", namespace, name, timeout)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ExecInPod executes a command in a pod and returns stdout/stderr.
func ExecInPod(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, namespace, podName, container string, command []string) (string, string, error) {
	if container == "" {
		container = "main"
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command:   command,
			Container: container,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("failed to create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	return stdout.String(), stderr.String(), err
}

// GetToolboxPod finds the Ceph toolbox pod in the given namespace.
// Tries multiple label selectors to handle different ODF versions.
func GetToolboxPod(ctx context.Context, clientset kubernetes.Interface, namespace string) (*corev1.Pod, error) {
	// Try multiple selectors for different ODF/Rook versions
	selectors := []string{
		"app=rook-ceph-tools",
		"app=rook-ceph-tools,app.kubernetes.io/part-of=rook-ceph-operator",
	}
	for _, selector := range selectors {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		})
		if err != nil {
			continue
		}
		for i := range pods.Items {
			if pods.Items[i].Status.Phase == corev1.PodRunning {
				return &pods.Items[i], nil
			}
		}
		if len(pods.Items) > 0 {
			return &pods.Items[0], nil
		}
	}

	// Fall back to name-based matching
	allPods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}
	for i := range allPods.Items {
		if strings.Contains(allPods.Items[i].Name, "rook-ceph-tools") {
			return &allPods.Items[i], nil
		}
	}

	return nil, fmt.Errorf("no Ceph toolbox pod found in namespace %s (tried labels and name matching)", namespace)
}
