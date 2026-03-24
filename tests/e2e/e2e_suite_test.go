package e2e_test

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strconv"
	"strings"
	"testing"
	"time"

	snapclient "github.com/kubernetes-csi/external-snapshotter/client/v8/clientset/versioned"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	smsclient "github.com/kubernetes-csi/external-snapshot-metadata/client/clientset/versioned"

	"github.com/cephcsi-cbt-e2e/pkg/cbt"
	k8sutil "github.com/cephcsi-cbt-e2e/pkg/k8s"
	"github.com/cephcsi-cbt-e2e/pkg/rbd"
)

var (
	storageClass     string
	snapshotClass    string
	cephcsiNamespace string
	testNamespace    string
	rbdPool          string
	smsName          string
	kubeconfig       string

	kubeConfig  *rest.Config
	clientset   kubernetes.Interface
	snapClient  snapclient.Interface
	smsClient   smsclient.Interface
	cbtClient   *cbt.Client
	rbdInspector *rbd.Inspector
)

func init() {
	flag.StringVar(&storageClass, "storage-class", "ocs-storagecluster-ceph-rbd", "RBD StorageClass name")
	flag.StringVar(&snapshotClass, "snapshot-class", "ocs-storagecluster-rbdplugin-snapclass", "VolumeSnapshotClass name")
	flag.StringVar(&cephcsiNamespace, "cephcsi-namespace", "openshift-storage", "CephCSI namespace")
	flag.StringVar(&testNamespace, "test-namespace", "cbt-e2e-test", "Test namespace prefix")
	flag.StringVar(&rbdPool, "rbd-pool", "ocs-storagecluster-cephblockpool", "RBD pool name")
	flag.StringVar(&smsName, "snapshot-metadata-service", "", "SnapshotMetadataService CR name")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (uses in-cluster config if empty)")
}

func TestCephCSICBT(t *testing.T) {
	RegisterFailHandler(Fail)

	flag.Parse()

	var err error
	if kubeconfig != "" {
		kubeConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		kubeConfig, err = rest.InClusterConfig()
		if err != nil {
			// Fallback to default kubeconfig
			kubeConfig, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		}
	}
	Expect(err).NotTo(HaveOccurred(), "failed to build kubeconfig")
	kubeConfig.QPS = 50
	kubeConfig.Burst = 100

	clientset, err = kubernetes.NewForConfig(kubeConfig)
	Expect(err).NotTo(HaveOccurred(), "failed to create kubernetes client")

	snapClient, err = snapclient.NewForConfig(kubeConfig)
	Expect(err).NotTo(HaveOccurred(), "failed to create snapshot client")

	smsClient, err = smsclient.NewForConfig(kubeConfig)
	Expect(err).NotTo(HaveOccurred(), "failed to create SnapshotMetadataService client")

	RunSpecs(t, "CephCSI CBT E2E Suite")
}

