// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stolostron/mtv-integrations/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

func TestFilterClusterSCsByEligibility(t *testing.T) {
	clusterSCs := map[string][]SCProvisioner{
		"allowed-cluster": {{Name: "ceph-rbd", Provisioner: "rbd.csi.ceph.com"}},
		"blocked-cluster": {{Name: "ceph-rbd", Provisioner: "rbd.csi.ceph.com"}},
	}
	eligible := map[string]struct{}{
		"allowed-cluster": {},
	}

	filtered := filterClusterSCsByEligibility(clusterSCs, eligible)
	assert.Len(t, filtered, 1)
	assert.Contains(t, filtered, "allowed-cluster")
	assert.NotContains(t, filtered, "blocked-cluster")
}

// ── clusterDataCache ─────────────────────────────────────────────────────────

func TestClusterDataCache_MissBeforeSet(t *testing.T) {
	var c clusterDataCache
	_, _, _, hit := c.get()
	assert.False(t, hit, "fresh cache must report a miss")
}

func TestClusterDataCache_HitAfterSet(t *testing.T) {
	var c clusterDataCache
	c.ttl = 5 * time.Minute

	nodes := api.ClusterNodeMetrics{"c1": {{NodeName: "n1", AllocatableCPUCores: 4}}}
	ceph := map[string]api.CephMetrics{"c1": {TotalBytes: 100, AvailableBytes: 80}}
	scs := map[string][]SCProvisioner{"c1": {{Name: "sc1", Provisioner: "prov"}}}
	c.set(nodes, ceph, scs)

	gotNodes, gotCeph, gotSCs, hit := c.get()
	require.True(t, hit)
	assert.Equal(t, nodes, gotNodes)
	assert.Equal(t, ceph, gotCeph)
	assert.Equal(t, scs, gotSCs)
}

func TestClusterDataCache_ExpiredAfterTTL(t *testing.T) {
	var c clusterDataCache
	c.ttl = 1 * time.Millisecond
	c.set(nil, nil, nil)
	time.Sleep(5 * time.Millisecond)
	_, _, _, hit := c.get()
	assert.False(t, hit, "expired cache must report a miss")
}

func TestClusterDataCache_DefaultTTL(t *testing.T) {
	// When ttl is zero, set() applies defaultCacheTTL and the entry should be fresh.
	var c clusterDataCache
	c.set(nil, nil, nil)
	_, _, _, hit := c.get()
	assert.True(t, hit, "zero ttl should fall back to defaultCacheTTL")
}

// ── ServeHTTP ────────────────────────────────────────────────────────────────

func TestServeHTTP_MethodNotAllowed(t *testing.T) {
	h := &Handler{}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/v1/migration-targets", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
		})
	}
}

func TestServeHTTP_MissingParams(t *testing.T) {
	h := &Handler{}
	tests := []struct {
		name string
		url  string
	}{
		{"all missing", "/api/v1/migration-targets"},
		{"vmNamespace missing", "/api/v1/migration-targets?cluster=c1&vmName=vm"},
		{"vmName missing", "/api/v1/migration-targets?cluster=c1&vmNamespace=ns"},
		{"cluster missing", "/api/v1/migration-targets?vmNamespace=ns&vmName=vm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

// ── getClusterSnapshot / fetchFreshClusterData ───────────────────────────────


// newFakeSearchServer creates an httptest server mimicking the ACM Search API.
func newFakeSearchServer(t *testing.T, clusters []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		items := make([]map[string]interface{}, 0, len(clusters))
		for _, c := range clusters {
			items = append(items, map[string]interface{}{
				"cluster": c, "name": "ceph-rbd", "provisioner": "rbd.csi.ceph.com",
			})
		}
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"search": []interface{}{
					map[string]interface{}{"items": items},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("fake search: encode: %v", err)
		}
	}))
}

