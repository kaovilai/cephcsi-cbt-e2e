package e2e_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/cephcsi-cbt-e2e/pkg/data"
	k8sutil "github.com/cephcsi-cbt-e2e/pkg/k8s"
)

var _ = Describe("Volume Resize", Ordered, func() {
	var (
		ctx       context.Context
		pvcName   string
		podName   string
		pod2Name  string
		snap1Name string
		snap2Name string
	)

	BeforeAll(func() {
		ctx = context.Background()
		pvcName = "resize-pvc"
		podName = "resize-pod"
		pod2Name = "resize-pod2"
		snap1Name = "resize-snap1"
		snap2Name = "resize-snap2"

		By("Creating a 1Gi PVC")
		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name:         pvcName,
			Namespace:    testNamespace,
			StorageClass: storageClass,
			Size:         "1Gi",
			AccessModes:  []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, pvcName, 2*time.Minute)).To(Succeed())

		By("Creating a pod to write data")
		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name:       podName,
			Namespace:  testNamespace,
			PVCName:    pvcName,
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, podName, 2*time.Minute)).To(Succeed())

		By("Writing known pattern to block 0")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 0, 0xAA)).To(Succeed())

		By("Deleting the pod before snapshot")
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())

		By("Creating snapshot 1")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap1Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap1Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Resizing PVC to 2Gi")
		Expect(k8sutil.ResizePVC(ctx, clientset, testNamespace, pvcName, "2Gi")).To(Succeed())

		By("Waiting for PVC resize to complete")
		Expect(k8sutil.WaitForPVCResized(ctx, clientset, testNamespace, pvcName, resource.MustParse("2Gi"), 5*time.Minute)).To(Succeed())

		By("Creating a second pod with the resized PVC")
		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name:       pod2Name,
			Namespace:  testNamespace,
			PVCName:    pvcName,
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, pod2Name, 2*time.Minute)).To(Succeed())

		By("Writing known pattern to block 1024 in the expanded region")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, pod2Name, 1024, 0xBB)).To(Succeed())

		By("Deleting the second pod before snapshot")
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, pod2Name)).To(Succeed())

		By("Creating snapshot 2")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap2Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap2Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		ctx := context.Background()
		// Clean up in reverse order
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap2Name)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap1Name)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
	})

	It("should report updated VolumeCapacityBytes after expansion", func() {
		result, err := cbtClient.GetAllocatedBlocks(ctx, snap2Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.VolumeCapacityBytes).To(BeNumerically(">=", 2*1024*1024*1024),
			"VolumeCapacityBytes should reflect expanded 2Gi size")
		GinkgoWriter.Printf("Expanded volume capacity: %d bytes\n", result.VolumeCapacityBytes)
	})

	It("should include blocks in expanded region in delta", func() {
		result, err := cbtClient.GetChangedBlocks(ctx, snap1Name, snap2Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty())

		expandedBlockOffset := int64(1024) * int64(data.DefaultBlockSize)
		Expect(result.ContainsOffset(expandedBlockOffset)).To(BeTrue(),
			"delta should include block written in expanded region")
		Expect(result.VolumeCapacityBytes).To(BeNumerically(">=", 2*1024*1024*1024),
			"delta VolumeCapacityBytes should reflect expanded size")
		GinkgoWriter.Printf("Delta blocks after resize: %d\n", len(result.Blocks))
	})

	It("should return correct allocated blocks for expanded volume", func() {
		result, err := cbtClient.GetAllocatedBlocks(ctx, snap2Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty())

		block0Offset := int64(0)
		block1024Offset := int64(1024) * int64(data.DefaultBlockSize)
		Expect(result.ContainsOffset(block0Offset)).To(BeTrue(),
			"should include original block 0")
		Expect(result.ContainsOffset(block1024Offset)).To(BeTrue(),
			"should include block in expanded region")
	})
})