var _ = BeforeSuite(func() {
	ctx := context.Background()

	By("Checking Kubernetes server version >= 1.33")
	serverVersion, err := clientset.Discovery().ServerVersion()
	Expect(err).NotTo(HaveOccurred())
	log.Printf("Kubernetes server version: %s", serverVersion.GitVersion)
	minor, err := strconv.Atoi(strings.TrimSuffix(serverVersion.Minor, "+"))
	Expect(err).NotTo(HaveOccurred())
	// Alpha went in v1.33. Beta targets v1.36 (KEP-3314 PR #5877).
	// The SnapshotMetadataService CRD stays at v1alpha1 (out-of-tree, no k8s feature gate).
	// No feature gate required - feature is enabled by deploying the CRD and sidecar.
	Expect(minor).To(BeNumerically(">=", 33),
		"Kubernetes version must be >= 1.33 for SnapshotMetadata alpha API. "+
			"Beta expected in v1.36 (KEP-3314). API remains v1alpha1 (out-of-tree).")

	By("Checking VolumeSnapshot CRDs exist")
	_, err = snapClient.SnapshotV1().VolumeSnapshotClasses().List(ctx, metav1.ListOptions{Limit: 1})
	Expect(err).NotTo(HaveOccurred(), "VolumeSnapshot CRDs not installed (external-snapshotter required)")

	By("Checking SnapshotMetadataService CRD exists")
	// The SnapshotMetadataService CRD stays at v1alpha1 even for beta (out-of-tree API).
	// It is NOT graduating to v1beta1 with the k8s beta milestone.
	_, err = smsClient.CbtV1alpha1().SnapshotMetadataServices().List(ctx, metav1.ListOptions{Limit: 1})
	Expect(err).NotTo(HaveOccurred(),
		"SnapshotMetadataService CRD (cbt.storage.k8s.io/v1alpha1) not found. "+
			"Ensure external-snapshot-metadata is deployed. "+
			"The CRD remains v1alpha1 for both alpha (k8s 1.33) and beta (k8s 1.36+).")

	By("Checking CephCSI RBD provisioner pods running")
	// ODF 4.18+ uses a different naming convention for CSI provisioner pods.
	// Try the ODF-style label first, then fall back to the upstream label.
	labelSelectors := []string{
		"app.kubernetes.io/name=csi-rbdplugin,app.kubernetes.io/component=ctrlplugin",
		"app=csi-rbdplugin-provisioner",
	}
	var pods *corev1.PodList
	for _, selector := range labelSelectors {
		pods, err = clientset.CoreV1().Pods(cephcsiNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		})
		Expect(err).NotTo(HaveOccurred())
		if len(pods.Items) > 0 {
			log.Printf("Found %d RBD provisioner pods with selector %q", len(pods.Items), selector)
			break
		}
	}
	// If label selectors didn't match, try matching by pod name prefix (ODF 4.21+)
	if len(pods.Items) == 0 {
		allPods, listErr := clientset.CoreV1().Pods(cephcsiNamespace).List(ctx, metav1.ListOptions{})
		Expect(listErr).NotTo(HaveOccurred())
		filteredItems := make([]corev1.Pod, 0)
		for _, p := range allPods.Items {
			if strings.Contains(p.Name, "rbd") && strings.Contains(p.Name, "ctrlplugin") {
				filteredItems = append(filteredItems, p)
			}
		}
		if len(filteredItems) > 0 {
			pods.Items = filteredItems
			log.Printf("Found %d RBD provisioner pods by name pattern", len(filteredItems))
		}
	}
	Expect(pods.Items).NotTo(BeEmpty(),
		"no RBD CSI provisioner pods found in %s. "+
			"Tried label selectors and name pattern matching.", cephcsiNamespace)

	By("Checking for snapshot-metadata sidecar")
	hasSidecar := false
	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			if container.Name == "csi-snapshot-metadata" {
				hasSidecar = true
				break
			}
		}
		if hasSidecar {
			break
		}
	}
	if !hasSidecar {
		log.Println("WARNING: external-snapshot-metadata sidecar not found in RBD provisioner pods. " +
			"CBT tests requiring GetMetadataAllocated/GetMetadataDelta will fail. " +
			"Deploy the csi-snapshot-metadata container as a sidecar in the RBD provisioner pod.")
	}

	By("Checking StorageClass exists")
	_, err = clientset.StorageV1().StorageClasses().Get(ctx, storageClass, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred(), "StorageClass %s not found", storageClass)

	By("Checking VolumeSnapshotClass exists")
	_, err = snapClient.SnapshotV1().VolumeSnapshotClasses().Get(ctx, snapshotClass, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred(), "VolumeSnapshotClass %s not found", snapshotClass)

	By("Checking Ceph version >= 17 (Quincy)")
	rbdInspector = rbd.NewInspector(clientset, kubeConfig, cephcsiNamespace, rbdPool)
	cephMajor, err := rbdInspector.GetCephMajorVersion(ctx)
	Expect(err).NotTo(HaveOccurred(), "failed to get Ceph version")
	log.Printf("Ceph major version: %d", cephMajor)
	Expect(cephMajor).To(BeNumerically(">=", 17),
		"Ceph version must be >= 17 (Quincy) for rbd snap diff support")

	By(fmt.Sprintf("Creating test namespace %s", testNamespace))
	err = k8sutil.CreateNamespace(ctx, clientset, testNamespace)
	Expect(err).NotTo(HaveOccurred())

	By("Creating ServiceAccount for CBT client authentication")
	cbtSAName := "cbt-e2e-client"
	_, err = clientset.CoreV1().ServiceAccounts(testNamespace).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: cbtSAName},
	}, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create CBT client ServiceAccount")

	By("Creating ClusterRoleBinding for CBT client ServiceAccount")
	_, err = clientset.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "cbt-e2e-client-binding"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "external-snapshot-metadata-client-runner",
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      cbtSAName,
			Namespace: testNamespace,
		}},
	}, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create CBT client ClusterRoleBinding")

	By("Initializing CBT client")
	cbtClient, err = cbt.NewClient(kubeConfig, testNamespace, testNamespace, cbtSAName)
	Expect(err).NotTo(HaveOccurred(), "failed to create CBT client")
})

var _ = AfterSuite(func() {
	ctx := context.Background()

	By("Cleaning up CBT client ClusterRoleBinding")
	_ = clientset.RbacV1().ClusterRoleBindings().Delete(ctx, "cbt-e2e-client-binding", metav1.DeleteOptions{})

	By(fmt.Sprintf("Cleaning up test namespace %s", testNamespace))
	err := k8sutil.DeleteNamespace(ctx, clientset, testNamespace)
	Expect(err).NotTo(HaveOccurred())
	err = k8sutil.WaitForNamespaceDeleted(ctx, clientset, testNamespace, 5*time.Minute)
	if err != nil {
		log.Printf("WARNING: namespace %s did not fully delete: %v", testNamespace, err)
	}
})
