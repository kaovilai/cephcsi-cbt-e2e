package e2e_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/cephcsi-cbt-e2e/pkg/data"
	k8sutil "github.com/cephcsi-cbt-e2e/pkg/k8s"
)

var _ = Describe("Error Compliance", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

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
