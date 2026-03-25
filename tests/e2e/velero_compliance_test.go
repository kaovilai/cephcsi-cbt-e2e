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

// These tests validate handle-based delta operations that Velero's Block Data
// Mover actually uses. Velero stores CSI snapshot handles (not VolumeSnapshot names)
// and uses GetChangedBlocksByID to compute deltas for incremental backups.
var _ = Describe("Velero Compliance", Ordered, func() {
	var (
		ctx       context.Context
		pvcName   string
		podName   string
		snap1Name string
		snap2Name string
		snap3Name string

		snap1Handle  string
		snap2Handle  string
		hashesAtSnap3 map[int]string
	)

	BeforeAll(func() {
		ctx = context.Background()
		pvcName = "velero-pvc"
		podName = "velero-pod"
		snap1Name = "velero-snap1"
		snap2Name = "velero-snap2"
		snap3Name = "velero-snap3"
		hashesAtSnap3 = make(map[int]string)

		By("Creating PVC and pod")
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

		By("Writing block 0 (0xAA) and creating snapshot 1")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 0, 0xAA)).To(Succeed())

		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap1Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap1Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Getting snapshot 1 handle")
		snap1Handle, err = k8sutil.GetSnapshotHandle(ctx, snapClient, testNamespace, snap1Name)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("snap1Handle: %s\n", snap1Handle)

		By("Writing block 1 (0xBB) and creating snapshot 2")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 1, 0xBB)).To(Succeed())

		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap2Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap2Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Getting snapshot 2 handle")
		snap2Handle, err = k8sutil.GetSnapshotHandle(ctx, snapClient, testNamespace, snap2Name)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("snap2Handle: %s\n", snap2Handle)

		By("Writing block 2 (0xCC) and recording hashes")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 2, 0xCC)).To(Succeed())

		for i := 0; i < 3; i++ {
			h, err := data.ReadBlockHash(ctx, clientset, kubeConfig, testNamespace, podName, int64(i)*data.DefaultBlockSize, data.DefaultBlockSize)
			Expect(err).NotTo(HaveOccurred())
			hashesAtSnap3[i] = h
		}

		By("Creating snapshot 3")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap3Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap3Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Deleting pod")
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())
	})

	AfterAll(func() {
		ctx := context.Background()
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap3Name)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap2Name)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap1Name)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
	})

	It("should return changed blocks using snapshot handle ID (GetChangedBlocksByID)", func() {
		By("Calling GetChangedBlocksByID with snap1Handle and snap2Name")
		result, err := cbtClient.GetChangedBlocksByID(ctx, snap1Handle, snap2Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty())

		By("Verifying block 1 is in the delta result")
		block1Offset := int64(1) * data.DefaultBlockSize
		Expect(result.ContainsOffset(block1Offset)).To(BeTrue(),
			"delta should include block 1 which was written between snap1 and snap2")

		By("Verifying block 0 is NOT in the delta result")
		block0Offset := int64(0) * data.DefaultBlockSize
		Expect(result.ContainsOffset(block0Offset)).To(BeFalse(),
			"delta should NOT include block 0 which was unchanged between snap1 and snap2")

		GinkgoWriter.Printf("Handle-based delta: %d blocks, %d bytes\n",
			len(result.Blocks), result.TotalChangedBytes())
	})

	// Velero retention Case 1: no snapshot retention (delete previous snapshot after backup).
	// Ceph RBD requires both snapshots to exist in the clone chain for `rbd snap diff`
	// to compute a delta. When the parent snapshot is fully deleted (VolumeSnapshot +
	// VolumeSnapshotContent + underlying RBD snapshot), delta computation must fail.
	// This locks in that Velero's Case 1 retention strategy does NOT work with CephCSI
	// and users must use Case 2 (retain previous snapshot).
	// Ref: https://github.com/Lyndon-Li/velero/blob/block-data-mover-design/design/block-data-mover/block-data-mover.md#volume-snapshot-retention
	It("should fail delta when parent snapshot is deleted (Case 1 no-retention does not work with Ceph)", func() {
		By("Creating separate PVC and pod for deletion test")
		delPVC := "velero-del-pvc"
		delPod := "velero-del-pod"
		delSnap1 := "velero-del-snap1"
		delSnap2 := "velero-del-snap2"

		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name:         delPVC,
			Namespace:    testNamespace,
			StorageClass: storageClass,
			Size:         "1Gi",
			AccessModes:  []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, delPVC, 2*time.Minute)).To(Succeed())

		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name:       delPod,
			Namespace:  testNamespace,
			PVCName:    delPVC,
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, delPod, 2*time.Minute)).To(Succeed())

		By("Writing block 0 (0x11) and creating first snapshot (parent)")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, delPod, 0, 0x11)).To(Succeed())

		_, err = k8sutil.CreateSnapshot(ctx, snapClient, delSnap1, testNamespace, delPVC, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, delSnap1, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		deletedHandle, err := k8sutil.GetSnapshotHandle(ctx, snapClient, testNamespace, delSnap1)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Parent snapshot handle (will be deleted): %s\n", deletedHandle)

		// Record the VolumeSnapshotContent name so we can verify it's fully deleted
		vscName, err := k8sutil.GetSnapshotContentName(ctx, snapClient, testNamespace, delSnap1)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Parent VolumeSnapshotContent: %s\n", vscName)

		By("Writing block 1 (0x22), deleting pod, and creating second snapshot (target)")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, delPod, 1, 0x22)).To(Succeed())
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, delPod)).To(Succeed())

		_, err = k8sutil.CreateSnapshot(ctx, snapClient, delSnap2, testNamespace, delPVC, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, delSnap2, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Deleting parent VolumeSnapshot (simulating Case 1: no retention after backup)")
		Expect(k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, delSnap1)).To(Succeed())
		Expect(k8sutil.WaitForSnapshotDeleted(ctx, snapClient, testNamespace, delSnap1, 3*time.Minute)).To(Succeed())

		By("Waiting for VolumeSnapshotContent to be fully deleted (confirms RBD snapshot cleanup)")
		Expect(k8sutil.WaitForSnapshotContentDeleted(ctx, snapClient, vscName, 5*time.Minute)).To(Succeed(),
			"VolumeSnapshotContent %s should be deleted along with the underlying RBD snapshot", vscName)

		By("Attempting delta with deleted parent handle — must fail for Ceph RBD")
		_, err = cbtClient.GetChangedBlocksByID(ctx, deletedHandle, delSnap2)
		Expect(err).To(HaveOccurred(),
			"Ceph RBD requires both snapshots in the clone chain for delta computation; "+
				"Case 1 (no-retention) must not work — Velero users must use Case 2 (retain previous snapshot)")
		GinkgoWriter.Printf("Confirmed: Case 1 fails with Ceph as expected: %v\n", err)

		By("Cleaning up deletion test resources")
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, delSnap2)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, delPVC)
	})

	It("should return consistent results between name-based and handle-based delta", func() {
		By("Getting delta via name-based API")
		nameResult, err := cbtClient.GetChangedBlocks(ctx, snap1Name, snap2Name)
		Expect(err).NotTo(HaveOccurred())

		By("Getting delta via handle-based API")
		handleResult, err := cbtClient.GetChangedBlocksByID(ctx, snap1Handle, snap2Name)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying both results have the same number of blocks")
		Expect(len(handleResult.Blocks)).To(Equal(len(nameResult.Blocks)),
			"handle-based and name-based delta should return the same number of blocks")

		By("Verifying both results have the same VolumeCapacityBytes")
		Expect(handleResult.VolumeCapacityBytes).To(Equal(nameResult.VolumeCapacityBytes),
			"handle-based and name-based delta should report the same volume capacity")

		By("Verifying each block has matching ByteOffset and SizeBytes")
		for i := range nameResult.Blocks {
			Expect(handleResult.Blocks[i].ByteOffset).To(Equal(nameResult.Blocks[i].ByteOffset),
				fmt.Sprintf("block %d ByteOffset mismatch", i))
			Expect(handleResult.Blocks[i].SizeBytes).To(Equal(nameResult.Blocks[i].SizeBytes),
				fmt.Sprintf("block %d SizeBytes mismatch", i))
		}

		GinkgoWriter.Printf("Consistency check passed: %d blocks match between name-based and handle-based delta\n",
			len(nameResult.Blocks))
	})

	It("should simulate Velero incremental backup chain with handle-based delta", func() {
		By("Full backup: GetMetadataAllocated for snap1")
		fullResult, err := cbtClient.GetAllocatedBlocks(ctx, snap1Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(fullResult.Blocks).NotTo(BeEmpty())

		block0Offset := int64(0) * data.DefaultBlockSize
		Expect(fullResult.ContainsOffset(block0Offset)).To(BeTrue(),
			"full backup should include block 0")

		GinkgoWriter.Printf("Full backup: %d blocks, %d bytes\n",
			len(fullResult.Blocks), fullResult.TotalChangedBytes())

		By("Incremental 1: GetChangedBlocksByID snap1Handle -> snap2")
		incr1Result, err := cbtClient.GetChangedBlocksByID(ctx, snap1Handle, snap2Name)
		Expect(err).NotTo(HaveOccurred())

		block1Offset := int64(1) * data.DefaultBlockSize
		Expect(incr1Result.ContainsOffset(block1Offset)).To(BeTrue(),
			"incr1 should include block 1 (written between snap1 and snap2)")
		Expect(incr1Result.ContainsOffset(block0Offset)).To(BeFalse(),
			"incr1 should NOT include block 0 (unchanged between snap1 and snap2)")

		GinkgoWriter.Printf("Incremental 1: %d blocks, %d bytes\n",
			len(incr1Result.Blocks), incr1Result.TotalChangedBytes())

		By("Incremental 2: GetChangedBlocksByID snap2Handle -> snap3")
		incr2Result, err := cbtClient.GetChangedBlocksByID(ctx, snap2Handle, snap3Name)
		Expect(err).NotTo(HaveOccurred())

		block2Offset := int64(2) * data.DefaultBlockSize
		Expect(incr2Result.ContainsOffset(block2Offset)).To(BeTrue(),
			"incr2 should include block 2 (written between snap2 and snap3)")
		Expect(incr2Result.ContainsOffset(block0Offset)).To(BeFalse(),
			"incr2 should NOT include block 0 (unchanged between snap2 and snap3)")
		Expect(incr2Result.ContainsOffset(block1Offset)).To(BeFalse(),
			"incr2 should NOT include block 1 (unchanged between snap2 and snap3)")

		GinkgoWriter.Printf("Incremental 2: %d blocks, %d bytes\n",
			len(incr2Result.Blocks), incr2Result.TotalChangedBytes())

		By("Restore: creating PVC from snap3")
		restorePVC := "velero-restore-pvc"
		restorePod := "velero-restore-pod"

		_, err = k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name:           restorePVC,
			Namespace:      testNamespace,
			StorageClass:   storageClass,
			Size:           "1Gi",
			AccessModes:    []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			SnapshotSource: snap3Name,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, restorePVC, 2*time.Minute)).To(Succeed())

		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name:       restorePod,
			Namespace:  testNamespace,
			PVCName:    restorePVC,
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, restorePod, 2*time.Minute)).To(Succeed())

		By("Verifying restored data matches snap3 state")
		for i := 0; i < 3; i++ {
			hash, err := data.ReadBlockHash(ctx, clientset, kubeConfig, testNamespace, restorePod, int64(i)*data.DefaultBlockSize, data.DefaultBlockSize)
			Expect(err).NotTo(HaveOccurred())
			Expect(hash).To(Equal(hashesAtSnap3[i]),
				fmt.Sprintf("restored block %d should match snap3 state", i))
		}

		GinkgoWriter.Printf("Restore verification passed: all 3 blocks match snap3 state\n")

		By("Verifying all allocated blocks contain non-zero data via VerifyAllocatedBlocks")
		allocResult, err := cbtClient.GetAllocatedBlocks(ctx, snap3Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(data.VerifyAllocatedBlocks(ctx, clientset, kubeConfig, testNamespace, restorePod, allocResult)).To(Succeed())

		By("Cleaning up restore resources")
		_ = k8sutil.DeletePod(ctx, clientset, testNamespace, restorePod)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, restorePVC)
	})
})
