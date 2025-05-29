package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stolostron/mtv-integrations/test/utils"
)

var _ = Describe("Test webhook", func() {
	const (
		planPath string = "../resources/webhook/plan.yaml"
		ns       string = "openshift-mtv"
	)

	//nolint:lll
	It("Should get failed message from webhook when user don't have permission to access target namespace",
		Label("webhook"), func() {
			utils.Kubectl("create", "ns", ns)
			DeferCleanup(func() {
				By("Clean up the namespace")
				utils.Kubectl("delete", "ns", ns, "--ignore-not-found")
			})

			output, _ := utils.KubectlWithOutput("apply", "-f", planPath, "--kubeconfig", "../../kubeconfig_e2e", "-n", ns)
			DeferCleanup(func() {
				By("Clean up the plan resource")
				utils.Kubectl("delete", "-f", planPath, "--ignore-not-found")
			})

			//nolint:lll
			Expect(output).Should(ContainSubstring(`admission webhook "validate.mtv.plan" denied the request: User does not have permission to access the target namespace: ` + ns))
		})
})
