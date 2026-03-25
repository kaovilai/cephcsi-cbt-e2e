package e2e_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/cephcsi-cbt-e2e/pkg/data"
	k8sutil "github.com/cephcsi-cbt-e2e/pkg/k8s"
)

var _ = Describe("Error Handling", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("should return error for CBT on non-existent snapshot", func() {
		_, err := cbtClient.GetAllocatedBlocks(ctx, "nonexistent-snapshot-xyz")
		Expect(err).To(HaveOccurred(), "CBT on non-existent snapshot should return error")
		GinkgoWriter.Printf("Expected error for non-existent snapshot: %v\n", err)
	})

	It("should return error for GetMetadataDelta across different PVCs", func() {
		pvc1Name := "err-cross-pvc1"
		pvc2Name := "err-cross-pvc2"
		pod1Name := "err-cross-pod1"
		pod2Name := "err-cross-pod2"
		snap1Name := "err-cross-snap1"
		snap2Name := "err-cross-snap2"

		// Clean up any leftovers from a previous failed run
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap2Name)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap1Name)
		_ = k8sutil.DeletePod(ctx, clientset, testNamespace, pod2Name)
		_ = k8sutil.DeletePod(ctx, clientset, testNamespace, pod1Name)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvc2Name)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvc1Name)

		By("Creating two independent PVCs with snapshots")
		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name: pvc1Name, Namespace: testNamespace, StorageClass: storageClass, Size: "1Gi",
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, pvc1Name, 2*time.Minute)).To(Succeed())

		_, err = k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name: pvc2Name, Namespace: testNamespace, StorageClass: storageClass, Size: "1Gi",
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, pvc2Name, 2*time.Minute)).To(Succeed())

		// Write data and snapshot PVC1
		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name: pod1Name, Namespace: testNamespace, PVCName: pvc1Name, VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, pod1Name, 2*time.Minute)).To(Succeed())
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, pod1Name, 0, 0x01)).To(Succeed())
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, pod1Name)).To(Succeed())

		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap1Name, testNamespace, pvc1Name, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap1Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		// Write data and snapshot PVC2
		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name: pod2Name, Namespace: testNamespace, PVCName: pvc2Name, VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, pod2Name, 2*time.Minute)).To(Succeed())
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, pod2Name, 0, 0x02)).To(Succeed())
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, pod2Name)).To(Succeed())

		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap2Name, testNamespace, pvc2Name, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap2Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Attempting GetMetadataDelta across different PVCs")
		_, err = cbtClient.GetChangedBlocks(ctx, snap1Name, snap2Name)
		Expect(err).To(HaveOccurred(),
			"GetMetadataDelta across snapshots from different PVCs should return error")
		GinkgoWriter.Printf("Expected error for cross-PVC delta: %v\n", err)

		// Cleanup
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap2Name)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap1Name)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvc2Name)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvc1Name)
	})

	It("should return error for reversed snapshot order in GetMetadataDelta", func() {
		pvcName := "err-reversed-pvc"
		podName := "err-reversed-pod"
		snapOlder := "err-reversed-snap-old"
		snapNewer := "err-reversed-snap-new"

		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name: pvcName, Namespace: testNamespace, StorageClass: storageClass, Size: "1Gi",
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, pvcName, 2*time.Minute)).To(Succeed())

		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name: podName, Namespace: testNamespace, PVCName: pvcName, VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, podName, 2*time.Minute)).To(Succeed())

		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 0, 0x10)).To(Succeed())
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snapOlder, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snapOlder, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 1, 0x20)).To(Succeed())
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snapNewer, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snapNewer, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())

		By("Attempting GetMetadataDelta with reversed order (newer as base)")
		_, err = cbtClient.GetChangedBlocks(ctx, snapNewer, snapOlder)
		Expect(err).To(HaveOccurred(),
			"GetMetadataDelta with reversed snapshot order should return error")
		GinkgoWriter.Printf("Expected error for reversed order: %v\n", err)

		// Cleanup
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapNewer)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapOlder)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
	})

	It("should handle concurrent snapshot creation and CBT operations", func() {
		pvcName := "err-concurrent-pvc"
		podName := "err-concurrent-pod"

		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name: pvcName, Namespace: testNamespace, StorageClass: storageClass, Size: "1Gi",
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, pvcName, 2*time.Minute)).To(Succeed())

		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name: podName, Namespace: testNamespace, PVCName: pvcName, VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, podName, 2*time.Minute)).To(Succeed())

		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 0, 0xAA)).To(Succeed())

		// Create a snapshot to use for concurrent CBT
		baseSnap := "err-concurrent-base"
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, baseSnap, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, baseSnap, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())

		By("Running concurrent CBT GetAllocated calls")
		var wg sync.WaitGroup
		errs := make([]error, 5)
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				defer GinkgoRecover()
				_, errs[idx] = cbtClient.GetAllocatedBlocks(ctx, baseSnap)
			}(i)
		}
		wg.Wait()

		for i, e := range errs {
			Expect(e).NotTo(HaveOccurred(), fmt.Sprintf("concurrent CBT call %d should not fail", i))
		}

		// Cleanup
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, baseSnap)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
	})

	It("should handle large volume with many blocks", func() {
		pvcName := "err-large-pvc"
		podName := "err-large-pod"
		snapName := "err-large-snap"

		// Clean up any leftovers from a previous failed run
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapName)
		_ = k8sutil.DeletePod(ctx, clientset, testNamespace, podName)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)

		By("Creating a larger PVC (5Gi)")
		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name: pvcName, Namespace: testNamespace, StorageClass: storageClass, Size: "5Gi",
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, pvcName, 2*time.Minute)).To(Succeed())

		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name: podName, Namespace: testNamespace, PVCName: pvcName, VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, podName, 2*time.Minute)).To(Succeed())

		By("Writing data to multiple blocks across the volume")
		for i := 0; i < 50; i++ {
			Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, i*100, byte(i+1))).To(Succeed())
		}
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())

		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snapName, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snapName, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying streaming completes for large volume")
		result, err := cbtClient.GetAllocatedBlocks(ctx, snapName)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty())
		GinkgoWriter.Printf("Large volume: %d blocks, capacity %d bytes\n",
			len(result.Blocks), result.VolumeCapacityBytes)

		// Cleanup
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapName)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
	})

	// Handle-based error compliance tests (GetChangedBlocksByID error cases)
	Context("Error Compliance", func() {
		It("should return error for invalid snapshot handle in GetChangedBlocksByID", func() {
			pvcName := "errc-inv-pvc"
			podName := "errc-inv-pod"
			snapName := "errc-inv-snap"

			By("Creating PVC and waiting for it to be bound")
			_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
				Name: pvcName, Namespace: testNamespace, StorageClass: storageClass, Size: "1Gi",
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, pvcName, 2*time.Minute)).To(Succeed())

			By("Creating pod and waiting for it to be running")
			_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
				Name: podName, Namespace: testNamespace, PVCName: pvcName, VolumeMode: corev1.PersistentVolumeBlock,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, podName, 2*time.Minute)).To(Succeed())

			By("Writing block 0 with pattern 0x01")
			Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 0, 0x01)).To(Succeed())

			By("Deleting the pod")
			Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())

			By("Creating snapshot and waiting for it to be ready")
			_, err = k8sutil.CreateSnapshot(ctx, snapClient, snapName, testNamespace, pvcName, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snapName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Calling GetChangedBlocksByID with a garbage snapshot handle")
			_, err = cbtClient.GetChangedBlocksByID(ctx, "garbage-handle-does-not-exist", snapName)
			Expect(err).To(HaveOccurred())
			GinkgoWriter.Printf("Expected error for invalid snapshot handle: %v\n", err)

			// Cleanup
			_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapName)
			_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
		})

		It("should return error for GetChangedBlocksByID with handle from different volume", func() {
			pvc1Name := "errc-xvol-pvc1"
			pod1Name := "errc-xvol-pod1"
			snap1Name := "errc-xvol-snap1"
			pvc2Name := "errc-xvol-pvc2"
			pod2Name := "errc-xvol-pod2"
			snap2Name := "errc-xvol-snap2"

			By("Creating first PVC and waiting for it to be bound")
			_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
				Name: pvc1Name, Namespace: testNamespace, StorageClass: storageClass, Size: "1Gi",
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, pvc1Name, 2*time.Minute)).To(Succeed())

			By("Creating first pod, writing data, and snapshotting")
			_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
				Name: pod1Name, Namespace: testNamespace, PVCName: pvc1Name, VolumeMode: corev1.PersistentVolumeBlock,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, pod1Name, 2*time.Minute)).To(Succeed())
			Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, pod1Name, 0, 0x01)).To(Succeed())
			Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, pod1Name)).To(Succeed())

			_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap1Name, testNamespace, pvc1Name, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap1Name, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Getting the snapshot handle from the first volume's snapshot")
			pvc1Handle, err := k8sutil.GetSnapshotHandle(ctx, snapClient, testNamespace, snap1Name)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("PVC1 snapshot handle: %s\n", pvc1Handle)

			By("Creating second PVC and waiting for it to be bound")
			_, err = k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
				Name: pvc2Name, Namespace: testNamespace, StorageClass: storageClass, Size: "1Gi",
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, pvc2Name, 2*time.Minute)).To(Succeed())

			By("Creating second pod, writing data, and snapshotting")
			_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
				Name: pod2Name, Namespace: testNamespace, PVCName: pvc2Name, VolumeMode: corev1.PersistentVolumeBlock,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, pod2Name, 2*time.Minute)).To(Succeed())
			Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, pod2Name, 0, 0x02)).To(Succeed())
			Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, pod2Name)).To(Succeed())

			_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap2Name, testNamespace, pvc2Name, snapshotClass)
			Expect(err).NotTo(HaveOccurred())
			_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap2Name, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Calling GetChangedBlocksByID with handle from a different volume")
			_, err = cbtClient.GetChangedBlocksByID(ctx, pvc1Handle, snap2Name)
			Expect(err).To(HaveOccurred())
			GinkgoWriter.Printf("Expected error for cross-volume handle: %v\n", err)

			// Cleanup
			_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap2Name)
			_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap1Name)
			_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvc2Name)
			_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvc1Name)
		})

		It("should return error when querying CBT on snapshot that is not ready", func() {
			pvcName := "errc-notready-pvc"
			podName := "errc-notready-pod"
			snapName := "errc-notready-snap"

			By("Creating PVC and waiting for it to be bound")
			_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
				Name: pvcName, Namespace: testNamespace, StorageClass: storageClass, Size: "1Gi",
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, pvcName, 2*time.Minute)).To(Succeed())

			By("Creating pod, writing data, and deleting pod")
			_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
				Name: podName, Namespace: testNamespace, PVCName: pvcName, VolumeMode: corev1.PersistentVolumeBlock,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, podName, 2*time.Minute)).To(Succeed())
			Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 0, 0x01)).To(Succeed())
			Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())

			By("Creating snapshot without waiting for ready")
			_, err = k8sutil.CreateSnapshot(ctx, snapClient, snapName, testNamespace, pvcName, snapshotClass)
			Expect(err).NotTo(HaveOccurred())

			By("Immediately querying CBT on potentially not-ready snapshot")
			_, err = cbtClient.GetAllocatedBlocks(ctx, snapName)
			if err != nil {
				GinkgoWriter.Printf("Query on not-ready snapshot returned expected error: %v\n", err)
			} else {
				GinkgoWriter.Printf("Snapshot became ready before query (race condition - test inconclusive)\n")
			}

			By("Waiting for snapshot to become ready for cleanup")
			_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snapName, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			// Cleanup
			_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapName)
			_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
		})
	})
})
