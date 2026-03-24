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

var _ = Describe("Flattening Prevention", func() {
	Context("PVC -> Snap -> Restore -> Snap chain", Ordered, func() {
		var (
			ctx           context.Context
			origPVCName   string
			origPodName   string
			snap1Name     string
			restoredPVC   string
			restoredPod   string
			snap2Name     string
		)

		BeforeAll(func() {
			ctx = context.Background()
			origPVCName = "flatten-prev-orig-pvc"
			origPodName = "flatten-prev-orig-pod"
			snap1Name = "flatten-prev-snap1"
			restoredPVC = "flatten-prev-restored-pvc"
			restoredPod = "flatten-prev-restored-pod"
			snap2Name = "flatten-prev-snap2"

			By("Creating original PVC and writing data")
			_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
				Name:         origPVCName,
				Namespace:    testNamespace,
				StorageClass: storageClass,
				Size:         "1Gi",
				AccessModes:  []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, origPVCName, 2*time.Minute)).To(Succeed())

			_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
				Name:       origPodName,
				Namespace:  testNamespace,
				PVCName:    origPVCName,
				VolumeMode: corev1.PersistentVolumeBlock,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, origPodName, 2*time.Minute)).To(Succeed())

			Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, origPodName, 0, 0x11)).To(Succeed())
			Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, origPodName)).To(Succeed())

			By("Creating first snapshot")
			_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap1Name, testNamespace, origPVCName, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap1Name, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Restoring PVC from snapshot")
			_, err = k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
				Name:           restoredPVC,
				Namespace:      testNamespace,
				StorageClass:   storageClass,
				Size:           "1Gi",
				AccessModes:    []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				SnapshotSource: snap1Name,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, restoredPVC, 2*time.Minute)).To(Succeed())

			By("Writing additional data to restored PVC")
			_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
				Name:       restoredPod,
				Namespace:  testNamespace,
				PVCName:    restoredPVC,
				VolumeMode: corev1.PersistentVolumeBlock,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, restoredPod, 2*time.Minute)).To(Succeed())

			Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, restoredPod, 1, 0x22)).To(Succeed())
			Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, restoredPod)).To(Succeed())

			By("Creating snapshot of restored PVC")
			_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap2Name, testNamespace, restoredPVC, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap2Name, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			ctx := context.Background()
			_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap2Name)
			_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, restoredPVC)
			_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap1Name)
			_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, origPVCName)
		})

		It("should NOT flatten snap1 after restore and re-snapshot", func() {
			By("Verifying snap1's RBD snapshot still has parent chain intact")
			pvc, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(ctx, origPVCName, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			pv, err := clientset.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			imageName := pv.Spec.CSI.VolumeAttributes["imageName"]
			if imageName != "" {
				flattened, err := rbdInspector.IsImageFlattened(ctx, imageName)
				Expect(err).NotTo(HaveOccurred())
				Expect(flattened).To(BeFalse(),
					fmt.Sprintf("original image %s should NOT be flattened in PVC->Snap->Restore->Snap chain", imageName))
			}
		})

		It("should have CBT working across the chain", func() {
			By("Verifying GetMetadataAllocated on snap1")
			result1, err := cbtClient.GetAllocatedBlocks(ctx, snap1Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(result1.Blocks).NotTo(BeEmpty())

			By("Verifying GetMetadataAllocated on snap2")
			result2, err := cbtClient.GetAllocatedBlocks(ctx, snap2Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(result2.Blocks).NotTo(BeEmpty())
		})
	})

	Context("PVC -> PVC clone -> Snap", Ordered, func() {
		var (
			ctx          context.Context
			origPVCName  string
			origPodName  string
			clonePVCName string
			clonePodName string
			snapName     string
		)

		BeforeAll(func() {
			ctx = context.Background()
			origPVCName = "flatten-clone-orig-pvc"
			origPodName = "flatten-clone-orig-pod"
			clonePVCName = "flatten-clone-clone-pvc"
			clonePodName = "flatten-clone-clone-pod"
			snapName = "flatten-clone-snap"

			By("Creating original PVC with data")
			_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
				Name:         origPVCName,
				Namespace:    testNamespace,
				StorageClass: storageClass,
				Size:         "1Gi",
				AccessModes:  []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, origPVCName, 2*time.Minute)).To(Succeed())

			_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
				Name:       origPodName,
				Namespace:  testNamespace,
				PVCName:    origPVCName,
				VolumeMode: corev1.PersistentVolumeBlock,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, origPodName, 2*time.Minute)).To(Succeed())

			Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, origPodName, 0, 0x33)).To(Succeed())
			Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, origPodName)).To(Succeed())

			By("Creating PVC-PVC clone")
			_, err = k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
				Name:           clonePVCName,
				Namespace:      testNamespace,
				StorageClass:   storageClass,
				Size:           "1Gi",
				AccessModes:    []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				PVCCloneSource: origPVCName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, clonePVCName, 2*time.Minute)).To(Succeed())

			By("Writing data to clone")
			_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
				Name:       clonePodName,
				Namespace:  testNamespace,
				PVCName:    clonePVCName,
				VolumeMode: corev1.PersistentVolumeBlock,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, clonePodName, 2*time.Minute)).To(Succeed())

			Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, clonePodName, 1, 0x44)).To(Succeed())
			Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, clonePodName)).To(Succeed())

			By("Creating snapshot of clone")
			_, err = k8sutil.CreateSnapshot(ctx, snapClient, snapName, testNamespace, clonePVCName, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snapName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			ctx := context.Background()
			_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapName)
			_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, clonePVCName)
			_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, origPVCName)
		})

		It("should NOT flatten original PVC after clone and snapshot", func() {
			pvc, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(ctx, origPVCName, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			pv, err := clientset.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			imageName := pv.Spec.CSI.VolumeAttributes["imageName"]
			if imageName != "" {
				flattened, err := rbdInspector.IsImageFlattened(ctx, imageName)
				Expect(err).NotTo(HaveOccurred())
				Expect(flattened).To(BeFalse(),
					fmt.Sprintf("original image %s should NOT be flattened after PVC clone and snapshot", imageName))
			}
		})

		It("should have CBT working on the clone's snapshot", func() {
			result, err := cbtClient.GetAllocatedBlocks(ctx, snapName)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Blocks).NotTo(BeEmpty())
		})
	})
})
