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
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var _ = Describe("Plan ownership", Label("plan_ownership"), Ordered, func() {
	const (
		path             = "../resources/plan_ownership"
		mtvNs            = "mtv-integrations"
		assertTimeout    = 60 * time.Second
		assertConsistent = 10 * time.Second
	)

	var (
		networkMapGVR = schema.GroupVersionResource{
			Group:    "forklift.konveyor.io",
			Version:  "v1beta1",
			Resource: "networkmaps",
		}
		storageMapGVR = schema.GroupVersionResource{
			Group:    "forklift.konveyor.io",
			Version:  "v1beta1",
			Resource: "storagemaps",
		}
		planGVR = schema.GroupVersionResource{
			Group:    "forklift.konveyor.io",
			Version:  "v1beta1",
			Resource: "plans",
		}
	)

	// hasOwnerRef returns true when the resource identified by gvr/name in mtvNs
	// has an ownerReference pointing to the named Plan.
	hasOwnerRef := func(ctx context.Context, gvr schema.GroupVersionResource, name, planName string) (bool, error) {
		obj, err := dynamicClientHub.Resource(gvr).Namespace(mtvNs).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, ref := range obj.GetOwnerReferences() {
			if ref.Kind == "Plan" && ref.Name == planName {
				return true, nil
			}
		}
		return false, nil
	}

	// resourceGone returns true when the resource no longer exists.
	resourceGone := func(ctx context.Context, gvr schema.GroupVersionResource, name string) bool {
		_, err := dynamicClientHub.Resource(gvr).Namespace(mtvNs).Get(ctx, name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}

	BeforeAll(func() {
		ctx := GinkgoT().Context()
		// Ensure the mtv-integrations namespace exists.
		_, err := clientHub.CoreV1().Namespaces().Get(ctx, mtvNs, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred(), "unexpected error checking namespace %s", mtvNs)
		}
		if apierrors.IsNotFound(err) {
			_, createErr := clientHub.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: mtvNs},
			}, metav1.CreateOptions{})
			Expect(createErr).NotTo(HaveOccurred())
		}

		// Ensure NetworkMap and StorageMap CRDs are available.
		utils.Kubectl("wait", "--for=condition=Established",
			"crd/networkmaps.forklift.konveyor.io", "--timeout=60s")
		utils.Kubectl("wait", "--for=condition=Established",
			"crd/storagemaps.forklift.konveyor.io", "--timeout=60s")
		utils.Kubectl("wait", "--for=condition=Established",
			"crd/plans.forklift.konveyor.io", "--timeout=60s")
	})

	AfterEach(func() {
		// Clean up all fixtures regardless of test outcome.
		utils.Kubectl("delete", "-f", path+"/plan_cclm.yaml", "--ignore-not-found")
		utils.Kubectl("delete", "-f", path+"/plan_no_label.yaml", "--ignore-not-found")
		utils.Kubectl("delete", "-f", path+"/plan_cclm_no_label_maps.yaml", "--ignore-not-found")
		utils.Kubectl("delete", "-f", path+"/networkmap_cclm.yaml", "--ignore-not-found")
		utils.Kubectl("delete", "-f", path+"/storagemap_cclm.yaml", "--ignore-not-found")
		utils.Kubectl("delete", "-f", path+"/networkmap_no_label.yaml", "--ignore-not-found")
		utils.Kubectl("delete", "-f", path+"/storagemap_no_label.yaml", "--ignore-not-found")
	})

	It("sets OwnerReference on NetworkMap and StorageMap when both have cclm label", func() {
		utils.Kubectl("apply", "-f", path+"/networkmap_cclm.yaml")
		utils.Kubectl("apply", "-f", path+"/storagemap_cclm.yaml")
		utils.Kubectl("apply", "-f", path+"/plan_cclm.yaml")

		ctx := GinkgoT().Context()

		Eventually(func() bool {
			ok, err := hasOwnerRef(ctx, networkMapGVR, "test-networkmap-cclm", "test-plan-cclm")
			Expect(err).NotTo(HaveOccurred())
			return ok
		}, assertTimeout).Should(BeTrue(), "NetworkMap should have OwnerReference to the Plan")

		Eventually(func() bool {
			ok, err := hasOwnerRef(ctx, storageMapGVR, "test-storagemap-cclm", "test-plan-cclm")
			Expect(err).NotTo(HaveOccurred())
			return ok
		}, assertTimeout).Should(BeTrue(), "StorageMap should have OwnerReference to the Plan")
	})

	It("does NOT set OwnerReference when Plan lacks cclm label", func() {
		utils.Kubectl("apply", "-f", path+"/networkmap_cclm.yaml")
		utils.Kubectl("apply", "-f", path+"/storagemap_cclm.yaml")
		utils.Kubectl("apply", "-f", path+"/plan_no_label.yaml")

		ctx := GinkgoT().Context()

		Consistently(func() bool {
			ok, err := hasOwnerRef(ctx, networkMapGVR, "test-networkmap-cclm", "test-plan-no-label")
			Expect(err).NotTo(HaveOccurred())
			return ok
		}, assertConsistent).Should(BeFalse(), "NetworkMap should NOT have OwnerReference from unlabeled Plan")

		Consistently(func() bool {
			ok, err := hasOwnerRef(ctx, storageMapGVR, "test-storagemap-cclm", "test-plan-no-label")
			Expect(err).NotTo(HaveOccurred())
			return ok
		}, assertConsistent).Should(BeFalse(), "StorageMap should NOT have OwnerReference from unlabeled Plan")
	})

	It("does NOT set OwnerReference on a map that lacks cclm label", func() {
		utils.Kubectl("apply", "-f", path+"/networkmap_no_label.yaml")
		utils.Kubectl("apply", "-f", path+"/storagemap_cclm.yaml")
		utils.Kubectl("apply", "-f", path+"/plan_cclm_no_label_maps.yaml")

		ctx := GinkgoT().Context()

		// StorageMap has the label → gets an OwnerReference.
		Eventually(func() bool {
			ok, err := hasOwnerRef(ctx, storageMapGVR, "test-storagemap-cclm", "test-plan-cclm-no-label-maps")
			Expect(err).NotTo(HaveOccurred())
			return ok
		}, assertTimeout).Should(BeTrue(), "StorageMap should have OwnerReference to the Plan")

		// NetworkMap lacks the label → must NOT get an OwnerReference.
		Consistently(func() bool {
			ok, err := hasOwnerRef(ctx, networkMapGVR, "test-networkmap-no-label", "test-plan-cclm-no-label-maps")
			Expect(err).NotTo(HaveOccurred())
			return ok
		}, assertConsistent).Should(BeFalse(), "NetworkMap without cclm label should NOT get OwnerReference")
	})

	It("deletes NetworkMap and StorageMap when the Plan is deleted", func() {
		utils.Kubectl("apply", "-f", path+"/networkmap_cclm.yaml")
		utils.Kubectl("apply", "-f", path+"/storagemap_cclm.yaml")
		utils.Kubectl("apply", "-f", path+"/plan_cclm.yaml")

		ctx := GinkgoT().Context()

		// Wait for OwnerReferences to be set.
		Eventually(func() bool {
			ok, err := hasOwnerRef(ctx, networkMapGVR, "test-networkmap-cclm", "test-plan-cclm")
			Expect(err).NotTo(HaveOccurred())
			return ok
		}, assertTimeout).Should(BeTrue(), "NetworkMap should have OwnerReference before deletion test")
		Eventually(func() bool {
			ok, err := hasOwnerRef(ctx, storageMapGVR, "test-storagemap-cclm", "test-plan-cclm")
			Expect(err).NotTo(HaveOccurred())
			return ok
		}, assertTimeout).Should(BeTrue(), "StorageMap should have OwnerReference before deletion test")

		// Delete the Plan — Kubernetes GC cascades to owned resources.
		utils.Kubectl("delete", "-f", path+"/plan_cclm.yaml")

		Eventually(func() bool {
			return resourceGone(ctx, planGVR, "test-plan-cclm")
		}, assertTimeout).Should(BeTrue(), "Plan should be deleted")

		Eventually(func() bool {
			return resourceGone(ctx, networkMapGVR, "test-networkmap-cclm")
		}, assertTimeout).Should(BeTrue(), "NetworkMap should be garbage-collected after Plan deletion")

		Eventually(func() bool {
			return resourceGone(ctx, storageMapGVR, "test-storagemap-cclm")
		}, assertTimeout).Should(BeTrue(), "StorageMap should be garbage-collected after Plan deletion")
	})
})
