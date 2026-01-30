package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stolostron/mtv-integrations/test/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var _ = Describe("ManagedCluster cleanup", Ordered, func() {
	const (
		clusterName = "cleanup-cluster"
		clusterMTV  = "cleanup-cluster-mtv"

		path                = "../resources/cleanup_resources"
		managedClusterPath  = path + "/managedcluster.yaml"
		providerCrdPath     = "../resources/managedcluster_provider_crd/provider_crd.yaml"
		msaaNs              = "open-cluster-management-agent-addon"
		mtvIntegrationsNs   = "mtv-integrations"
		managedClusterFinal = "mtv-integrations.open-cluster-management.io/resource-cleanup"

		assertTimeout = 60 * time.Second
	)

	var (
		ClusterPermissionsGVR = schema.GroupVersionResource{
			Group:    "rbac.open-cluster-management.io",
			Version:  "v1alpha1",
			Resource: "clusterpermissions",
		}
		ManagedServiceAccountsGVR = schema.GroupVersionResource{
			Group:    "authentication.open-cluster-management.io",
			Version:  "v1beta1",
			Resource: "managedserviceaccounts",
		}
		ProvidersGVR = schema.GroupVersionResource{
			Group:    "forklift.konveyor.io",
			Version:  "v1beta1",
			Resource: "providers",
		}
		ProviderSecretGVR = schema.GroupVersionResource{
			Group:    "",
			Version:  "v1",
			Resource: "secrets",
		}
		ManagedClusterGVR = schema.GroupVersionResource{
			Group:    "cluster.open-cluster-management.io",
			Version:  "v1",
			Resource: "managedclusters",
		}
	)

	containsString := func(haystack []string, needle string) bool {
		for _, v := range haystack {
			if v == needle {
				return true
			}
		}
		return false
	}

	resourceGet := func(ctx context.Context, gvr schema.GroupVersionResource,
		namespace, name string) (*unstructured.Unstructured, error) {
		if namespace == "" {
			return dynamicClientHub.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		}
		return dynamicClientHub.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	}

	resourceExists := func(gvr schema.GroupVersionResource, namespace, name string) {
		Eventually(func() error {
			_, err := resourceGet(GinkgoT().Context(), gvr, namespace, name)
			return err
		}, assertTimeout).Should(Succeed(), "%s expected %s/%s to exist", gvr.Resource, namespace, name)
	}

	resourceGone := func(gvr schema.GroupVersionResource, namespace, name string) {
		Eventually(func() bool {
			_, err := resourceGet(GinkgoT().Context(), gvr, namespace, name)
			return apierrors.IsNotFound(err)
		}, assertTimeout).Should(BeTrue(), "%s expected %s/%s to be deleted", gvr.Resource, namespace, name)
	}

	getManagedCluster := func(ctx context.Context) (*unstructured.Unstructured, error) {
		// ManagedCluster is cluster-scoped (no namespace).
		return dynamicClientHub.Resource(ManagedClusterGVR).Get(ctx, clusterName, metav1.GetOptions{})
	}

	finalizerPresent := func() bool {
		mc, err := getManagedCluster(GinkgoT().Context())
		if err != nil {
			return false
		}
		return containsString(mc.GetFinalizers(), managedClusterFinal)
	}

	finalizerAbsent := func() bool {
		mc, err := getManagedCluster(GinkgoT().Context())
		if apierrors.IsNotFound(err) {
			// If the ManagedCluster is already deleted, the finalizer is effectively absent.
			return true
		}
		if err != nil {
			return false
		}
		return !containsString(mc.GetFinalizers(), managedClusterFinal)
	}

	ensureProviderSecret := func() {
		ctx := GinkgoT().Context()

		desired := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterMTV,
				Namespace: mtvIntegrationsNs,
			},
			Data: map[string][]byte{
				"token":              []byte("dummy-token"),
				"cacert":             []byte("dummy-ca"),
				"insecureSkipVerify": []byte("false"),
				"url":                []byte("https://api.cleanup-cluster.example:6443"),
			},
		}

		_, err := clientHub.CoreV1().Secrets(mtvIntegrationsNs).Get(ctx, clusterMTV, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				_, err = clientHub.CoreV1().Secrets(mtvIntegrationsNs).Create(ctx, desired, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())
				return
			}
		}
	}

	requireCRDsAndProviderCRD := func() {
		// Preflight: these CRDs must exist for cleanup to delete the custom resources.
		requireCRD := func(name string) {
			out, err := utils.KubectlWithOutput("get", "crd", name)
			Expect(err).NotTo(HaveOccurred(), "required CRD is missing: %s\n%s", name, out)
		}

		requireCRD("managedserviceaccounts.authentication.open-cluster-management.io")
		requireCRD("clusterpermissions.rbac.open-cluster-management.io")

		// Ensure Provider CRD exists and is established.
		utils.Kubectl("apply", "-f", providerCrdPath)
		utils.Kubectl("wait", "--for=condition=Established", "crd/providers.forklift.konveyor.io", "--timeout=60s")
	}

	ensureNamespaces := func(ctx context.Context) {
		// Ensure the managedCluster namespace exists (idempotent).
		if _, err := clientHub.CoreV1().Namespaces().Get(ctx, clusterName, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			_, err := clientHub.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName},
			}, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
		}

		if _, err := clientHub.CoreV1().Namespaces().Get(ctx,
			mtvIntegrationsNs, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			_, err := clientHub.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: mtvIntegrationsNs},
			}, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
		}
	}

	waitForResourcesExist := func() {
		resourceExists(ClusterPermissionsGVR, clusterName, clusterMTV)
		resourceExists(ManagedServiceAccountsGVR, clusterName, clusterMTV)
		resourceExists(ProviderSecretGVR, mtvIntegrationsNs, clusterMTV)
		resourceExists(ProvidersGVR, mtvIntegrationsNs, clusterMTV)
	}

	ensureBaseline := func() {
		// Ensure ManagedCluster exists.
		utils.Kubectl("apply", "-f", managedClusterPath)

		ctx := GinkgoT().Context()
		ensureNamespaces(ctx)

		// Ensure MSAA deployment exists so the controller can compute its namespace.
		err := utils.EnsureMSAADummyDeployment(ctx, clientHub, msaaNs)
		Expect(err).NotTo(HaveOccurred())

		// Enable management and wait for finalizer.
		utils.Kubectl("label", "managedcluster", clusterName, "acm/cnv-operator-install=true", "--overwrite")
		Eventually(finalizerPresent, assertTimeout).Should(BeTrue(), "expected MTV finalizer to be added")

		// The controller normally creates the provider Secret from the ManagedServiceAccount token secret.
		// In this e2e environment, we might not have the MSAA token flow, so create a dummy provider Secret
		// to allow Provider reconciliation/validation to proceed.
		ensureProviderSecret()

		// Ensure we actually have resources to later verify deletion semantics.
		waitForResourcesExist()
	}

	BeforeAll(func() {
		requireCRDsAndProviderCRD()
	})

	BeforeEach(func() {
		ensureBaseline()
	})

	AfterEach(func() {
		// Keep the env clean between specs so each test is self-contained.
		utils.Kubectl("delete", "providers.forklift.konveyor.io", clusterMTV, "-n",
			mtvIntegrationsNs, "--ignore-not-found")
		utils.Kubectl("delete", "secret", clusterMTV, "-n", mtvIntegrationsNs, "--ignore-not-found")
		utils.Kubectl("delete", "managedserviceaccounts.authentication.open-cluster-management.io",
			clusterMTV, "-n", clusterName, "--ignore-not-found")
		utils.Kubectl("delete", "clusterpermissions.rbac.open-cluster-management.io",
			clusterMTV, "-n", clusterName, "--ignore-not-found")
		utils.Kubectl("delete", "managedcluster", clusterName, "--ignore-not-found")
		utils.Kubectl("delete", "ns", clusterName, "--ignore-not-found")
		utils.Kubectl("delete", "ns", mtvIntegrationsNs, "--ignore-not-found")
	})

	assertResourcesDeleted := func() {
		resourceGone(ClusterPermissionsGVR, clusterName, clusterMTV)
		resourceGone(ManagedServiceAccountsGVR, clusterName, clusterMTV)
		resourceGone(ProviderSecretGVR, mtvIntegrationsNs, clusterMTV)
		resourceGone(ProvidersGVR, mtvIntegrationsNs, clusterMTV)
	}

	assertResourcesExist := func() {
		resourceExists(ClusterPermissionsGVR, clusterName, clusterMTV)
		resourceExists(ManagedServiceAccountsGVR, clusterName, clusterMTV)
		resourceExists(ProviderSecretGVR, mtvIntegrationsNs, clusterMTV)
		resourceExists(ProvidersGVR, mtvIntegrationsNs, clusterMTV)
	}

	It("Should delete MTV resources when the label is removed", func() {
		// Trigger cleanup: remove the label that enables management.
		utils.Kubectl("label", "managedcluster", clusterName, "acm/cnv-operator-install-")
		// Finalizer should be removed by the controller.
		Eventually(finalizerAbsent, assertTimeout).Should(BeTrue(), "expected MTV finalizer to be removed")
		// Resources should be deleted.
		assertResourcesDeleted()

		By("Should recreate MTV resources when the label is added again")
		ensureProviderSecret()
		// Re-enable management: add the label back.
		utils.Kubectl("label", "managedcluster", clusterName, "acm/cnv-operator-install=true", "--overwrite")
		// Finalizer is added by the controller.
		Eventually(finalizerPresent, assertTimeout).Should(BeTrue(), "expected MTV finalizer to be added")
		// Resources should be recreated.
		assertResourcesExist()
	})

	It("Should delete MTV resources when the ManagedCluster is deleted", func() {
		// Trigger cleanup: delete the ManagedCluster. Controller should remove the finalizer,
		// allowing the CR to fully disappear.
		utils.Kubectl("delete", "managedcluster", clusterName, "--ignore-not-found")

		// ManagedCluster should eventually be fully deleted.
		resourceGone(ManagedClusterGVR, "", clusterName)

		// Finalizer should be removed by the controller.
		Eventually(finalizerAbsent, assertTimeout).Should(BeTrue(), "expected MTV finalizer to be removed")
		// Resources should be deleted.
		assertResourcesDeleted()
	})
})