func buildFakeHandlerWithClusters(
	t *testing.T,
	thanosSrv *httptest.Server,
	searchSrv *httptest.Server,
	eligibleClusters ...string,
) *Handler {
	t.Helper()
	scheme := runtime.NewScheme()
	objs := make([]runtime.Object, 0, len(eligibleClusters))
	for _, name := range eligibleClusters {
		objs = append(objs, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "cluster.open-cluster-management.io/v1",
				"kind":       "ManagedCluster",
				"metadata": map[string]interface{}{
					"name":   name,
					"labels": map[string]interface{}{cnvOperatorInstallLabel: "true"},
				},
			},
		})
	}
	fakeClient := dynamicfake.NewSimpleDynamicClient(scheme, objs...)
	return &Handler{
		DynamicClient:     fakeClient,
		RestConfig:        &rest.Config{Host: thanosSrv.URL},
		ThanosHost:        thanosSrv.URL,
		SearchAPIEndpoint: searchSrv.URL + "/searchapi/graphql",
	}
}

func TestGetClusterSnapshot(t *testing.T) {
	thanosSrv := newFakeThanosServer(t)
	defer thanosSrv.Close()
	searchSrv := newFakeSearchServer(t, []string{"target-cluster", "untarget-cluster"})
	defer searchSrv.Close()

	h := buildFakeHandlerWithClusters(t, thanosSrv, searchSrv, "target-cluster", "untarget-cluster")

	snap, err := h.getClusterSnapshot(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, snap.nodes)
	assert.NotEmpty(t, snap.scs)
}

func TestGetClusterSnapshot_CacheHit(t *testing.T) {
	thanosSrv := newFakeThanosServer(t)
	defer thanosSrv.Close()
	searchSrv := newFakeSearchServer(t, []string{"target-cluster"})
	defer searchSrv.Close()

	h := buildFakeHandlerWithClusters(t, thanosSrv, searchSrv, "target-cluster")
	h.CacheTTL = 5 * time.Minute

	// First call: populates cache.
	_, err := h.getClusterSnapshot(context.Background())
	require.NoError(t, err)

	// Second call: must be a cache hit (no new HTTP calls).
	// We close both servers to prove no outbound requests are made.
	thanosSrv.Close()
	searchSrv.Close()
	_, err = h.getClusterSnapshot(context.Background())
	require.NoError(t, err)
}

func TestGetClusterSnapshot_ThanosError(t *testing.T) {
	// Thanos server always returns 500.
	thanosSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer thanosSrv.Close()
	searchSrv := newFakeSearchServer(t, []string{"c1"})
	defer searchSrv.Close()

	h := buildFakeHandlerWithClusters(t, thanosSrv, searchSrv, "c1")
	_, err := h.getClusterSnapshot(context.Background())
	assert.Error(t, err)
}

func TestListEligibleManagedClusters(t *testing.T) {
	scheme := runtime.NewScheme()
	// Only seed cluster-enabled — simulates what the apiserver returns after
	// applying the label selector acm/cnv-operator-install=true.
	fakeClient := dynamicfake.NewSimpleDynamicClient(
		scheme,
		&unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "cluster.open-cluster-management.io/v1",
				"kind":       "ManagedCluster",
				"metadata": map[string]interface{}{
					"name": "cluster-enabled",
					"labels": map[string]interface{}{
						cnvOperatorInstallLabel: "true",
					},
				},
			},
		},
	)

	h := &Handler{DynamicClient: fakeClient}
	eligible, err := h.listEligibleManagedClusters(context.Background())
	assert.NoError(t, err)
	assert.Len(t, eligible, 1)
	assert.Contains(t, eligible, "cluster-enabled")
}

// ── evaluate / ServeHTTP full-pipeline ───────────────────────────────────────

// mcvGR is the GroupResource used when constructing fake apierrors for the
// managedclusterviews resource.
var mcvGR = schema.GroupResource{
	Group:    managedClusterViewGVR.Group,
	Resource: managedClusterViewGVR.Resource,
}

// dummyManagedCluster is a placeholder ManagedCluster added to the fake dynamic
// client so that dynamicfake can infer the ManagedClusterList kind and avoid
// panicking when listEligibleManagedClusters calls List.
var dummyManagedCluster = &unstructured.Unstructured{
	Object: map[string]interface{}{
		"apiVersion": "cluster.open-cluster-management.io/v1",
		"kind":       "ManagedCluster",
		"metadata":   map[string]interface{}{"name": "placeholder"},
	},
}

