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

// These tests validate CBT behavior when many snapshots exist for the same PVC.
// CephCSI uses a snap-clone architecture: each VolumeSnapshot creates a temporary
// snapshot on the source image, clones to a new intermediate image, deletes the
// temp snapshot, then creates the real snapshot on the intermediate image. This
// means the source PVC's RBD image accumulates NO direct RBD snapshots.
//
// The 250-snapshot limit (--maxsnapshotsonimage) applies per-image and is managed
// internally by CephCSI. These tests verify that CBT works correctly across many
// snapshots and after snapshot deletion.
var _ = Describe("Priority Flattening", Label("slow"), Ordered, func() {
	const totalSnapshots = 15

	var (
		ctx       context.Context
		pvcName   string
		podName   string
		snapNames []string
	)

	BeforeAll(func() {
		ctx = context.Background()
		pvcName = "priority-flatten-pvc"
		podName = "priority-flatten-pod"

		By("Creating PVC")
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

		By(fmt.Sprintf("Creating %d snapshots", totalSnapshots))
		snapNames = make([]string, totalSnapshots)
		for i := 0; i < totalSnapshots; i++ {
			snapNames[i] = fmt.Sprintf("priority-snap-%03d", i)

			// Write a unique block for each snapshot
			Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, i, byte(i+1))).To(Succeed())

			_, err = k8sutil.CreateSnapshot(ctx, snapClient, snapNames[i], testNamespace, pvcName, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snapNames[i], 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			GinkgoWriter.Printf("Created snapshot %d/%d: %s\n", i+1, totalSnapshots, snapNames[i])
		}

		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())
	})

	AfterAll(func() {
		ctx := context.Background()
		for _, name := range snapNames {
			_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, name)
		}
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
	})

	It("should have CBT working on all created snapshots", func() {
		// Verify GetMetadataAllocated works on the first, middle, and last snapshots
		for _, idx := range []int{0, totalSnapshots / 2, totalSnapshots - 1} {
			result, err := cbtClient.GetAllocatedBlocks(ctx, snapNames[idx])
			Expect(err).NotTo(HaveOccurred(), "GetAllocatedBlocks should work on snapshot %s", snapNames[idx])
			Expect(result.Blocks).NotTo(BeEmpty(),
				fmt.Sprintf("snapshot %s should have allocated blocks", snapNames[idx]))
			GinkgoWriter.Printf("Snapshot %s: %d allocated blocks\n", snapNames[idx], len(result.Blocks))
		}
	})

	It("should still have CBT working after deleting some snapshots", func() {
		By("Deleting some early snapshots")
		for i := 0; i < 3; i++ {
			Expect(k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapNames[i])).To(Succeed())
			GinkgoWriter.Printf("Deleted snapshot %s\n", snapNames[i])
		}

		// Allow time for any async processing
		time.Sleep(10 * time.Second)

		By("Verifying CBT still works on remaining snapshots")
		latestSnap := snapNames[len(snapNames)-1]
		result, err := cbtClient.GetAllocatedBlocks(ctx, latestSnap)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty(),
			"latest snapshot should still have valid CBT metadata after deleting earlier snapshots")
	})

	It("should preserve CBT metadata on recent snapshots", func() {
		// Verify multiple remaining snapshots still work
		for _, idx := range []int{totalSnapshots - 3, totalSnapshots - 2, totalSnapshots - 1} {
			result, err := cbtClient.GetAllocatedBlocks(ctx, snapNames[idx])
			Expect(err).NotTo(HaveOccurred(), "GetAllocatedBlocks should work on snapshot %s", snapNames[idx])
			Expect(result.Blocks).NotTo(BeEmpty(),
				fmt.Sprintf("snapshot %s should still have valid CBT metadata", snapNames[idx]))
		}
	})
})
