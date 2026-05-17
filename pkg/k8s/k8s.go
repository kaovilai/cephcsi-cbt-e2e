// Package k8s provides Kubernetes resource lifecycle helpers for E2E tests.
package k8s

import (
	"bytes"
	"context"
	"encoding/json"
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

const (
	// pollInterval is the default polling interval for Wait* functions.
	pollInterval = 5 * time.Second
	// fastPollInterval is used for short-lived resources like pods.
	fastPollInterval = 2 * time.Second

	// DefaultPodImage is the default container image used for test pods.
	DefaultPodImage = "registry.access.redhat.com/ubi9/ubi-minimal:latest"

	// DefaultBlockDevicePath is the default device path for Block-mode PVC mounts in test pods.
	DefaultBlockDevicePath = "/dev/xvda"

	// DefaultFilesystemMountPath is the default mount directory for Filesystem-mode PVC mounts in test pods.
	DefaultFilesystemMountPath = "/mnt/data"

	// defaultContainerName is the name given to the main container in test pods,
	// and the fallback used by ExecInPod when no explicit container is specified.
	defaultContainerName = "main"

	// volumeName is the volume name used inside test pods for the PVC attachment.
	volumeName = "data"

	// snapshotAPIGroup is the API group for VolumeSnapshot resources.
	snapshotAPIGroup = "snapshot.storage.k8s.io"

	// toolboxPodNameFragment is used to find the Ceph toolbox pod by name when
	// label-based lookups fail.
	toolboxPodNameFragment = "rook-ceph-tools"
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
	if err != nil {
		return fmt.Errorf("create namespace %s: %w", name, err)
	}
	return nil
}

// DeleteNamespace deletes a namespace, ignoring NotFound.
func DeleteNamespace(ctx context.Context, clientset kubernetes.Interface, name string) error {
	err := clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete namespace %s: %w", name, err)
	}
	return nil
}

// WaitForNamespaceDeleted waits until the namespace is fully removed.
func WaitForNamespaceDeleted(ctx context.Context, clientset kubernetes.Interface, name string, timeout time.Duration) error {
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("namespace %s not deleted after %v: %w", name, timeout, err)
	}
	return nil
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
		apiGroup := snapshotAPIGroup
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
	// DataSourceRef supports cross-namespace and newer group/kind references.
	// It is independent of DataSource and set separately when provided.
	if opts.DataSourceRef != nil {
		pvc.Spec.DataSourceRef = opts.DataSourceRef
	}

	result, err := clientset.CoreV1().PersistentVolumeClaims(opts.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create PVC %s/%s: %w", opts.Namespace, opts.Name, err)
	}
	return result, nil
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
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		pvc, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return pvc.Status.Phase == corev1.ClaimBound, nil
	}); err != nil {
		return fmt.Errorf("PVC %s/%s not bound after %v: %w", namespace, name, timeout, err)
	}
	return nil
}

// DeletePVC deletes a PVC, ignoring NotFound.
func DeletePVC(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
	err := clientset.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete PVC %s/%s: %w", namespace, name, err)
	}
	return nil
}

// ResizePVC patches a PVC to request a new storage size.
func ResizePVC(ctx context.Context, clientset kubernetes.Interface, namespace, name, newSize string) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{
					"storage": newSize,
				},
			},
		},
	}
	patchData, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal resize patch for PVC %s/%s: %w", namespace, name, err)
	}
	_, err = clientset.CoreV1().PersistentVolumeClaims(namespace).Patch(
		ctx, name, types.MergePatchType, patchData, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("resize PVC %s/%s to %s: %w", namespace, name, newSize, err)
	}
	return nil
}

// WaitForPVCResized waits until a PVC's status capacity reaches the expected size.
func WaitForPVCResized(ctx context.Context, clientset kubernetes.Interface, namespace, name string, expectedSize resource.Quantity, timeout time.Duration) error {
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		pvc, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if pvc.Status.Capacity == nil {
			return false, nil
		}
		currentSize := pvc.Status.Capacity[corev1.ResourceStorage]
		return currentSize.Cmp(expectedSize) >= 0, nil
	}); err != nil {
		return fmt.Errorf("PVC %s/%s not resized to %s after %v: %w", namespace, name, expectedSize.String(), timeout, err)
	}
	return nil
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

	result, err := snapClient.SnapshotV1().VolumeSnapshots(namespace).Create(ctx, vs, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create VolumeSnapshot %s/%s: %w", namespace, name, err)
	}
	return result, nil
}

