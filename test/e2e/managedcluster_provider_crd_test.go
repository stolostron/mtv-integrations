package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stolostron/mtv-integrations/test/utils"
)

var _ = Describe("Test crd controller", Label("managedcluster_provider_crd"), Ordered, func() {
	const (
		path               string = "../resources/managedcluster_provider_crd"
		providerCrdPath    string = path + "/provider_crd.yaml"
		managedclusterPath string = path + "/managedcluster.yaml"
	)

	AfterEach(func() {
		By("Clean log file")
		err := utils.EmptyLogFile()
		Expect(err).ToNot(HaveOccurred(), "Failed to empty log file")
	})

	BeforeAll(func() {
		utils.Kubectl("apply", "-f", managedclusterPath)
		err := utils.EnsureMSAADummyDeployment(GinkgoT().Context(), clientHub, "open-cluster-management-agent-addon")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		utils.Kubectl("delete", "-f", managedclusterPath, "--ignore-not-found")
		utils.Kubectl("delete", "ns", "open-cluster-management-agent-addon", "--ignore-not-found")
	})
	It("Should not start managedcluster controller", func() {
		Consistently(func() bool {
			return utils.FindLogMessage("Reconciling ManagedCluster")
		}, 10).Should(BeFalse(),
			"ManagedCluster controller should not reconcile without Provider CRD")
	})

	It("Should reconcile managedcluster after Provider CRD provided", func() {
		utils.Kubectl("apply", "-f", providerCrdPath)
		DeferCleanup(func() {
			By("Clean up the Provider CRD")
			utils.Kubectl("delete", "-f", providerCrdPath, "--ignore-not-found")
		})

		Eventually(func() bool {
			return utils.FindLogMessage("Reconciling ManagedCluster")
		}, 30).Should(BeTrue(),
			"ManagedCluster controller continues with Provider CRD")
	})

	It("Should not continue managedcluster controller after Provider CRD deleted", func() {
		utils.Kubectl("delete", "-f", providerCrdPath, "--ignore-not-found")
		DeferCleanup(func() {
			By("Clean up the Provider CRD")
			utils.Kubectl("delete", "-f", providerCrdPath, "--ignore-not-found")
		})

		Eventually(func() bool {
			return utils.FindLogMessage("Provider CRD is not established, skipping reconciliation")
		}, 30).Should(BeTrue(),
			"ManagedCluster controller should skip with Provider CRD removed")
	})

	It("Should start again managedcluster controller after Provider CRD provided", func() {
		utils.Kubectl("apply", "-f", providerCrdPath)
		DeferCleanup(func() {
			By("Clean up the Provider CRD")
			utils.Kubectl("delete", "-f", providerCrdPath, "--ignore-not-found")
		})

		Eventually(func() bool {
			return utils.FindLogMessage("Reconciling ManagedCluster")
		}, 30).Should(BeTrue(),
			"ManagedCluster controller should continue with Provider CRD")
	})
})
