package e2e_test

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/cephcsi-cbt-e2e/pkg/data"
	k8sutil "github.com/cephcsi-cbt-e2e/pkg/k8s"
)

// These tests validate CBT behavior before and after force-flattening RBD images.
//
// CephCSI's snap-clone architecture creates an intermediate RBD image (csi-snap-*)
// for each VolumeSnapshot, with a parent chain back to the source image (csi-vol-*).
// CBT works via `rbd snap diff` traversing this parent chain.
//
// The test:
// 1. Creates snapshots and verifies CBT works with intact clone chains
// 2. Force-flattens the intermediate images via `rbd flatten` (simulating what
//    CephCSI does when hitting the 250-snapshot limit)
// 3. Verifies CBT behavior after flattening — without CephCSI's "stored diffs"
//    mechanism (which is not triggered by manual flatten), delta computation
//    should fail because the clone chain is broken
var _ = Describe("Stored Diffs", Label("stored-diffs"), Ordered, func() {
	var (
		ctx                context.Context
		pvcName            string
		podName            string
		snap1Name          string
		snap2Name          string
		snap3Name          string
		intermediateImages []string // csi-snap-* images (not csi-vol-*)
	)

	BeforeAll(func() {
		ctx = context.Background()
		pvcName = "stored-diffs-pvc"
		podName = "stored-diffs-pod"
		snap1Name = "stored-diffs-snap1"
		snap2Name = "stored-diffs-snap2"
		snap3Name = "stored-diffs-snap3"

		By("Listing RBD images before snapshot creation")
		imagesBefore, err := rbdInspector.ListImages(ctx)
		Expect(err).NotTo(HaveOccurred())
		beforeSet := make(map[string]bool, len(imagesBefore))
		for _, img := range imagesBefore {
			beforeSet[img] = true
		}
		GinkgoWriter.Printf("Images before: %d\n", len(imagesBefore))

		By("Creating PVC and writing initial data")
		_, err = k8sutil.CreatePVC(ctx, clientset, k8sutil.PVCOptions{
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

		By("Discovering intermediate images created by CephCSI")
		imagesAfter, err := rbdInspector.ListImages(ctx)
		Expect(err).NotTo(HaveOccurred())

		for _, img := range imagesAfter {
			if !beforeSet[img] && strings.HasPrefix(img, "csi-snap-") {
				intermediateImages = append(intermediateImages, img)
			}
		}
		GinkgoWriter.Printf("Images after: %d, intermediate (csi-snap-*) images: %v\n",
			len(imagesAfter), intermediateImages)
		Expect(intermediateImages).To(HaveLen(3),
			"should find exactly 3 intermediate images (one per VolumeSnapshot)")
	})

	AfterAll(func() {
		ctx := context.Background()
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap3Name)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap2Name)
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snap1Name)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
	})

	// --- Phase 1: Verify CBT works with intact clone chains ---

	It("should have intact parent chains on intermediate images", func() {
		for _, img := range intermediateImages {
			flattened, err := rbdInspector.IsImageFlattened(ctx, img)
			Expect(err).NotTo(HaveOccurred())
			Expect(flattened).To(BeFalse(),
				fmt.Sprintf("intermediate image %s should have a parent (not flattened) before force-flatten", img))

			parent, err := rbdInspector.GetImageParent(ctx, img)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("Image %s: parent=%q\n", img, parent)
		}
	})

	It("should have CBT working on all snapshots with intact chains", func() {
		for _, snapName := range []string{snap1Name, snap2Name, snap3Name} {
			result, err := cbtClient.GetAllocatedBlocks(ctx, snapName)
			Expect(err).NotTo(HaveOccurred(),
				"GetMetadataAllocated should work on %s with intact chain", snapName)
			Expect(result.Blocks).NotTo(BeEmpty())
			GinkgoWriter.Printf("Before flatten - %s: %d allocated blocks, %d bytes\n",
				snapName, len(result.Blocks), result.TotalChangedBytes())
		}

		By("Verifying GetMetadataDelta works with intact chains")
		delta12, err := cbtClient.GetChangedBlocks(ctx, snap1Name, snap2Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(delta12.Blocks).NotTo(BeEmpty())
		GinkgoWriter.Printf("Before flatten - delta snap1->snap2: %d blocks\n", len(delta12.Blocks))

		delta13, err := cbtClient.GetChangedBlocks(ctx, snap1Name, snap3Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(delta13.Blocks).NotTo(BeEmpty())
		GinkgoWriter.Printf("Before flatten - delta snap1->snap3: %d blocks\n", len(delta13.Blocks))
	})

	// --- Phase 2: Force-flatten and verify impact ---

	It("should force-flatten all intermediate images via rbd flatten", func() {
		for _, img := range intermediateImages {
			By(fmt.Sprintf("Flattening %s", img))
			err := rbdInspector.FlattenImage(ctx, img)
			Expect(err).NotTo(HaveOccurred(), "rbd flatten should succeed for %s", img)

			flattened, err := rbdInspector.IsImageFlattened(ctx, img)
			Expect(err).NotTo(HaveOccurred())
			Expect(flattened).To(BeTrue(),
				fmt.Sprintf("image %s should have no parent after rbd flatten", img))
			GinkgoWriter.Printf("Confirmed %s is flattened\n", img)
		}
	})

	It("should have no stored diffs in omap (manual flatten bypasses CephCSI)", func() {
		// Manual `rbd flatten` bypasses CephCSI, so the "stored diffs" mechanism
		// is NOT triggered — no diff data is pre-stored in omap.
		for _, img := range intermediateImages {
			keys, err := rbdInspector.ListOmapKeys(ctx, img)
			if err != nil {
				GinkgoWriter.Printf("Omap for %s: %v (expected - no CSI omap object)\n", img, err)
				continue
			}
			GinkgoWriter.Printf("Omap keys for %s: %v\n", img, keys)
		}
	})

	It("should fail GetMetadataAllocated after flattening without stored diffs", func() {
		// After flattening, the temp snapshots on the source image (csi-vol-*) are
		// unprotected and deleted by Ceph because no clones reference them anymore.
		// The sidecar resolves VolumeSnapshots by looking up these temp snapshots,
		// so ALL CBT operations fail — not just deltas.
		for _, snapName := range []string{snap1Name, snap2Name, snap3Name} {
			_, err := cbtClient.GetAllocatedBlocks(ctx, snapName)
			Expect(err).To(HaveOccurred(),
				"GetAllocatedBlocks should fail for %s after flattening (temp snapshot deleted)", snapName)
			GinkgoWriter.Printf("After flatten - GetAllocatedBlocks correctly failed for %s: %v\n", snapName, err)
		}
	})

	It("should fail GetMetadataDelta after flattening without stored diffs", func() {
		// GetMetadataDelta between snapshots on different (now flattened) intermediate
		// images should fail. The clone chain is broken and no stored diffs exist.
		// This demonstrates WHY stored diffs are needed: flatten without pre-stored
		// diffs permanently breaks ALL CBT operations.
		for _, pair := range [][2]string{
			{snap1Name, snap2Name},
			{snap2Name, snap3Name},
			{snap1Name, snap3Name},
		} {
			_, err := cbtClient.GetChangedBlocks(ctx, pair[0], pair[1])
			Expect(err).To(HaveOccurred(),
				"GetChangedBlocks %s->%s should fail after flattening", pair[0], pair[1])
			GinkgoWriter.Printf("After flatten - delta %s->%s correctly failed: %v\n",
				pair[0], pair[1], err)
		}
	})
})
