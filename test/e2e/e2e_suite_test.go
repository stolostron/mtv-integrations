package e2e

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

var clientHub kubernetes.Interface

const kubeconfigHub = "../../kubeconfig_e2e"

func TestE2e(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "MTV integrations e2e Suite")
}

var _ = BeforeSuite(func() {
	By("Setup the controller-runtime logger")
	ctrllog.SetLogger(GinkgoLogr)

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigHub)
	if err != nil {
		Expect(err).NotTo(HaveOccurred())
	}

	clientHub, err = kubernetes.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred())
})
