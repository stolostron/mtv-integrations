// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package e2e

import (
	"testing"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // Ginkgo tests conventionally use dot imports.
	. "github.com/onsi/gomega"    //nolint:revive // Gomega assertions are used pervasively in this file.
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

var clientHub kubernetes.Interface
var dynamicClientHub dynamic.Interface
var restConfig *rest.Config

const kubeconfigHub = "../../kubeconfig_e2e"

func TestE2e(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "MTV integrations e2e Suite")
}

var _ = BeforeSuite(func() {
	By("Setup the controller-runtime logger")
	ctrllog.SetLogger(GinkgoLogr)

	var err error
	restConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfigHub)
	Expect(err).NotTo(HaveOccurred())

	clientHub, err = kubernetes.NewForConfig(restConfig)
	Expect(err).NotTo(HaveOccurred())

	dynamicClientHub, err = dynamic.NewForConfig(restConfig)
	Expect(err).NotTo(HaveOccurred())
})