// WaitForSnapshotReady waits until a VolumeSnapshot is ReadyToUse.
func WaitForSnapshotReady(ctx context.Context, snapClient snapclient.Interface, namespace, name string, timeout time.Duration) (*snapshotv1.VolumeSnapshot, error) {
	var result *snapshotv1.VolumeSnapshot
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		vs, err := snapClient.SnapshotV1().VolumeSnapshots(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if vs.Status == nil || vs.Status.ReadyToUse == nil {
			return false, nil
		}
		if *vs.Status.ReadyToUse {
			result = vs
			return true, nil
		}
		return false, nil
	}); err != nil {
		return nil, fmt.Errorf("VolumeSnapshot %s/%s not ready after %v: %w", namespace, name, timeout, err)
	}
	return result, nil
}

// DeleteSnapshot deletes a VolumeSnapshot, ignoring NotFound.
func DeleteSnapshot(ctx context.Context, snapClient snapclient.Interface, namespace, name string) error {
	err := snapClient.SnapshotV1().VolumeSnapshots(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete VolumeSnapshot %s/%s: %w", namespace, name, err)
	}
	return nil
}

// WaitForSnapshotDeleted waits until a VolumeSnapshot is fully removed.
func WaitForSnapshotDeleted(ctx context.Context, snapClient snapclient.Interface, namespace, name string, timeout time.Duration) error {
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := snapClient.SnapshotV1().VolumeSnapshots(namespace).Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("VolumeSnapshot %s/%s not deleted after %v: %w", namespace, name, timeout, err)
	}
	return nil
}

// GetSnapshotContentName returns the bound VolumeSnapshotContent name for a VolumeSnapshot.
func GetSnapshotContentName(ctx context.Context, snapClient snapclient.Interface, namespace, name string) (string, error) {
	vs, err := snapClient.SnapshotV1().VolumeSnapshots(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get VolumeSnapshot %s/%s: %w", namespace, name, err)
	}
	if vs.Status == nil || vs.Status.BoundVolumeSnapshotContentName == nil {
		return "", fmt.Errorf("VolumeSnapshot %s/%s not bound", namespace, name)
	}
	return *vs.Status.BoundVolumeSnapshotContentName, nil
}

// WaitForSnapshotContentDeleted waits until a VolumeSnapshotContent is fully removed.
func WaitForSnapshotContentDeleted(ctx context.Context, snapClient snapclient.Interface, name string, timeout time.Duration) error {
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := snapClient.SnapshotV1().VolumeSnapshotContents().Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("VolumeSnapshotContent %s not deleted after %v: %w", name, timeout, err)
	}
	return nil
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
		return "", fmt.Errorf("VolumeSnapshotContent %s has no snapshot handle", *vs.Status.BoundVolumeSnapshotContentName)
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
		opts.Image = DefaultPodImage
	}
	if opts.MountPath == "" {
		if opts.VolumeMode == corev1.PersistentVolumeBlock {
			opts.MountPath = DefaultBlockDevicePath
		} else {
			opts.MountPath = DefaultFilesystemMountPath
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
					Name:    defaultContainerName,
					Image:   opts.Image,
					Command: opts.Command,
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: volumeName,
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
				Name:       volumeName,
				DevicePath: opts.MountPath,
			},
		}
	} else {
		pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
			{
				Name:      volumeName,
				MountPath: opts.MountPath,
				ReadOnly:  opts.ReadOnly,
			},
		}
	}

	result, err := clientset.CoreV1().Pods(opts.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create pod %s/%s: %w", opts.Namespace, opts.Name, err)
	}
	return result, nil
}

// WaitForPodRunning waits until a pod reaches Running phase.
// Returns immediately with an error if the pod enters a terminal failure state.
func WaitForPodRunning(ctx context.Context, clientset kubernetes.Interface, namespace, name string, timeout time.Duration) error {
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		switch pod.Status.Phase {
		case corev1.PodRunning:
			return true, nil
		case corev1.PodFailed:
			return false, fmt.Errorf("pod %s/%s entered Failed phase (reason: %s, message: %s)",
				namespace, name, pod.Status.Reason, pod.Status.Message)
		case corev1.PodSucceeded:
			// Succeeded without ever entering Running — treat as failure for test pods
			return false, fmt.Errorf("pod %s/%s exited unexpectedly with Succeeded phase", namespace, name)
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("pod %s/%s not running after %v: %w", namespace, name, timeout, err)
	}
	return nil
}

