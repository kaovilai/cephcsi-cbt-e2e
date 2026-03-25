package e2e_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cephcsi-cbt-e2e/pkg/data"
	k8sutil "github.com/cephcsi-cbt-e2e/pkg/k8s"
)

var _ = Describe("ROX PVC", Ordered, func() {
	var (
		ctx         context.Context
		pvcName     string
		podName     string
		snapName    string
		roxPVCName  string
		roxPodName  string
		roxPVC2Name string
		clonePVCName string
	)

	BeforeAll(func() {
		ctx = context.Background()
		pvcName = "rox-test-pvc"
		podName = "rox-test-pod"
		snapName = "rox-test-snap"
		roxPVCName = "rox-test-rox-pvc"
		roxPodName = "rox-test-rox-pod"
		roxPVC2Name = "rox-test-rox-pvc2"
		clonePVCName = "rox-test-clone-pvc"

		By("Creating source PVC and writing data")
		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name:         pvcName,
			Namespace:    testNamespace,
			StorageClass: storageClass,
			Size:         "1Gi",
			AccessModes:  []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, pvcName, 2*time.Minute)).To(Succeed())

		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name:       podName,
			Namespace:  testNamespace,
			PVCName:    pvcName,
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, podName, 2*time.Minute)).To(Succeed())

		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 0, 0xDD)).To(Succeed())

		// Delete pod before snapshotting (ensures no active I/O)
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())

		By("Creating snapshot")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snapName, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snapName, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		ctx := context.Background()
		_ = k8sutil.DeletePod(ctx, clientset, testNamespace, roxPodName)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, clonePVCName)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, roxPVC2Name)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, roxPVCName)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapName)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
	})

	It("should create a ROX PVC from snapshot in Bound state with ReadOnlyMany", func() {
		var err error
		_, err = k8sutil.CreateROXPVCFromSnapshot(ctx, clientset, roxPVCName, testNamespace, storageClass, snapName, "1Gi")
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, roxPVCName, 2*time.Minute)).To(Succeed())

		pvc, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(ctx, roxPVCName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadOnlyMany))
	})

	It("should mount ROX PVC read-only with correct data", func() {
		_, err := k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name:       roxPodName,
			Namespace:  testNamespace,
			PVCName:    roxPVCName,
			ReadOnly:   true,
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, roxPodName, 2*time.Minute)).To(Succeed())

		By("Verifying data can be read from ROX PVC")
		hash, err := data.ReadBlockHash(ctx, clientset, kubeConfig, testNamespace, roxPodName, 0, data.DefaultBlockSize)
		Expect(err).NotTo(HaveOccurred())
		Expect(hash).NotTo(BeEmpty(), "should be able to read data from ROX PVC")

		By("Verifying write fails on ROX PVC")
		err = data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, roxPodName, 0, 0xFF)
		Expect(err).To(HaveOccurred(), "write should fail on read-only volume")
	})

	It("should not flatten ROX PVC despite multiple ROX PVCs from same snapshot", func() {
		By("Creating a second ROX PVC from the same snapshot")
		_, err := k8sutil.CreateROXPVCFromSnapshot(ctx, clientset, roxPVC2Name, testNamespace, storageClass, snapName, "1Gi")
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, roxPVC2Name, 2*time.Minute)).To(Succeed())

		// CephCSI creates ROX PVCs as clones of an intermediate image created during
		// snapshotting. The ROX PVC's image should retain its parent chain (not be
		// flattened) since the clone depth is shallow.
		// Note: The source PVC was created from scratch and never had a parent,
		// so checking it for flattening would be meaningless.
		By("Verifying the ROX PVC's RBD image is NOT flattened")
		pvc, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(ctx, roxPVCName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc.Spec.VolumeName).NotTo(BeEmpty())

		pv, err := clientset.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(pv.Spec.CSI).NotTo(BeNil())

		imageName := pv.Spec.CSI.VolumeAttributes["imageName"]
		Expect(imageName).NotTo(BeEmpty(), "PV should have imageName attribute")
		flattened, err := rbdInspector.IsImageFlattened(ctx, imageName)
		Expect(err).NotTo(HaveOccurred())
		Expect(flattened).To(BeFalse(),
			fmt.Sprintf("ROX PVC image %s should NOT be flattened when multiple ROX PVCs reference the same snapshot", imageName))
	})

	It("should support PVC-PVC clone from ROX PVC", func() {
		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name:           clonePVCName,
			Namespace:      testNamespace,
			StorageClass:   storageClass,
			Size:           "1Gi",
			AccessModes:    []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PVCCloneSource: roxPVCName,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, clonePVCName, 2*time.Minute)).To(Succeed())
	})
})
