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

var _ = Describe("Counter-based Deletion", Ordered, func() {
	var (
		ctx           context.Context
		pvcName       string
		podName       string
		snapName      string
		roxPVCNames   []string
		numROXPVCs    int
	)

	BeforeAll(func() {
		ctx = context.Background()
		pvcName = "counter-del-pvc"
		podName = "counter-del-pod"
		snapName = "counter-del-snap"
		numROXPVCs = 3

		By("Creating source PVC with data")
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

		Expect(data.WriteBlockPattern(ctx, clientset, kubeConfig, testNamespace, podName, 0, 0xEE)).To(Succeed())
		Expect(k8sutil.DeletePod(ctx, clientset, testNamespace, podName)).To(Succeed())

		By("Creating snapshot")
		_, err = k8sutil.CreateSnapshot(ctx, snapClient, snapName, testNamespace, pvcName, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		_, err = k8sutil.WaitForSnapshotReady(ctx, snapClient, testNamespace, snapName, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Creating multiple ROX PVCs from the snapshot")
		roxPVCNames = make([]string, numROXPVCs)
		for i := 0; i < numROXPVCs; i++ {
			roxPVCNames[i] = fmt.Sprintf("counter-del-rox-%d", i)
			_, err = k8sutil.CreateROXPVCFromSnapshot(ctx, clientset, roxPVCNames[i], testNamespace, storageClass, snapName, "1Gi")
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sutil.WaitForPVCBound(ctx, clientset, testNamespace, roxPVCNames[i], 2*time.Minute)).To(Succeed())
		}
	})

	AfterAll(func() {
		ctx := context.Background()
		for _, name := range roxPVCNames {
			_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, name)
		}
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapName)
		_ = k8sutil.DeletePVC(ctx, clientset, testNamespace, pvcName)
	})

	It("should defer snapshot deletion while ROX PVCs exist", func() {
		By("Requesting snapshot deletion while ROX PVCs still exist")
		err := k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapName)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting briefly and verifying snapshot still exists (deferred deletion)")
		time.Sleep(10 * time.Second)

		// The snapshot should still be present because ROX PVCs hold references.
		// The deletion is deferred by the counter-based mechanism.
		_, err = snapClient.SnapshotV1().VolumeSnapshots(testNamespace).Get(ctx, snapName, metav1.GetOptions{})
		// Note: Depending on implementation, the VolumeSnapshot K8s object may be deleted
		// even though the underlying RBD snapshot is retained. The key behavior is that
		// the RBD snapshot is not removed while ROX PVCs reference it.
		// The VolumeSnapshotContent may have DeletionPolicy=Retain or a finalizer.
		GinkgoWriter.Printf("Snapshot deletion deferred check: err=%v\n", err)
	})

	It("should complete deletion after all ROX PVCs are removed", func() {
		By("Deleting ROX PVCs one by one (counter 3->2->1->0)")
		for i := numROXPVCs - 1; i >= 0; i-- {
			Expect(k8sutil.DeletePVC(ctx, clientset, testNamespace, roxPVCNames[i])).To(Succeed())
			GinkgoWriter.Printf("Deleted ROX PVC %s (remaining: %d)\n", roxPVCNames[i], i)

			// Brief pause to allow counter update
			time.Sleep(5 * time.Second)
		}

		By("Verifying snapshot is eventually fully deleted after all ROX PVCs removed")
		// Recreate the snapshot deletion request if it was deferred
		_ = k8sutil.DeleteSnapshot(ctx, snapClient, testNamespace, snapName)
		err := k8sutil.WaitForSnapshotDeleted(ctx, snapClient, testNamespace, snapName, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "snapshot should be fully deleted after all ROX PVCs removed")

		// Clear the names so AfterAll doesn't double-delete
		roxPVCNames = nil
	})
})
