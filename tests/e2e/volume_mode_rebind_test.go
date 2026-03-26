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

// These tests validate the Velero Block Data Mover's volume mode rebinding workflow:
// 1. Source PVC (Filesystem) -> snapshot -> Block PVC restore -> CBT read
// 2. Rebind: create a new PV with Filesystem VolumeMode using the same volume handle
// 3. Verify the rebound Filesystem PVC retains original data
//
// Ref: https://github.com/Lyndon-Li/velero/blob/block-data-mover-design/design/block-data-mover/block-data-mover.md
var _ = Describe("Volume Mode Rebind", Ordered, func() {
	var (
		ctx     context.Context
		fsPVC   string
		fsPod   string
		snapName string

		fileHashes map[string]string // filename -> sha256
	)

	fsMode := corev1.PersistentVolumeFilesystem

	BeforeAll(func() {
		ctx = context.Background()
		fsPVC = "rebind-fs-pvc"
		fsPod = "rebind-fs-pod"
		snapName = "rebind-snap"
		fileHashes = make(map[string]string)

		By("Creating Filesystem-mode source PVC")
		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name:         fsPVC,
			Namespace:    testNamespace,
			StorageClass: storageClass,
			Size:         "1Gi",
			AccessModes:  []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			VolumeMode:   &fsMode,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, fsPVC, 2*time.Minute)).To(Succeed())

		By("Creating pod and writing files to Filesystem PVC")
		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name:       fsPod,
			Namespace:  testNamespace,
			PVCName:    fsPVC,
			VolumeMode: corev1.PersistentVolumeFilesystem,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, fsPod, 2*time.Minute)).To(Succeed())

		// Write identifiable files
		testFiles := map[string]string{
			"hello.txt":   "Hello from Velero volume mode rebind test!",
			"data.bin":    "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789",
			"subdir/nested.txt": "nested file content for verification",
		}

		// Create subdirectory first
		_, _, err = k8sutil.ExecInPod(ctx, clientset, kubeConfig, testNamespace, fsPod, "", []string{
			"mkdir", "-p", "/mnt/data/subdir",
		})
		Expect(err).NotTo(HaveOccurred())

		for filename, content := range testFiles {
			Expect(data.WriteFile(ctx, clientset, kubeConfig, testNamespace, fsPod, filename, content)).To(Succeed())
		}

		By("Recording file hashes before snapshot")
		for filename := range testFiles {
			hash, hashErr := data.ReadFileHash(ctx, clientset, kubeConfig, testNamespace, fsPod, filename)
			Expect(hashErr).NotTo(HaveOccurred())
			fileHashes[filename] = hash
			GinkgoWriter.Printf("File %s hash: %s\n", filename, hash)
		}

		By("Deleting pod before snapshot")
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, fsPod)).To(Succeed())
		Expect(k8sutil.WaitForPodDeleted(ctx, clientset, testNamespace, fsPod, 2*time.Minute)).To(Succeed())

		By("Creating snapshot of Filesystem PVC")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snapName, testNamespace, fsPVC, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snapName, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Annotating VolumeSnapshotContent to allow volume mode conversion (KEP-3141)")
		// Kubernetes prevents creating a PVC with a different VolumeMode from the snapshot
		// unless the VolumeSnapshotContent has this annotation. This is what Velero sets
		// to enable Filesystem->Block conversion during backup.
		// Ref: https://github.com/kubernetes/enhancements/blob/master/keps/sig-storage/3141-prevent-volume-mode-conversion/README.md
		vscName, err := k8sutil.GetSnapshotContentName(ctx, snapClient, testNamespace, snapName)
		Expect(err).NotTo(HaveOccurred())
		vsc, err := snapClient.SnapshotV1().VolumeSnapshotContents().Get(ctx, vscName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		if vsc.Annotations == nil {
			vsc.Annotations = make(map[string]string)
		}
		vsc.Annotations["snapshot.storage.kubernetes.io/allow-volume-mode-change"] = "true"
		_, err = snapClient.SnapshotV1().VolumeSnapshotContents().Update(ctx, vsc, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Annotated VolumeSnapshotContent %s for volume mode conversion\n", vscName)
	})

	AfterAll(func() {
		ctx := context.Background()
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapName)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, fsPVC)
	})

	It("should return allocated blocks for a Filesystem-mode snapshot via CBT", func() {
		result, err := cbtClient.GetAllocatedBlocks(ctx, snapName)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Blocks).NotTo(BeEmpty(),
			"CBT should report allocated blocks for Filesystem PVC (filesystem metadata + file data)")

		GinkgoWriter.Printf("Filesystem snapshot: %d allocated blocks, %d bytes\n",
			len(result.Blocks), result.TotalChangedBytes())
	})

	It("should restore Filesystem snapshot to Block PVC and read block data via CBT", func() {
		blockRestorePVC := "rebind-block-pvc"
		blockRestorePod := "rebind-block-pod"

		By("Creating Block-mode PVC from Filesystem snapshot (Velero exposer pattern)")
		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name:           blockRestorePVC,
			Namespace:      testNamespace,
			StorageClass:   storageClass,
			Size:           "1Gi",
			AccessModes:    []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			SnapshotSource: snapName,
			// VolumeMode defaults to Block
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, blockRestorePVC, 2*time.Minute)).To(Succeed())

		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name:       blockRestorePod,
			Namespace:  testNamespace,
			PVCName:    blockRestorePVC,
			VolumeMode: corev1.PersistentVolumeBlock,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, blockRestorePod, 2*time.Minute)).To(Succeed())

		By("Verifying CBT-reported blocks contain non-zero data on Block PVC")
		allocResult, err := cbtClient.GetAllocatedBlocks(ctx, snapName)
		Expect(err).NotTo(HaveOccurred())
		Expect(data.VerifyAllocatedBlocks(ctx, clientset, kubeConfig, testNamespace, blockRestorePod, allocResult)).To(Succeed())

		GinkgoWriter.Printf("Block restore: verified %d allocated blocks contain data\n", len(allocResult.Blocks))

		_ = k8sutil.DeletePod(ctx, clientset, testNamespace, blockRestorePod)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, blockRestorePVC)
	})

	It("should rebind volume as Filesystem and retain original file data", func() {
		restorePVC := "rebind-restore-pvc"
		reboundPV := "rebind-rebound-pv"
		reboundPVC := "rebind-rebound-pvc"
		reboundPod := "rebind-rebound-pod"

		By("Restoring Block PVC from snapshot (intermediate step)")
		_, err := k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
			Name:           restorePVC,
			Namespace:      testNamespace,
			StorageClass:   storageClass,
			Size:           "1Gi",
			AccessModes:    []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			SnapshotSource: snapName,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, restorePVC, 2*time.Minute)).To(Succeed())

		By("Getting the PV name from the restored PVC")
		pvc, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(ctx, restorePVC, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		sourcePVName := pvc.Spec.VolumeName
		Expect(sourcePVName).NotTo(BeEmpty())
		GinkgoWriter.Printf("Restored PVC bound to PV: %s\n", sourcePVName)

		By("Deleting the intermediate Block PVC (releasing the PV)")
		// Set reclaim policy to Retain so the volume survives PVC deletion
		sourcePV, err := clientset.CoreV1().PersistentVolumes().Get(ctx, sourcePVName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		sourcePV.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
		_, err = clientset.CoreV1().PersistentVolumes().Update(ctx, sourcePV, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sutil.DeletePVC(ctx, clientset, testNamespace, restorePVC)).To(Succeed())

		// Wait for PV to become Released/Available
		Eventually(func() corev1.PersistentVolumePhase {
			pv, pvErr := clientset.CoreV1().PersistentVolumes().Get(ctx, sourcePVName, metav1.GetOptions{})
			if pvErr != nil {
				return ""
			}
			return pv.Status.Phase
		}, 2*time.Minute, 5*time.Second).Should(Equal(corev1.VolumeReleased))

		By("Rebinding: creating new PV with Filesystem VolumeMode using same volume handle")
		Expect(k8sutil.RebindPVWithVolumeMode(ctx, clientset,
			sourcePVName, reboundPV, reboundPVC, testNamespace,
			corev1.PersistentVolumeFilesystem,
			[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		)).To(Succeed())

		Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, reboundPVC, 2*time.Minute)).To(Succeed())

		By("Mounting rebound Filesystem PVC and verifying original files")
		_, err = k8sutil.CreatePodWithPVC(ctx, clientset, k8sutil.PodOptions{
			Name:       reboundPod,
			Namespace:  testNamespace,
			PVCName:    reboundPVC,
			VolumeMode: corev1.PersistentVolumeFilesystem,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sutil.WaitForPodRunning(ctx, clientset, testNamespace, reboundPod, 2*time.Minute)).To(Succeed())

		By("Verifying each file matches the original hash")
		for filename, expectedHash := range fileHashes {
			hash, hashErr := data.ReadFileHash(ctx, clientset, kubeConfig, testNamespace, reboundPod, filename)
			Expect(hashErr).NotTo(HaveOccurred(),
				fmt.Sprintf("file %s should be readable on rebound Filesystem PVC", filename))
			Expect(hash).To(Equal(expectedHash),
				fmt.Sprintf("file %s hash should match original after rebind (expected %s, got %s)", filename, expectedHash, hash))
			GinkgoWriter.Printf("Verified %s: hash matches (%s)\n", filename, hash)
		}

		GinkgoWriter.Printf("Rebind verification passed: all %d files retained after Block->Filesystem rebind\n", len(fileHashes))

		By("Cleaning up rebind resources")
		_ = k8sutil.DeletePod(ctx, clientset, testNamespace, reboundPod)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, reboundPVC)
		_ = k8sutil.DeletePV(ctx, clientset, reboundPV)
		_ = k8sutil.DeletePV(ctx, clientset, sourcePVName)
	})
})
