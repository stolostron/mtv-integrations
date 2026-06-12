// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // Ginkgo tests conventionally use dot imports.
	. "github.com/onsi/gomega"    //nolint:revive // Gomega assertions are used pervasively in this file.
	"github.com/stolostron/mtv-integrations/test/utils"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var _ = Describe("Migration advisor API", Label("migration_advisor"), Ordered, func() {
	const (
		path                 = "../resources/migration_advisor"
		managedclusterPath   = path + "/managedcluster.yaml"
		targetClusterPath    = path + "/target_managedcluster.yaml"
		untargetClusterPath  = path + "/untarget_managecluster.yaml"
		userpermissionPath   = path + "/userpermission.yaml"

		sourceCluster = "advisor-cluster"
		vmNamespace   = "default"
		vmName        = "advisor-e2e-vm"
		pvcName       = "advisor-e2e-pvc"
		scName        = "ceph-rbd"
	)

	managedClusterViewGVR := schema.GroupVersionResource{
		Group:    "view.open-cluster-management.io",
		Version:  "v1beta1",
		Resource: "managedclusterviews",
	}

	const (
		advisorSAName      = "advisor-e2e-sa"
		advisorSANamespace = "default"
	)

	var (
		baseURL    string
		authClient *http.Client // sends Authorization: Bearer <advisor-e2e-sa token>
	)

	ensureNamespace := func(ctx context.Context, name string) {
		_, err := clientHub.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = clientHub.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: name},
			}, metav1.CreateOptions{})
		}
		Expect(err).NotTo(HaveOccurred())
	}

	upsertMCVWithResult := func(ctx context.Context, clusterNS, name string, result map[string]interface{}) {
		obj := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "view.open-cluster-management.io/v1beta1",
				"kind":       "ManagedClusterView",
				"metadata": map[string]interface{}{
					"name":      name,
					"namespace": clusterNS,
				},
				"spec": map[string]interface{}{
					"scope": map[string]interface{}{
						"apiGroup":  "",
						"version":   "v1",
						"resource":  "configmaps",
						"name":      "placeholder",
						"namespace": vmNamespace,
					},
				},
			},
		}

		created, err := dynamicClientHub.Resource(managedClusterViewGVR).
			Namespace(clusterNS).
			Create(ctx, obj, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			created, err = dynamicClientHub.Resource(managedClusterViewGVR).
				Namespace(clusterNS).
				Get(ctx, name, metav1.GetOptions{})
		}
		Expect(err).NotTo(HaveOccurred())

		err = unstructured.SetNestedMap(created.Object, result, "status", "result")
		Expect(err).NotTo(HaveOccurred())
		_, err = dynamicClientHub.Resource(managedClusterViewGVR).
			Namespace(clusterNS).
			UpdateStatus(ctx, created, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())
	}

	createFakeMCVResults := func(ctx context.Context) {
		upsertMCVWithResult(ctx, sourceCluster, fmt.Sprintf("migration-advisor-vmi-%s", vmName), map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachineInstance",
			"metadata": map[string]interface{}{
				"name":      vmName,
				"namespace": vmNamespace,
			},
			"spec": map[string]interface{}{
				"domain": map[string]interface{}{
					"cpu": map[string]interface{}{
						"cores": int64(2),
					},
					"memory": map[string]interface{}{
						"guest": "4Gi",
					},
				},
			},
			"status": map[string]interface{}{
				"volumeStatus": []interface{}{
					map[string]interface{}{
						"name": "rootdisk",
						"persistentVolumeClaimInfo": map[string]interface{}{
							"claimName": pvcName,
							"capacity": map[string]interface{}{
								"storage": "10Gi",
							},
						},
					},
				},
			},
		})

		upsertMCVWithResult(ctx, sourceCluster, fmt.Sprintf("migration-advisor-pvc-%s", pvcName), map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "PersistentVolumeClaim",
			"metadata": map[string]interface{}{
				"name":      pvcName,
				"namespace": vmNamespace,
			},
			"spec": map[string]interface{}{
				"storageClassName": scName,
			},
		})
	}

	BeforeAll(func() {
		baseURL = "http://127.0.0.1:8082"
		ctx := GinkgoT().Context()

		// Create a ServiceAccount and bind it to cluster-admin so its token
		// passes the advisor RBAC gate. Kind uses cert auth for the admin
		// kubeconfig, which adds no Authorization header — we need a real
		// Bearer token to test the authenticated path.
		_, err := clientHub.CoreV1().ServiceAccounts(advisorSANamespace).Create(ctx,
			&corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{Name: advisorSAName},
			}, metav1.CreateOptions{})
		if !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}

		_, err = clientHub.RbacV1().ClusterRoleBindings().Create(ctx,
			&rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: advisorSAName},
				Subjects: []rbacv1.Subject{{
					Kind:      rbacv1.ServiceAccountKind,
					Name:      advisorSAName,
					Namespace: advisorSANamespace,
				}},
				RoleRef: rbacv1.RoleRef{
					APIGroup: rbacv1.GroupName,
					Kind:     "ClusterRole",
					Name:     "cluster-admin",
				},
			}, metav1.CreateOptions{})
		if !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}

		expiry := int64(3600)
		tokenResp, err := clientHub.CoreV1().ServiceAccounts(advisorSANamespace).
			CreateToken(ctx, advisorSAName, &authv1.TokenRequest{
				Spec: authv1.TokenRequestSpec{ExpirationSeconds: &expiry},
			}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		authClient = &http.Client{
			Transport: &bearerRoundTripper{token: tokenResp.Status.Token},
			Timeout:   15 * time.Second,
		}

		utils.Kubectl("apply", "-f", userpermissionPath)
		utils.Kubectl("apply", "-f", managedclusterPath)
		utils.Kubectl("apply", "-f", targetClusterPath)
		utils.Kubectl("apply", "-f", untargetClusterPath)
		ensureNamespace(ctx, sourceCluster)
		createFakeMCVResults(ctx)
		Eventually(func() bool {
			resp, err := http.Get(baseURL + "/health")
			if err != nil {
				return false
			}
			defer func() { _ = resp.Body.Close() }()
			return resp.StatusCode == http.StatusOK
		}, 30*time.Second, 1*time.Second).Should(BeTrue(),
			fmt.Sprintf("advisor API should be reachable at %s", baseURL))
	})

	AfterAll(func() {
		ctx := GinkgoT().Context()
		err := clientHub.RbacV1().ClusterRoleBindings().Delete(ctx, advisorSAName, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred())
		}
		err = clientHub.CoreV1().ServiceAccounts(advisorSANamespace).Delete(ctx, advisorSAName, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred())
		}
		utils.Kubectl("delete", "-f", userpermissionPath, "--ignore-not-found")
		utils.Kubectl("delete", "-f", managedclusterPath, "--ignore-not-found")
		utils.Kubectl("delete", "-f", targetClusterPath, "--ignore-not-found")
		utils.Kubectl("delete", "-f", untargetClusterPath, "--ignore-not-found")
		utils.Kubectl("delete", "ns", sourceCluster, "--ignore-not-found")
	})

	It("responds on /health", func() {
		resp, err := http.Get(baseURL + "/health")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = resp.Body.Close() }()

		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("returns 400 on /api/v1/migration-targets when required params are missing", func() {
		// Params are validated before the auth check, so 400 is returned even without a token.
		resp, err := http.Get(baseURL + "/api/v1/migration-targets")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = resp.Body.Close() }()

		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
	})

	It("returns 401 on /api/v1/migration-targets when Authorization header is missing", func() {
		url := fmt.Sprintf("%s/api/v1/migration-targets?vmNamespace=%s&vmName=%s&cluster=%s",
			baseURL, vmNamespace, vmName, sourceCluster)
		resp, err := http.Get(url)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = resp.Body.Close() }()

		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
	})

	It("returns 200 on /api/v1/migration-targets with fake Search and fake MCV", func() {
		url := fmt.Sprintf("%s/api/v1/migration-targets?vmNamespace=%s&vmName=%s&cluster=%s",
			baseURL, vmNamespace, vmName, sourceCluster)
		resp, err := authClient.Get(url)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = resp.Body.Close() }()

		var body map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&body)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(body).To(HaveKey("sourceVM"))
		Expect(body).To(HaveKey("candidates"))
		Expect(body).To(HaveKey("excludedClusters"))

		recommendation, ok := body["recommendation"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(recommendation["cluster"]).To(Equal("target-cluster"))

		excluded, ok := body["excludedClusters"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(excluded).NotTo(BeEmpty())

		foundUntarget := false
		for _, item := range excluded {
			entry, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			// untarget-cluster is a negative test fixture: it should be excluded
			// because it cannot satisfy the VM disk StorageClass requirement.
			if entry["cluster"] == "untarget-cluster" {
				foundUntarget = true
				Expect(entry["reason"]).To(ContainSubstring("No matching StorageClass"))
				break
			}
		}
		Expect(foundUntarget).To(BeTrue(), "expected untarget-cluster in excludedClusters")
	})
})

// bearerRoundTripper injects an Authorization: Bearer header on every request.
type bearerRoundTripper struct{ token string }

func (b *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
