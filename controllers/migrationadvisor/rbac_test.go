// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// newFakeUserPermissionServer creates an httptest server that handles
// GET /apis/clusterview.open-cluster-management.io/v1alpha1/userpermissions/<name>.
//
// roleBindings maps each role name to the cluster names that appear in its
// status.bindings. A role not present in the map returns an empty binding.
func newFakeUserPermissionServer(t *testing.T, roleBindings map[string][]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/apis/clusterview.open-cluster-management.io/v1alpha1/userpermissions/"
		if r.Method != http.MethodGet || len(r.URL.Path) <= len(prefix) {
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		roleName := r.URL.Path[len(prefix):]
		var bindings []interface{}
		for _, cluster := range roleBindings[roleName] {
			bindings = append(bindings, map[string]interface{}{
				"cluster":    cluster,
				"namespaces": []interface{}{"*"},
			})
		}
		resp := map[string]interface{}{
			"apiVersion": "clusterview.open-cluster-management.io/v1alpha1",
			"kind":       "UserPermission",
			"metadata":   map[string]interface{}{"name": roleName},
			"status":     map[string]interface{}{"bindings": bindings},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("fake UP server: encode: %v", err)
		}
	}))
}

// newFakeAuthHandlerWithMCV builds a Handler pointing at the given fake
// UserPermission server. The handler uses fakeClient as its controller
// DynamicClient and the fake servers for Thanos/Search.
func newFakeAuthHandlerWithMCV(
	t *testing.T,
	upSrv *httptest.Server,
	fakeClient *dynamicfake.FakeDynamicClient,
	thanosSrv *httptest.Server,
	searchSrv *httptest.Server,
) *Handler {
	t.Helper()
	return &Handler{
		DynamicClient:     fakeClient,
		RestConfig:        &rest.Config{Host: upSrv.URL},
		ThanosHost:        thanosSrv.URL,
		SearchAPIEndpoint: searchSrv.URL + "/searchapi/graphql",
	}
}

// newCNVClusterFakeClient creates a fake dynamic client seeded with a
// ManagedCluster that carries the CNV install label.
func newCNVClusterFakeClient(
	clusterName string,
	extraReactors ...k8stesting.ReactionFunc,
) *dynamicfake.FakeDynamicClient {
	cnvCluster := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cluster.open-cluster-management.io/v1",
			"kind":       "ManagedCluster",
			"metadata": map[string]interface{}{
				"name":   clusterName,
				"labels": map[string]interface{}{cnvOperatorInstallLabel: "true"},
			},
		},
	}
	scheme := runtime.NewScheme()
	c := dynamicfake.NewSimpleDynamicClient(scheme, cnvCluster, dummyManagedCluster)
	for _, r := range extraReactors {
		c.PrependReactor("create", "managedclusterviews", r)
	}
	return c
}

// ── ServeHTTP RBAC gate ───────────────────────────────────────────────────────

// TestServeHTTP_RBAC_NoToken verifies that a request without an Authorization
// header receives HTTP 401 Unauthorized.
func TestServeHTTP_RBAC_NoToken(t *testing.T) {
	h := &Handler{RestConfig: &rest.Config{Host: "http://unused"}}
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/migration-targets?cluster=c1&vmNamespace=ns&vmName=vm", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "authorization required")
}

