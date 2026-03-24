package e2e_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/cephcsi-cbt-e2e/pkg/data"
	k8sutil "github.com/cephcsi-cbt-e2e/pkg/k8s"
)

var _ = Describe("Basic CBT", Ordered, func() {
	var (
		ctx       context.Context
		pvcName   string
		podName   string
		snap1Name string
		snap2Name string
		snap3Name string
	)

	BeforeAll(func() {
		ctx = context.Background()
		pvcName = "basic-cbt-pvc"
		podName = "basic-cbt-pod"
		snap1Name = "basic-cbt-snap1"
		snap2Name = "basic-cbt-snap2"
		snap3Name = "basic-cbt-snap3"

		By("Creating a PVC")
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

		By("Creating snapshot 1")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap1Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap1Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Writing known pattern to block 1")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 1, 0xBB)).To(Succeed())

		By("Creating snapshot 2")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap2Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap2Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Writing known pattern to block 2")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 2, 0xCC)).To(Succeed())

		By("Creating snapshot 3")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap3Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap3Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		ctx := context.Background()
		// Clean up in reverse order
		_ = k8sutil.DeletePod(ctx, clientset, testNamespace, podName)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap3Name)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap2Name)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap1Name)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
	})

	It("should return allocated blocks for a single snapshot via GetMetadataAllocated", func() {
		result, err := cbtClient.GetAllocatedBlocks(ctx, snap1Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty(), "expected at least one allocated block in snapshot 1")
		Expect(result.VolumeCapacityBytes).To(BeNumerically(">", 0))

		By("Verifying block 0 offset is covered")
		Expect(result.ContainsOffset(0)).To(BeTrue(),
			"block 0 was written but not reported in allocated blocks")
	})

	It("should return changed blocks between consecutive snapshots via GetMetadataDelta", func() {
		result, err := cbtClient.GetChangedBlocks(ctx, snap1Name, snap2Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty(), "expected changed blocks between snap1 and snap2")

		By("Verifying block 1 is in the delta (written between snap1 and snap2)")
		block1Offset := int64(1) * data.DefaultBlockSize
		Expect(result.ContainsOffset(block1Offset)).To(BeTrue(),
			fmt.Sprintf("block 1 (offset %d) was written between snap1 and snap2 but not in delta", block1Offset))

		By("Verifying block 0 is NOT in the delta (unchanged between snap1 and snap2)")
		Expect(result.ContainsOffset(0)).To(BeFalse(),
			"block 0 was NOT modified between snap1 and snap2 but was reported in delta")
	})

	It("should return cumulative changes between non-consecutive snapshots", func() {
		result, err := cbtClient.GetChangedBlocks(ctx, snap1Name, snap3Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty(), "expected changed blocks between snap1 and snap3")

		By("Verifying blocks 1 and 2 are both in the delta")
		block1Offset := int64(1) * data.DefaultBlockSize
		block2Offset := int64(2) * data.DefaultBlockSize
		Expect(result.ContainsOffset(block1Offset)).To(BeTrue(),
			"block 1 should be in cumulative delta snap1->snap3")
		Expect(result.ContainsOffset(block2Offset)).To(BeTrue(),
			"block 2 should be in cumulative delta snap1->snap3")
	})

	It("should report accurate metadata matching written data", func() {
		result, err := cbtClient.GetAllocatedBlocks(ctx, snap3Name)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying all three written blocks are allocated")
		for i := 0; i < 3; i++ {
			offset := int64(i) * data.DefaultBlockSize
			Expect(result.ContainsOffset(offset)).To(BeTrue(),
				fmt.Sprintf("block %d (offset %d) was written but not reported as allocated", i, offset))
		}
	})
})
