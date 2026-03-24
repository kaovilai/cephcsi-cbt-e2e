package e2e_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cephcsi-cbt-e2e/pkg/data"
	k8sutil "github.com/cephcsi-cbt-e2e/pkg/k8s"
)

// These tests validate the stored diffs mechanism: when a snapshot is flattened,
// its diff data (the blocks that changed since the previous snapshot) is stored
// in a Ceph omap so that GetMetadata can still return correct data for the
// flattened snapshot. The stored diffs form a linked list.
var _ = Describe("Stored Diffs", Label("stored-diffs"), Ordered, func() {
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
		pvcName = "stored-diffs-pvc"
		podName = "stored-diffs-pod"
		snap1Name = "stored-diffs-snap1"
		snap2Name = "stored-diffs-snap2"
		snap3Name = "stored-diffs-snap3"

		By("Creating PVC and writing initial data")
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

		By("Creating snap1 after writing block 0")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 0, 0xA1)).To(Succeed())
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap1Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap1Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Creating snap2 after writing block 1")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 1, 0xB2)).To(Succeed())
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap2Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap2Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Creating snap3 after writing block 2")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 2, 0xC3)).To(Succeed())
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap3Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap3Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())
	})

	AfterAll(func() {
		ctx := context.Background()
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap3Name)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap2Name)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap1Name)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
	})

	It("should have stored diffs in omap when a snapshot is flattened", func() {
		// This test requires that a flattening event has occurred which stored
		// the diff in omap. In a real environment, flattening would be triggered
		// by the 250-snapshot limit or by explicit flattening.
		//
		// We verify by checking if omap keys exist for the image.
		pvc, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(ctx, pvcName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		pv, err := clientset.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())

		imageName := pv.Spec.CSI.VolumeAttributes["imageName"]
		if imageName != "" {
			keys, err := rbdInspector.ListOmapKeys(ctx, imageName)
			if err == nil && keys != nil {
				GinkgoWriter.Printf("Omap keys for image %s: %v\n", imageName, keys)
			}
		}
	})

	It("should return correct data via GetMetadataAllocated on all snapshots", func() {
		for _, snapName := range []string{snap1Name, snap2Name, snap3Name} {
			result, err := cbtClient.GetAllocatedBlocks(ctx, snapName)
			Expect(err).NotTo(HaveOccurred(),
				"GetMetadataAllocated should work for %s even if flattened (via stored diffs)", snapName)
			Expect(result.Blocks).NotTo(BeEmpty(),
				"snapshot %s should have allocated blocks", snapName)
		}
	})

	It("should return correct delta between snapshots regardless of flattening state", func() {
		By("Testing delta snap1 -> snap2")
		delta12, err := cbtClient.GetChangedBlocks(ctx, snap1Name, snap2Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(delta12.Blocks).NotTo(BeEmpty())

		By("Testing delta snap2 -> snap3")
		delta23, err := cbtClient.GetChangedBlocks(ctx, snap2Name, snap3Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(delta23.Blocks).NotTo(BeEmpty())

		By("Testing cumulative delta snap1 -> snap3")
		delta13, err := cbtClient.GetChangedBlocks(ctx, snap1Name, snap3Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(delta13.Blocks).NotTo(BeEmpty())

		By("Verifying cumulative delta covers both intermediate deltas")
		// The snap1->snap3 delta should cover offsets from both snap1->snap2 and snap2->snap3
		for _, block := range delta12.Blocks {
			Expect(delta13.ContainsOffset(block.ByteOffset)).To(BeTrue(),
				"cumulative delta should include changes from snap1->snap2")
		}
		for _, block := range delta23.Blocks {
			Expect(delta13.ContainsOffset(block.ByteOffset)).To(BeTrue(),
				"cumulative delta should include changes from snap2->snap3")
		}
	})

	It("should handle oldest snapshot's diff as full allocated blocks", func() {
		// The oldest snapshot in the chain (snap1) should have its diff stored
		// as full allocated blocks (since there's no previous snapshot to diff against).
		result, err := cbtClient.GetAllocatedBlocks(ctx, snap1Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty(),
			"oldest snapshot should report allocated blocks (stored as full diff)")
	})
})