// TestServeHTTP_RBAC_Denied verifies that a caller without acm-vm-fleet:admin
// or managedcluster:admin for all CNV clusters receives HTTP 403.
func TestServeHTTP_RBAC_Denied(t *testing.T) {
	thanosSrv := newFakeThanosServer(t)
	defer thanosSrv.Close()
	searchSrv := newFakeSearchServer(t, []string{})
	defer searchSrv.Close()

	// No bindings for either role → denied.
	upSrv := newFakeUserPermissionServer(t, nil)
	defer upSrv.Close()

	// CNV cluster exists so the cluster-admin path is reachable but fails.
	fakeClient := newCNVClusterFakeClient("spoke01")
	h := newFakeAuthHandlerWithMCV(t, upSrv, fakeClient, thanosSrv, searchSrv)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/migration-targets?cluster=c1&vmNamespace=ns&vmName=vm", nil)
	req.Header.Set("Authorization", "Bearer unprivileged-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "forbidden")
}

// TestServeHTTP_RBAC_VMFleetAdminAllowed verifies that a caller whose
// acm-vm-fleet:admin UserPermission contains a local-cluster binding proceeds
// past the RBAC gate.
func TestServeHTTP_RBAC_VMFleetAdminAllowed(t *testing.T) {
	thanosSrv := newFakeThanosServer(t)
	defer thanosSrv.Close()
	searchSrv := newFakeSearchServer(t, []string{})
	defer searchSrv.Close()

	// acm-vm-fleet:admin has local-cluster binding → fleet admin.
	upSrv := newFakeUserPermissionServer(t, map[string][]string{
		advisorRoleVMFleetAdmin(): {hubClusterName},
	})
	defer upSrv.Close()

	fakeClient := newFakeClientWithMCV(
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewNotFound(mcvGR, "c1")
		},
	)
	h := newFakeAuthHandlerWithMCV(t, upSrv, fakeClient, thanosSrv, searchSrv)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/migration-targets?cluster=c1&vmNamespace=ns&vmName=vm", nil)
	req.Header.Set("Authorization", "Bearer fleet-admin-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusUnauthorized, w.Code)
	assert.NotEqual(t, http.StatusForbidden, w.Code)
}

// TestServeHTTP_RBAC_ClusterAdminAllowed verifies that a caller whose
// managedcluster:admin UserPermission covers ALL CNV-enabled managed clusters
// proceeds past the RBAC gate (cluster-admin path).
func TestServeHTTP_RBAC_ClusterAdminAllowed(t *testing.T) {
	thanosSrv := newFakeThanosServer(t)
	defer thanosSrv.Close()
	searchSrv := newFakeSearchServer(t, []string{})
	defer searchSrv.Close()

	// acm-vm-fleet:admin → no local-cluster (not fleet admin).
	// managedcluster:admin → covers "spoke01" (the only CNV cluster).
	upSrv := newFakeUserPermissionServer(t, map[string][]string{
		advisorRoleManagedClusterAdmin(): {"spoke01", hubClusterName},
	})
	defer upSrv.Close()

	// Controller sees "spoke01" as the only CNV cluster.
	fakeClient := newCNVClusterFakeClient("spoke01",
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewNotFound(mcvGR, "c1")
		},
	)
	h := newFakeAuthHandlerWithMCV(t, upSrv, fakeClient, thanosSrv, searchSrv)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/migration-targets?cluster=c1&vmNamespace=ns&vmName=vm", nil)
	req.Header.Set("Authorization", "Bearer cluster-admin-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusUnauthorized, w.Code)
	assert.NotEqual(t, http.StatusForbidden, w.Code)
}

// TestServeHTTP_RBAC_ClusterAdminDenied_MissingCluster verifies that a caller
// whose managedcluster:admin does NOT cover all CNV clusters is denied.
func TestServeHTTP_RBAC_ClusterAdminDenied_MissingCluster(t *testing.T) {
	thanosSrv := newFakeThanosServer(t)
	defer thanosSrv.Close()
	searchSrv := newFakeSearchServer(t, []string{})
	defer searchSrv.Close()

	// managedcluster:admin covers only "spoke01", but "spoke02" also has CNV.
	upSrv := newFakeUserPermissionServer(t, map[string][]string{
		advisorRoleManagedClusterAdmin(): {"spoke01"},
	})
	defer upSrv.Close()

	fakeClient := newCNVClusterFakeClient("spoke02")
	h := newFakeAuthHandlerWithMCV(t, upSrv, fakeClient, thanosSrv, searchSrv)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/migration-targets?cluster=c1&vmNamespace=ns&vmName=vm", nil)
	req.Header.Set("Authorization", "Bearer partial-admin-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestServeHTTP_RBAC_AuthServerError verifies that when the UserPermission
// lookup fails with an unexpected error, ServeHTTP returns HTTP 500.
func TestServeHTTP_RBAC_AuthServerError(t *testing.T) {
	badAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer badAuth.Close()

	h := &Handler{
		DynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
		RestConfig:    &rest.Config{Host: badAuth.URL},
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/migration-targets?cluster=c1&vmNamespace=ns&vmName=vm", nil)
	req.Header.Set("Authorization", "Bearer any-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ── userPermissionCoversHub unit tests ───────────────────────────────────────

func TestUserPermissionCoversHub_LocalClusterBinding(t *testing.T) {
	upSrv := newFakeUserPermissionServer(t, map[string][]string{
		advisorRoleVMFleetAdmin(): {hubClusterName},
	})
	defer upSrv.Close()

	dynClient, err := dynamic.NewForConfig(&rest.Config{Host: upSrv.URL})
	require.NoError(t, err)

	ok, err := userPermissionCoversHub(context.Background(), dynClient)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestUserPermissionCoversHub_NoLocalClusterBinding(t *testing.T) {
	// acm-vm-fleet:admin exists but only for a spoke cluster.
	upSrv := newFakeUserPermissionServer(t, map[string][]string{
		advisorRoleVMFleetAdmin(): {"spoke01"},
	})
	defer upSrv.Close()

	dynClient, err := dynamic.NewForConfig(&rest.Config{Host: upSrv.URL})
	require.NoError(t, err)

	ok, err := userPermissionCoversHub(context.Background(), dynClient)
	require.NoError(t, err)
	assert.False(t, ok, "spoke-only binding should not grant advisor access")
}

func TestUserPermissionCoversHub_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	dynClient, err := dynamic.NewForConfig(&rest.Config{Host: srv.URL})
	require.NoError(t, err)

	ok, err := userPermissionCoversHub(context.Background(), dynClient)
	require.NoError(t, err)
	assert.False(t, ok)
}

// ── callerIsClusterAdmin unit tests ──────────────────────────────────────────

func TestCallerIsClusterAdmin_AllCNVClustersCovers(t *testing.T) {
	// Controller sees "spoke01" as the CNV cluster.
	controllerClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(),
		&unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "cluster.open-cluster-management.io/v1",
				"kind":       "ManagedCluster",
				"metadata": map[string]interface{}{
					"name":   "spoke01",
					"labels": map[string]interface{}{cnvOperatorInstallLabel: "true"},
				},
			},
		},
	)

	// Caller's managedcluster:admin covers "spoke01".
	upSrv := newFakeUserPermissionServer(t, map[string][]string{
		advisorRoleManagedClusterAdmin(): {"spoke01", hubClusterName},
	})
	defer upSrv.Close()

	callerClient, err := dynamic.NewForConfig(&rest.Config{Host: upSrv.URL})
	require.NoError(t, err)

	ok, err := callerIsClusterAdmin(context.Background(), controllerClient, callerClient)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestCallerIsClusterAdmin_MissingOneCluster(t *testing.T) {
	// Controller sees "spoke01" and "spoke02".
	controllerClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(),
		&unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "cluster.open-cluster-management.io/v1",
				"kind":       "ManagedCluster",
				"metadata": map[string]interface{}{
					"name":   "spoke01",
					"labels": map[string]interface{}{cnvOperatorInstallLabel: "true"},
				},
			},
		},
		&unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "cluster.open-cluster-management.io/v1",
				"kind":       "ManagedCluster",
				"metadata": map[string]interface{}{
					"name":   "spoke02",
					"labels": map[string]interface{}{cnvOperatorInstallLabel: "true"},
				},
			},
		},
	)

	// Caller's managedcluster:admin covers only "spoke01" — missing "spoke02".
	upSrv := newFakeUserPermissionServer(t, map[string][]string{
		advisorRoleManagedClusterAdmin(): {"spoke01"},
	})
	defer upSrv.Close()

	callerClient, err := dynamic.NewForConfig(&rest.Config{Host: upSrv.URL})
	require.NoError(t, err)

	ok, err := callerIsClusterAdmin(context.Background(), controllerClient, callerClient)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCallerIsClusterAdmin_NoCNVClusters(t *testing.T) {
	// No CNV clusters → deny access.
	// Seed with dummyManagedCluster so the fake knows the list type, then
	// override with a reactor that returns an empty list.
	controllerClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), dummyManagedCluster)
	controllerClient.PrependReactor("list", "managedclusters",
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			list := &unstructured.UnstructuredList{}
			list.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   managedClusterGVR.Group,
				Version: "v1",
				Kind:    "ManagedClusterList",
			})
			return true, list, nil
		},
	)

	upSrv := newFakeUserPermissionServer(t, map[string][]string{
		advisorRoleManagedClusterAdmin(): {hubClusterName},
	})
	defer upSrv.Close()

	callerClient, err := dynamic.NewForConfig(&rest.Config{Host: upSrv.URL})
	require.NoError(t, err)

	ok, err := callerIsClusterAdmin(context.Background(), controllerClient, callerClient)
	require.NoError(t, err)
	assert.False(t, ok)
}

