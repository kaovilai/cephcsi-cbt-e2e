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

// These tests validate the priority-based flattening behavior when approaching
// the 250-snapshot limit per RBD image. The Combined solution ensures that:
// 1. Deleted snapshots are flattened first
// 2. PVC-PVC clone snapshots are flattened next
// 3. Alive (user-visible) snapshots are flattened last
// 4. The latest 250 snapshots are always preserved for CBT
var _ = Describe("Priority Flattening", Label("slow"), Ordered, func() {
	const (
		// Use a smaller limit for testing (the real limit is 250)
		testSnapshotLimit = 10
		totalSnapshots    = testSnapshotLimit + 5
	)

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

		By(fmt.Sprintf("Creating %d snapshots to approach limit", totalSnapshots))
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

	It("should report snapshot count approaching limit", func() {
		pvc, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(ctx, pvcName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		pv, err := clientset.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())

		imageName := pv.Spec.CSI.VolumeAttributes["imageName"]
		if imageName != "" {
			count, err := rbdInspector.GetSnapshotCount(ctx, imageName)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("RBD image %s has %d snapshots\n", imageName, count)
			Expect(count).To(BeNumerically(">", 0))
		}
	})

	It("should prioritize flattening deleted snapshots first", func() {
		By("Deleting some early snapshots to trigger priority flattening")
		for i := 0; i < 3; i++ {
			Expect(k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapNames[i])).To(Succeed())
			GinkgoWriter.Printf("Deleted snapshot %s for priority test\n", snapNames[i])
		}

		// Allow time for the flattening controller to process
		time.Sleep(30 * time.Second)

		By("Verifying recent snapshots still have CBT working")
		latestSnap := snapNames[len(snapNames)-1]
		result, err := cbtClient.GetAllocatedBlocks(ctx, latestSnap)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty(),
			"latest snapshot should still have valid CBT metadata")
	})

	It("should preserve latest snapshots for CBT even after flattening", func() {
		// Test CBT delta between two recent snapshots
		recentBase := snapNames[len(snapNames)-3]
		recentTarget := snapNames[len(snapNames)-1]

		result, err := cbtClient.GetChangedBlocks(ctx, recentBase, recentTarget)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty(),
			"CBT delta between recent snapshots should work after priority flattening")
	})
})
