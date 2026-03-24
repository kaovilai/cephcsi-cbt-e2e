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

// These tests simulate the full backup/restore workflow that Velero's Block Data
// Mover (PR #9528) would perform:
// 1. Full backup: GetMetadataAllocated -> read all allocated blocks
// 2. Incremental backup: GetMetadataDelta -> read only changed blocks
// 3. Restore: apply blocks in order (full + incremental) -> verify data
var _ = Describe("Backup Workflow", Ordered, func() {
	var (
		ctx       context.Context
		pvcName   string
		podName   string
		snap1Name string // Full backup point
		snap2Name string // First incremental
		snap3Name string // Second incremental

		// Store block hashes at each snapshot point for restore verification
		hashesAtSnap1 map[int]string
		hashesAtSnap2 map[int]string
		hashesAtSnap3 map[int]string
	)

	BeforeAll(func() {
		ctx = context.Background()
		pvcName = "backup-wf-pvc"
		podName = "backup-wf-pod"
		snap1Name = "backup-wf-snap1"
		snap2Name = "backup-wf-snap2"
		snap3Name = "backup-wf-snap3"
		hashesAtSnap1 = make(map[int]string)
		hashesAtSnap2 = make(map[int]string)
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

		// Write initial data: blocks 0, 1, 2
		By("Writing initial data for full backup (blocks 0, 1, 2)")
		for i := 0; i < 3; i++ {
			Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, i, byte(0x10+i))).To(Succeed())
		}

		// Record hashes at snap1 point
		for i := 0; i < 5; i++ {
			h, _ := data.ReadBlockHash(ctx, clientset, kubeConfig, testNamespace, podName, int64(i)*data.DefaultBlockSize, data.DefaultBlockSize)
			hashesAtSnap1[i] = h
		}

		By("Creating snapshot 1 (full backup point)")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap1Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap1Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		// Modify blocks 1 and 3 for first incremental
		By("Writing modified data for first incremental (blocks 1, 3)")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 1, 0xA1)).To(Succeed())
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 3, 0xA3)).To(Succeed())

		for i := 0; i < 5; i++ {
			h, _ := data.ReadBlockHash(ctx, clientset, kubeConfig, testNamespace, podName, int64(i)*data.DefaultBlockSize, data.DefaultBlockSize)
			hashesAtSnap2[i] = h
		}

		By("Creating snapshot 2 (first incremental)")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snap2Name, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snap2Name, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		// Modify blocks 2 and 4 for second incremental
		By("Writing modified data for second incremental (blocks 2, 4)")
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 2, 0xB2)).To(Succeed())
		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 4, 0xB4)).To(Succeed())

		for i := 0; i < 5; i++ {
			h, _ := data.ReadBlockHash(ctx, clientset, kubeConfig, testNamespace, podName, int64(i)*data.DefaultBlockSize, data.DefaultBlockSize)
			hashesAtSnap3[i] = h
		}

		By("Creating snapshot 3 (second incremental)")
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

	It("should perform full backup via GetMetadataAllocated", func() {
		result, err := cbtClient.GetAllocatedBlocks(ctx, snap1Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty())

		By("Verifying all written blocks are in the full backup")
		for i := 0; i < 3; i++ {
			offset := int64(i) * data.DefaultBlockSize
			Expect(result.ContainsOffset(offset)).To(BeTrue(),
				fmt.Sprintf("full backup should include block %d (offset %d)", i, offset))
		}

		GinkgoWriter.Printf("Full backup: %d blocks, %d bytes total\n",
			len(result.Blocks), result.TotalChangedBytes())
	})

	It("should perform first incremental backup via GetMetadataDelta", func() {
		result, err := cbtClient.GetChangedBlocks(ctx, snap1Name, snap2Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty())

		By("Verifying only changed blocks are in the incremental")
		block1Offset := int64(1) * data.DefaultBlockSize
		block3Offset := int64(3) * data.DefaultBlockSize
		Expect(result.ContainsOffset(block1Offset)).To(BeTrue(),
			"incremental should include modified block 1")
		Expect(result.ContainsOffset(block3Offset)).To(BeTrue(),
			"incremental should include new block 3")

		By("Verifying unchanged blocks are NOT in the incremental")
		block0Offset := int64(0) * data.DefaultBlockSize
		Expect(result.ContainsOffset(block0Offset)).To(BeFalse(),
			"incremental should NOT include unchanged block 0")

		GinkgoWriter.Printf("First incremental: %d blocks, %d bytes\n",
			len(result.Blocks), result.TotalChangedBytes())
	})

	It("should perform second incremental backup (chained full->incr1->incr2)", func() {
		result, err := cbtClient.GetChangedBlocks(ctx, snap2Name, snap3Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty())

		By("Verifying second incremental captures correct blocks")
		block2Offset := int64(2) * data.DefaultBlockSize
		block4Offset := int64(4) * data.DefaultBlockSize
		Expect(result.ContainsOffset(block2Offset)).To(BeTrue(),
			"second incremental should include modified block 2")
		Expect(result.ContainsOffset(block4Offset)).To(BeTrue(),
			"second incremental should include new block 4")

		GinkgoWriter.Printf("Second incremental: %d blocks, %d bytes\n",
			len(result.Blocks), result.TotalChangedBytes())
	})

	It("should restore from chain and verify data integrity", func() {
		By("Creating a restore PVC from snap3")
		restorePVC := "backup-wf-restore-pvc"
		restorePod := "backup-wf-restore-pod"

		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
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

		By("Verifying restored data matches final snapshot state")
		for i := 0; i < 5; i++ {
			hash, err := data.ReadBlockHash(ctx, clientset, kubeConfig, testNamespace, restorePod, int64(i)*data.DefaultBlockSize, data.DefaultBlockSize)
			Expect(err).NotTo(HaveOccurred())
			Expect(hash).To(Equal(hashesAtSnap3[i]),
				fmt.Sprintf("restored block %d should match snap3 state", i))
		}

		// Cleanup
		_ = k8sutil.DeletePod(ctx, clientset, testNamespace, restorePod)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, restorePVC)
	})

	It("should support backup workflow with ROX PVCs as read source", func() {
		By("Creating ROX PVC from latest snapshot for read access")
		roxPVC := "backup-wf-rox-pvc"
		roxPod := "backup-wf-rox-pod"

		_, err := k8sutil.CreateROXPVCFromSnapshot(ctx, clientset, roxPVC, testNamespace, storageClass, snap3Name, "1Gi")
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, roxPVC, 2*time.Minute)).To(Succeed())

		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name:       roxPod,
			Namespace:  testNamespace,
			PVCName:    roxPVC,
			ReadOnly:   true,
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, roxPod, 2*time.Minute)).To(Succeed())

		By("Reading data from ROX PVC (simulating backup data reader)")
		for i := 0; i < 5; i++ {
			hash, err := data.ReadBlockHash(ctx, clientset, kubeConfig, testNamespace, roxPod, int64(i)*data.DefaultBlockSize, data.DefaultBlockSize)
			Expect(err).NotTo(HaveOccurred())
			Expect(hash).To(Equal(hashesAtSnap3[i]),
				fmt.Sprintf("ROX PVC block %d should match snap3 state for backup", i))
		}

		By("Verifying CBT still works while ROX PVC is active")
		result, err := cbtClient.GetAllocatedBlocks(ctx, snap3Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty())

		// Cleanup
		_ = k8sutil.DeletePod(ctx, clientset, testNamespace, roxPod)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, roxPVC)
	})
})
