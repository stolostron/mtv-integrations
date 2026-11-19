// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // Ginkgo tests conventionally use dot imports.
	. "github.com/onsi/gomega"    //nolint:revive // Gomega assertions are used pervasively in this file.
	"github.com/stolostron/mtv-integrations/api"
	"github.com/stolostron/mtv-integrations/test/utils"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var _ = Describe("Proxy CCLM CA exchange API", Label("cclm_ca_exchange"), Ordered, func() {
	const (
		path               = "../resources/cclm_ca_exchange"
		managedClusters    = path + "/managedclusters.yaml"
		userpermissionPath        = "../resources/migration_advisor/userpermission.yaml"
		userpermissionKubeVirtPath = "../resources/migration_advisor/userpermission-kubevirt.yaml"

		exchangePath   = "/api/v1/proxy-cclm/ca-exchange"
		baseURL        = "http://127.0.0.1:8082"
		mtvNs          = "mtv-integrations"
		kubevirtNs     = "openshift-cnv"
		kubevirtCAName = "kubevirt-ca"
		caBundleKey    = "ca-bundle"

		clusterA = "ca-spoke-a"
		clusterB = "ca-spoke-b"

		caExchangeSAName      = "ca-exchange-e2e-sa"
		caExchangeSANamespace = "default"
	)

	var (
		authClient               *http.Client
		bareHTTPClient           *http.Client
		spokeToken               string
		createdNamespaces        []string
		kubevirtCAPrior          *corev1.ConfigMap
		kubevirtCACreated        bool
		caExchangeSACreated      bool
		caExchangeCRBCreated     bool
	)

	expectNoErr := func(err error, msg string) {
		Expect(err).NotTo(HaveOccurred(), msg)
	}

	deleteIgnoringNotFound := func(err error, resource string) {
		if err != nil && !apierrors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred(), "delete "+resource)
		}
	}

	generateCAPEM := func(cn string) []byte {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		expectNoErr(err, "generate RSA key for test CA")
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(48 * time.Hour),
			KeyUsage:     x509.KeyUsageCertSign,
			IsCA:         true,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		expectNoErr(err, "generate test CA certificate")
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	}

	ensureNamespace := func(ctx context.Context, name string) {
		_, err := clientHub.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = clientHub.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: name},
			}, metav1.CreateOptions{})
			if err == nil {
				createdNamespaces = append(createdNamespaces, name)
			}
		}
		expectNoErr(err, "ensure namespace "+name)
	}

	ensureProviderSecret := func(ctx context.Context, cluster, token string) {
		secretName := cluster + "-mtv"
		url := restConfig.Host
		data := map[string][]byte{
			"token": []byte(token),
			"url":   []byte(url),
		}
		if len(restConfig.CAData) > 0 {
			data["cacert"] = restConfig.CAData
		}
		desired := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: mtvNs},
			Data:       data,
		}
		_, err := clientHub.CoreV1().Secrets(mtvNs).Get(ctx, secretName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = clientHub.CoreV1().Secrets(mtvNs).Create(ctx, desired, metav1.CreateOptions{})
		} else if err == nil {
			_, err = clientHub.CoreV1().Secrets(mtvNs).Update(ctx, desired, metav1.UpdateOptions{})
		}
		expectNoErr(err, "ensure provider secret for "+cluster)
	}

	patchManagedClusterAPIURL := func(ctx context.Context, name string) {
		gvr := schema.GroupVersionResource{
			Group: "cluster.open-cluster-management.io", Version: "v1", Resource: "managedclusters",
		}
		mc, err := dynamicClientHub.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		expectNoErr(err, "get ManagedCluster "+name)
		err = unstructured.SetNestedSlice(mc.Object, []interface{}{
			map[string]interface{}{"url": restConfig.Host},
		}, "spec", "managedClusterClientConfigs")
		expectNoErr(err, "patch ManagedCluster "+name+" client URL")
		_, err = dynamicClientHub.Resource(gvr).Update(ctx, mc, metav1.UpdateOptions{})
		expectNoErr(err, "update ManagedCluster "+name)
	}

	upsertKubeVirtCA := func(ctx context.Context, bundle string) {
		desired := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: kubevirtCAName, Namespace: kubevirtNs},
			Data:       map[string]string{caBundleKey: bundle},
		}
		existing, err := clientHub.CoreV1().ConfigMaps(kubevirtNs).Get(ctx, kubevirtCAName, metav1.GetOptions{})
		var prior *corev1.ConfigMap
		if err == nil {
			prior = existing.DeepCopy()
			_, err = clientHub.CoreV1().ConfigMaps(kubevirtNs).Update(ctx, desired, metav1.UpdateOptions{})
		} else if apierrors.IsNotFound(err) {
			_, err = clientHub.CoreV1().ConfigMaps(kubevirtNs).Create(ctx, desired, metav1.CreateOptions{})
			prior = nil
		}
		expectNoErr(err, "upsert kubevirt-ca ConfigMap")
		kubevirtCAPrior = prior
		kubevirtCACreated = prior == nil
	}

	postExchange := func(client *http.Client, body interface{}) (*http.Response, api.CACLMExchangeResponse) {
		if client == nil {
			client = bareHTTPClient
		}
		payload, err := json.Marshal(body)
		expectNoErr(err, "marshal CA exchange request")
		req, err := http.NewRequest(http.MethodPost, baseURL+exchangePath, bytes.NewReader(payload))
		expectNoErr(err, "build CA exchange POST")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		expectNoErr(err, "POST CA exchange")
		var decoded api.CACLMExchangeResponse
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		if len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &decoded) != nil {
			decoded = api.CACLMExchangeResponse{}
		}
		return resp, decoded
	}

	BeforeAll(func() {
		ctx := GinkgoT().Context()
		utils.Kubectl("apply", "-f", userpermissionPath)
		utils.Kubectl("apply", "-f", userpermissionKubeVirtPath)
		utils.Kubectl("apply", "-f", managedClusters)
		ensureNamespace(ctx, mtvNs)
		ensureNamespace(ctx, kubevirtNs)

		_, err := clientHub.CoreV1().ServiceAccounts(caExchangeSANamespace).Create(ctx,
			&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: caExchangeSAName}},
			metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			caExchangeSACreated = false
		} else {
			expectNoErr(err, "create CA exchange e2e ServiceAccount")
			caExchangeSACreated = true
		}

		_, err = clientHub.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: caExchangeSAName},
			Subjects: []rbacv1.Subject{{
				Kind: rbacv1.ServiceAccountKind, Name: caExchangeSAName, Namespace: caExchangeSANamespace,
			}},
			RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			caExchangeCRBCreated = false
		} else {
			expectNoErr(err, "create CA exchange e2e ClusterRoleBinding")
			caExchangeCRBCreated = true
		}

		expiry := int64(3600)
		tokenResp, err := clientHub.CoreV1().ServiceAccounts(caExchangeSANamespace).
			CreateToken(ctx, caExchangeSAName, &authv1.TokenRequest{
				Spec: authv1.TokenRequestSpec{ExpirationSeconds: &expiry},
			}, metav1.CreateOptions{})
		expectNoErr(err, "create ServiceAccount token for CA exchange e2e")
		spokeToken = tokenResp.Status.Token

		bareHTTPClient = &http.Client{Timeout: 30 * time.Second}
		authClient = &http.Client{
			Transport: &bearerRoundTripper{token: spokeToken},
			Timeout:   2 * time.Minute,
		}

		patchManagedClusterAPIURL(ctx, clusterA)
		patchManagedClusterAPIURL(ctx, clusterB)
		ensureProviderSecret(ctx, clusterA, spokeToken)
		ensureProviderSecret(ctx, clusterB, spokeToken)

		caPEM := string(generateCAPEM("cclm-ca-exchange-e2e"))
		upsertKubeVirtCA(ctx, caPEM)

		Eventually(func() bool {
			req, err := http.NewRequest(http.MethodGet, baseURL+"/health", nil)
			if err != nil {
				return false
			}
			resp, err := bareHTTPClient.Do(req)
			if err != nil {
				return false
			}
			defer func() { _ = resp.Body.Close() }()
			return resp.StatusCode == http.StatusOK
		}, 30*time.Second, 1*time.Second).Should(BeTrue(), "migration advisor /health")
	})

	AfterAll(func() {
		cleanupCtx := context.Background()
		for _, cluster := range []string{clusterA, clusterB} {
			deleteIgnoringNotFound(
				clientHub.CoreV1().Secrets(mtvNs).Delete(cleanupCtx, cluster+"-mtv", metav1.DeleteOptions{}),
				cluster+"-mtv secret",
			)
		}
		if kubevirtCAPrior != nil {
			_, restoreErr := clientHub.CoreV1().ConfigMaps(kubevirtNs).
				Update(cleanupCtx, kubevirtCAPrior, metav1.UpdateOptions{})
			deleteIgnoringNotFound(restoreErr, "restore "+kubevirtCAName)
		} else if kubevirtCACreated {
			deleteIgnoringNotFound(
				clientHub.CoreV1().ConfigMaps(kubevirtNs).Delete(cleanupCtx, kubevirtCAName, metav1.DeleteOptions{}),
				kubevirtCAName+" ConfigMap",
			)
		}
		for _, ns := range createdNamespaces {
			deleteIgnoringNotFound(
				clientHub.CoreV1().Namespaces().Delete(cleanupCtx, ns, metav1.DeleteOptions{}),
				ns+" namespace",
			)
		}
		if caExchangeCRBCreated {
			deleteIgnoringNotFound(
				clientHub.RbacV1().ClusterRoleBindings().Delete(cleanupCtx, caExchangeSAName, metav1.DeleteOptions{}),
				caExchangeSAName+" ClusterRoleBinding",
			)
		}
		if caExchangeSACreated {
			deleteIgnoringNotFound(
				clientHub.CoreV1().ServiceAccounts(caExchangeSANamespace).
					Delete(cleanupCtx, caExchangeSAName, metav1.DeleteOptions{}),
				caExchangeSAName+" ServiceAccount",
			)
		}
		utils.Kubectl("delete", "-f", managedClusters, "--ignore-not-found")
		utils.Kubectl("delete", "-f", userpermissionKubeVirtPath, "--ignore-not-found")
		utils.Kubectl("delete", "-f", userpermissionPath, "--ignore-not-found")
	})

	It("returns 405 on GET", func() {
		req, err := http.NewRequest(http.MethodGet, baseURL+exchangePath, nil)
		expectNoErr(err, "build GET CA exchange")
		resp, err := bareHTTPClient.Do(req)
		expectNoErr(err, "GET CA exchange")
		defer func() { _ = resp.Body.Close() }()
		Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed), "GET must not be allowed on CA exchange")
	})

	It("returns 401 when Authorization is missing", func() {
		resp, _ := postExchange(nil, api.CACLMExchangeRequest{ClusterA: clusterA, ClusterB: clusterB})
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized), "missing Authorization header")
	})

	It("returns 400 when clusterA and clusterB are missing", func() {
		resp, body := postExchange(authClient, map[string]string{})
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest), "empty cluster fields")
		Expect(body.Message).To(ContainSubstring("clusterA and clusterB"))
	})

	It("returns 400 when clusterA equals clusterB", func() {
		resp, body := postExchange(authClient, api.CACLMExchangeRequest{ClusterA: clusterA, ClusterB: clusterA})
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest), "same clusterA and clusterB")
		Expect(body.Message).To(ContainSubstring("must differ"))
	})

	It("returns 400 PROXY_CCLM_NOT_ENABLED when a cluster lacks acm/proxy-cclm=true", func() {
		utils.Kubectl("label", "managedcluster", clusterB, "acm/proxy-cclm-")
		defer utils.Kubectl("label", "managedcluster", clusterB, "acm/proxy-cclm=true", "--overwrite")
		resp, body := postExchange(authClient, api.CACLMExchangeRequest{ClusterA: clusterA, ClusterB: clusterB})
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest), "proxy-cclm label required")
		Expect(body.Code).To(Equal("PROXY_CCLM_NOT_ENABLED"))
		Expect(body.Message).To(ContainSubstring("acm/proxy-cclm"))
	})

	It("returns 200 CA_ALREADY_TRUSTED when both spokes share the same kubevirt-ca bundle", func() {
		resp, body := postExchange(authClient, api.CACLMExchangeRequest{
			ClusterA: clusterA,
			ClusterB: clusterB,
		})
		Expect(resp.StatusCode).To(Equal(http.StatusOK), fmt.Sprintf("body=%+v", body))
		Expect(body.Status).To(Equal("success"), "exchange status")
		Expect(body.Code).To(Equal("CA_ALREADY_TRUSTED"))
		Expect(body.ClusterA).NotTo(BeNil())
		Expect(body.ClusterB).NotTo(BeNil())
		Expect(body.ClusterA.CAVerified).To(BeTrue(), "cluster A verified")
		Expect(body.ClusterB.CAVerified).To(BeTrue(), "cluster B verified")
	})
})