// newFakeClientWithMCV creates a fake dynamic client that already knows the
// ManagedCluster list kind (preventing a panic in listEligibleManagedClusters)
// and applies the given reactors on top.
func newFakeClientWithMCV(reactors ...k8stesting.ReactionFunc) *dynamicfake.FakeDynamicClient {
	c := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), dummyManagedCluster)
	for _, r := range reactors {
		c.PrependReactor("create", "managedclusterviews", r)
	}
	return c
}

// TestServeHTTP_EvaluateVMNotFound verifies that when the ManagedClusterView
// Create returns NotFound (cluster namespace absent on hub), ServeHTTP returns
// HTTP 400 Bad Request (not 500).
func TestServeHTTP_EvaluateVMNotFound(t *testing.T) {
	thanosSrv := newFakeThanosServer(t)
	defer thanosSrv.Close()
	searchSrv := newFakeSearchServer(t, []string{})
	defer searchSrv.Close()
	authSrv := newFakeUserPermissionServer(t, "test-token", // fleet admin
		map[string][]string{advisorRoleVMFleetAdmin(): {hubClusterName}})
	defer authSrv.Close()

	fakeClient := newFakeClientWithMCV(
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewNotFound(mcvGR, "nonexistent-cluster")
		},
	)

	h := &Handler{
		DynamicClient:     fakeClient,
		RestConfig:        &rest.Config{Host: authSrv.URL},
		ThanosHost:        thanosSrv.URL,
		SearchAPIEndpoint: searchSrv.URL + "/searchapi/graphql",
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/migration-targets?cluster=nonexistent-cluster&vmNamespace=default&vmName=vm1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "not found")
}

// TestServeHTTP_EvaluateServerError verifies that a generic (non-VMNotFound)
// error from Branch A causes ServeHTTP to return HTTP 500.
func TestServeHTTP_EvaluateServerError(t *testing.T) {
	thanosSrv := newFakeThanosServer(t)
	defer thanosSrv.Close()
	searchSrv := newFakeSearchServer(t, []string{})
	defer searchSrv.Close()
	authSrv := newFakeUserPermissionServer(t, "test-token", // fleet admin
		map[string][]string{advisorRoleVMFleetAdmin(): {hubClusterName}})
	defer authSrv.Close()

	fakeClient := newFakeClientWithMCV(
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, fmt.Errorf("hub API server unavailable")
		},
	)

	h := &Handler{
		DynamicClient:     fakeClient,
		RestConfig:        &rest.Config{Host: authSrv.URL},
		ThanosHost:        thanosSrv.URL,
		SearchAPIEndpoint: searchSrv.URL + "/searchapi/graphql",
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/migration-targets?cluster=c1&vmNamespace=default&vmName=vm1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestServeHTTP_EvaluateSuccess verifies that a complete evaluate round-trip
// (VMI watch result + Thanos + Search) returns HTTP 200 with a JSON body
// containing the source VM info.
func TestServeHTTP_EvaluateSuccess(t *testing.T) {
	thanosSrv := newFakeThanosServer(t)
	defer thanosSrv.Close()
	searchSrv := newFakeSearchServer(t, []string{})
	defer searchSrv.Close()
	authSrv := newFakeUserPermissionServer(t, "test-token", // fleet admin
		map[string][]string{advisorRoleVMFleetAdmin(): {hubClusterName}})
	defer authSrv.Close()

	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), dummyManagedCluster)
	injectWatchEvent(fakeClient, mcvWithResult(map[string]interface{}{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachineInstance",
		"metadata":   map[string]interface{}{"name": "vm1", "namespace": "default"},
	}))

	h := &Handler{
		DynamicClient:     fakeClient,
		RestConfig:        &rest.Config{Host: authSrv.URL},
		ThanosHost:        thanosSrv.URL,
		SearchAPIEndpoint: searchSrv.URL + "/searchapi/graphql",
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/migration-targets?cluster=c1&vmNamespace=default&vmName=vm1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	var resp api.MigrationTargetResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "vm1", resp.SourceVM.Name)
	assert.Equal(t, "default", resp.SourceVM.Namespace)
	assert.Equal(t, "c1", resp.SourceVM.Cluster)
}