// ── listCNVManagedClusters unit tests ────────────────────────────────────────

func TestListCNVManagedClusters(t *testing.T) {
	cnvCluster := &unstructured.Unstructured{}
	cnvCluster.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   managedClusterGVR.Group,
		Version: "v1",
		Kind:    "ManagedCluster",
	})
	cnvCluster.SetName("spoke01")
	cnvCluster.SetLabels(map[string]string{cnvOperatorInstallLabel: "true"})

	nonCNV := &unstructured.Unstructured{}
	nonCNV.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   managedClusterGVR.Group,
		Version: "v1",
		Kind:    "ManagedCluster",
	})
	nonCNV.SetName("spoke02")

	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), cnvCluster, nonCNV, dummyManagedCluster)

	// Override list to apply label filtering (fake client ignores LabelSelector).
	fakeClient.PrependReactor("list", "managedclusters",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			listAction := action.(k8stesting.ListAction)
			restriction := listAction.GetListRestrictions()
			selector := restriction.Labels.String()

			list := &unstructured.UnstructuredList{}
			list.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   managedClusterGVR.Group,
				Version: "v1",
				Kind:    "ManagedClusterList",
			})
			if selector == cnvOperatorInstallLabel+"=true" {
				list.Items = []unstructured.Unstructured{*cnvCluster}
			}
			return true, list, nil
		},
	)

	clusters, err := listCNVManagedClusters(context.Background(), fakeClient)
	require.NoError(t, err)
	assert.Len(t, clusters, 1)
	assert.Contains(t, clusters, "spoke01")
	assert.NotContains(t, clusters, "spoke02")
}
