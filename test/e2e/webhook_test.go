package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stolostron/mtv-integrations/test/utils"
)

var _ = Describe("Test webhook", func() {
	const (
		path             string = "../resources/webhook/"
		projectsPath     string = path + "projects.yaml"
		planEmptyPath    string = path + "plan_empty.yaml"
		planManaged1Path string = path + "plan_managed1.yaml"
		ns               string = "openshift-mtv"
	)

	//nolint:lll
	It("Should get failed message from webhook when user don't have permission to access target namespace",
		Label("webhook"), func() {
			utils.Kubectl("apply", "-f", projectsPath)
			DeferCleanup(func() {
				By("Clean up the projects")
				utils.Kubectl("delete", "-f", projectsPath, "--ignore-not-found")
			})

			utils.Kubectl("create", "ns", ns)
			DeferCleanup(func() {
				By("Clean up the namespace")
				utils.Kubectl("delete", "ns", ns, "--ignore-not-found")
			})

			output, _ := utils.KubectlWithOutput("apply", "-f", planEmptyPath, "--kubeconfig", "../../kubeconfig_e2e", "-n", ns)
			DeferCleanup(func() {
				By("Clean up the plan resource")
				utils.Kubectl("delete", "-f", planEmptyPath, "--ignore-not-found")
			})

			//nolint:lll
			Expect(output).Should(ContainSubstring(`admission webhook "validate.mtv.plan" denied the request: User does not have permission to access the target namespace: ` +
				ns + ` in cluster: ` + "managed-empty"))

			By("Check the plan resource is created if user has permission to access target namespace")
			output, _ = utils.KubectlWithOutput("apply", "-f", planManaged1Path, "--kubeconfig", "../../kubeconfig_e2e", "-n", ns)
			Expect(output).Should(ContainSubstring("created"))
			DeferCleanup(func() {
				By("Clean up the plan resource")
				utils.Kubectl("delete", "-f", planManaged1Path, "--ignore-not-found")
			})
		})

	It("Should get success message from webhook when provider is not managed by MTV controller",
		Label("webhook"), func() {
			const planSuffixPath string = path + "/plan_no_mtv_suffix.yaml"
			const planName string = "test-plan-1"

			utils.Kubectl("create", "ns", ns)
			DeferCleanup(func() {
				By("Clean up the namespace")
				utils.Kubectl("delete", "ns", ns, "--ignore-not-found")
			})

			output, _ := utils.KubectlWithOutput("apply", "-f", planSuffixPath, "--kubeconfig", "../../kubeconfig_e2e", "-n", ns)
			DeferCleanup(func() {
				By("Clean up the plan resource")
				utils.Kubectl("delete", "-f", planSuffixPath, "--ignore-not-found")
			})

			//nolint:lll
			Expect(output).Should(ContainSubstring("plan.forklift.konveyor.io/" + planName + " created"))
		})
})
