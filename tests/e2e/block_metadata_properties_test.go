package e2e_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/cephcsi-cbt-e2e/pkg/cbt"
	"github.com/cephcsi-cbt-e2e/pkg/data"
	k8sutil "github.com/cephcsi-cbt-e2e/pkg/k8s"
)

var _ = Describe("Block Metadata Properties", Ordered, func() {
	var (
		ctx             context.Context
		pvcName         string
		podName         string
		snap1Name       string
		snap2Name       string
		allocatedResult *cbt.MetadataResult
		deltaResult     *cbt.MetadataResult
	)

	BeforeAll(func() {
		ctx = context.Background()
		pvcName = "bmp-pvc"
		podName = "bmp-pod"
		snap1Name = "bmp-snap1"
		snap2Name = "bmp-snap2"

		By("Creating a 1Gi block PVC")
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

		By("Writing non-sequential blocks: 5 (0x55), 2 (0x22), 8 (0x88), 0 (0x00)")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 5, 0x55)).To(Succeed())
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 2, 0x22)).To(Succeed())
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 8, 0x88)).To(Succeed())
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 0, 0x00)).To(Succeed())

		By("Deleting pod before snapshot 1")
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())
		Expect(k8sutil.WaitForPodDeleted(ctx, clientset, testNamespace, podName, 2*time.Minute)).To(Succeed())

		By("Creating snapshot 1")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap1Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap1Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Creating a new pod to write more data")
		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name:       podName,
			Namespace:  testNamespace,
			PVCName:    pvcName,
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, podName, 2*time.Minute)).To(Succeed())

		By("Writing blocks 3 (0x33) and 7 (0x77)")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 3, 0x33)).To(Succeed())
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 7, 0x77)).To(Succeed())

		By("Deleting pod before snapshot 2")
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())

		By("Creating snapshot 2")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap2Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap2Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Pre-fetching allocated blocks for snap1")
		allocatedResult, err = cbtClient.GetAllocatedBlocks(ctx, snap1Name)
		Expect(err).NotTo(HaveOccurred())

		By("Pre-fetching delta blocks between snap1 and snap2")
		deltaResult, err = cbtClient.GetChangedBlocks(ctx, snap1Name, snap2Name)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		ctx := context.Background()
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap2Name)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap1Name)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
	})

	It("should return blocks in ascending order by ByteOffset", func() {
		Expect(allocatedResult.Blocks).NotTo(BeEmpty())
		Expect(allocatedResult.BlocksAreSorted()).To(BeTrue(), "allocated blocks not sorted by ByteOffset")

		Expect(deltaResult.Blocks).NotTo(BeEmpty())
		Expect(deltaResult.BlocksAreSorted()).To(BeTrue(), "delta blocks not sorted by ByteOffset")
	})

	It("should return non-overlapping block ranges", func() {
		Expect(allocatedResult.BlocksAreNonOverlapping()).To(BeTrue(), "allocated blocks have overlapping ranges")
		Expect(deltaResult.BlocksAreNonOverlapping()).To(BeTrue(), "delta blocks have overlapping ranges")
	})

	It("should report consistent VolumeCapacityBytes across calls", func() {
		Expect(allocatedResult.VolumeCapacityBytes).To(BeNumerically(">", 0))
		Expect(allocatedResult.VolumeCapacityBytes).To(BeNumerically(">=", int64(1073741824)),
			"VolumeCapacityBytes should be at least 1Gi (1073741824)")
		Expect(deltaResult.VolumeCapacityBytes).To(Equal(allocatedResult.VolumeCapacityBytes),
			"VolumeCapacityBytes should be consistent between allocated and delta calls")
	})

	It("should return 1MiB-aligned block offsets and sizes", func() {
		const alignment = int64(data.DefaultBlockSize) // 1 MiB
		for i, block := range allocatedResult.Blocks {
			Expect(block.ByteOffset % alignment).To(BeZero(),
				fmt.Sprintf("block[%d] ByteOffset %d not 1MiB-aligned", i, block.ByteOffset))
			Expect(block.SizeBytes % alignment).To(BeZero(),
				fmt.Sprintf("block[%d] SizeBytes %d not 1MiB-aligned", i, block.SizeBytes))
		}
	})

	It("should report FIXED_LENGTH BlockMetadataType", func() {
		Expect(allocatedResult.BlockMetadataType).To(Equal(cbt.BlockMetadataTypeAllocated),
			"CephCSI RBD should report FIXED_LENGTH BlockMetadataType")
	})

	It("should support StartingOffset for resumption", func() {
		// Pick a midpoint offset from the allocated blocks
		Expect(len(allocatedResult.Blocks)).To(BeNumerically(">=", 2),
			"need at least 2 blocks to test StartingOffset")

		midIndex := len(allocatedResult.Blocks) / 2
		midOffset := allocatedResult.Blocks[midIndex].ByteOffset

		By(fmt.Sprintf("Querying with StartingOffset=%d (block %d of %d)", midOffset, midIndex, len(allocatedResult.Blocks)))
		partialResult, err := cbtClient.GetAllocatedBlocksWithOptions(ctx, snap1Name, midOffset, 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(partialResult.Blocks).NotTo(BeEmpty())

		By("Verifying all returned blocks are at or after the starting offset")
		for i, block := range partialResult.Blocks {
			Expect(block.ByteOffset + block.SizeBytes).To(BeNumerically(">", midOffset),
				fmt.Sprintf("block[%d] at offset %d ends before StartingOffset %d", i, block.ByteOffset, midOffset))
		}

		By("Verifying partial result is a subset of full result")
		Expect(len(partialResult.Blocks)).To(BeNumerically("<=", len(allocatedResult.Blocks)),
			"partial result should have fewer or equal blocks than full result")
		Expect(len(partialResult.Blocks)).To(BeNumerically("<", len(allocatedResult.Blocks)),
			"partial result should have fewer blocks than full result since we skipped some")
	})

	It("should honor MaxResults parameter without error", func() {
		By("Querying with MaxResults=1")
		result, err := cbtClient.GetAllocatedBlocksWithOptions(ctx, snap1Name, 0, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty())

		By("Verifying result still contains all blocks (iterator collects entire stream)")
		Expect(len(result.Blocks)).To(Equal(len(allocatedResult.Blocks)),
			"MaxResults controls per-message batch size, not total result count")
		Expect(result.VolumeCapacityBytes).To(Equal(allocatedResult.VolumeCapacityBytes))
	})

	It("should handle volume not aligned to 1MB block size", func() {
		ctx := context.Background()
		oddPVCName := "bmp-odd-pvc"
		oddPodName := "bmp-odd-pod"
		oddSnapName := "bmp-odd-snap"

		By("Creating a 1500Mi PVC")
		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name:         oddPVCName,
			Namespace:    testNamespace,
			StorageClass: storageClass,
			Size:         "1500Mi",
			AccessModes:  []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, oddPVCName, 2*time.Minute)).To(Succeed())

		By("Creating a pod to write data")
		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name:       oddPodName,
			Namespace:  testNamespace,
			PVCName:    oddPVCName,
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, oddPodName, 2*time.Minute)).To(Succeed())

		By("Writing block 0 (0xDD)")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, oddPodName, 0, 0xDD)).To(Succeed())

		By("Deleting pod before snapshot")
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, oddPodName)).To(Succeed())

		By("Creating snapshot")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, oddSnapName, testNamespace, oddPVCName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, oddSnapName, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Getting allocated blocks")
		result, err := cbtClient.GetAllocatedBlocks(ctx, oddSnapName)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty())
		Expect(result.VolumeCapacityBytes).To(BeNumerically(">=", int64(1500*1024*1024)),
			"VolumeCapacityBytes should be at least 1500Mi")

		fmt.Fprintf(GinkgoWriter, "Odd-size volume: capacity=%d bytes, block count=%d\n",
			result.VolumeCapacityBytes, len(result.Blocks))

		By("Cleaning up odd-size resources")
		Expect(k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, oddSnapName)).To(Succeed())
		Expect(k8sutil.DeletePVC(ctx, clientset, testNamespace, oddPVCName)).To(Succeed())
	})
})