// DeletePod deletes a pod, ignoring NotFound.
func DeletePod(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
	err := clientset.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete pod %s/%s: %w", namespace, name, err)
	}
	return nil
}

// WaitForPodDeleted waits until a pod no longer exists.
func WaitForPodDeleted(ctx context.Context, clientset kubernetes.Interface, namespace, name string, timeout time.Duration) error {
	if err := wait.PollUntilContextTimeout(ctx, fastPollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("pod %s/%s still exists after %v: %w", namespace, name, timeout, err)
	}
	return nil
}

// RebindPVWithVolumeMode creates a new PV with a different VolumeMode pointing to
// the same CSI volume handle as an existing PV, then creates a PVC bound to it.
// This simulates Velero's rebinding workflow for volume mode conversion during restore.
func RebindPVWithVolumeMode(ctx context.Context, clientset kubernetes.Interface,
	sourcePVName, newPVName, newPVCName, namespace string,
	targetMode corev1.PersistentVolumeMode, accessModes []corev1.PersistentVolumeAccessMode) error {

	// Get the source PV to copy CSI volume info
	sourcePV, err := clientset.CoreV1().PersistentVolumes().Get(ctx, sourcePVName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get source PV %s: %w", sourcePVName, err)
	}
	if sourcePV.Spec.CSI == nil {
		return fmt.Errorf("source PV %s is not a CSI volume", sourcePVName)
	}

	capacity := sourcePV.Spec.Capacity
	if len(accessModes) == 0 {
		accessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}

	// Create new PV with same volume handle but different VolumeMode
	newPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: newPVName,
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:   capacity,
			VolumeMode: &targetMode,
			AccessModes: accessModes,
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              sourcePV.Spec.StorageClassName,
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:           sourcePV.Spec.CSI.Driver,
					VolumeHandle:     sourcePV.Spec.CSI.VolumeHandle,
					VolumeAttributes: sourcePV.Spec.CSI.VolumeAttributes,
					NodeStageSecretRef:   sourcePV.Spec.CSI.NodeStageSecretRef,
					NodePublishSecretRef: sourcePV.Spec.CSI.NodePublishSecretRef,
				},
			},
			// Pre-bind to the target PVC
			ClaimRef: &corev1.ObjectReference{
				Kind:      "PersistentVolumeClaim",
				Namespace: namespace,
				Name:      newPVCName,
			},
		},
	}

	_, err = clientset.CoreV1().PersistentVolumes().Create(ctx, newPV, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create rebound PV %s: %w", newPVName, err)
	}

	// Create PVC that binds to the new PV
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      newPVCName,
			Namespace: namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: accessModes,
			Resources: corev1.VolumeResourceRequirements{
				Requests: capacity,
			},
			VolumeMode: &targetMode,
			VolumeName: newPVName,
			StorageClassName: &sourcePV.Spec.StorageClassName,
		},
	}

	_, err = clientset.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create rebound PVC %s: %w", newPVCName, err)
	}

	return nil
}

// DeletePV deletes a PersistentVolume, ignoring NotFound.
func DeletePV(ctx context.Context, clientset kubernetes.Interface, name string) error {
	err := clientset.CoreV1().PersistentVolumes().Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete PV %s: %w", name, err)
	}
	return nil
}

// ExecInPod executes a command in a pod and returns stdout/stderr.
func ExecInPod(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, namespace, podName, container string, command []string) (string, string, error) {
	if container == "" {
		container = defaultContainerName
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
		"app=" + toolboxPodNameFragment,
		"app=" + toolboxPodNameFragment + ",app.kubernetes.io/part-of=rook-ceph-operator",
	}
	var listErrs []string
	for _, selector := range selectors {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		})
		if err != nil {
			listErrs = append(listErrs, fmt.Sprintf("label %q: %v", selector, err))
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
		if len(listErrs) > 0 {
			return nil, fmt.Errorf("failed to list pods (prior label errors: %s): %w", strings.Join(listErrs, "; "), err)
		}
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}
	for i := range allPods.Items {
		if strings.Contains(allPods.Items[i].Name, toolboxPodNameFragment) {
			return &allPods.Items[i], nil
		}
	}

	if len(listErrs) > 0 {
		return nil, fmt.Errorf("no Ceph toolbox pod found in namespace %s (label errors: %s)", namespace, strings.Join(listErrs, "; "))
	}
	return nil, fmt.Errorf("no Ceph toolbox pod found in namespace %s (tried labels and name matching)", namespace)
}
