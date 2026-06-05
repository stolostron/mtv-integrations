// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stolostron/mtv-integrations/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

// ── helpers ────────────────────────────────────────────────────────────────────

// fakeNodeResponse returns a Prometheus vector response with two cluster/node rows.
//
//nolint:unparam
func fakeNodeResponse(c1, n1, v1, c2, n2, v2 string) string {
	return `{"status":"` + promStatusSuccess + `","data":{"resultType":"vector","result":[` +
		`{"metric":{"cluster":"` + c1 + `","node":"` + n1 + `"},"value":[1234567890,"` + v1 + `"]},` +
		`{"metric":{"cluster":"` + c2 + `","node":"` + n2 + `"},"value":[1234567890,"` + v2 + `"]}` +
		`]}}`
}

// fakeClusterResponse returns a Prometheus vector response with two cluster-level rows.
//
//nolint:unparam
func fakeClusterResponse(c1, v1, c2, v2 string) string {
	return `{"status":"` + promStatusSuccess + `","data":{"resultType":"vector","result":[` +
		`{"metric":{"cluster":"` + c1 + `"},"value":[1234567890,"` + v1 + `"]},` +
		`{"metric":{"cluster":"` + c2 + `"},"value":[1234567890,"` + v2 + `"]}` +
		`]}}`
}

// newFakeThanosServer creates an httptest.Server that mimics the Thanos Query
// Frontend, returning the same responses as test/utils/fake-thanos-server.
func newFakeThanosServer(t *testing.T) *httptest.Server {
	t.Helper()
	const (
		c1 = "target-cluster"
		n1 = "node1"
		c2 = "untarget-cluster"
		n2 = "node2"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query := r.URL.Query().Get("query")
		body := `{"status":"success","data":{"resultType":"vector","result":[]}}`
		switch {
		case query == "up":
			body = fakeNodeResponse(c1, n1, "1", c2, n2, "1")
		case strings.Contains(query, "kube_node_labels"):
			body = fakeNodeResponse(c1, n1, "1", c2, n2, "1")
		case strings.Contains(query, `kube_node_status_allocatable{resource="cpu"}`):
			body = fakeNodeResponse(c1, n1, "8", c2, n2, "2")
		case strings.Contains(query, `kube_node_status_allocatable{resource="memory"}`):
			body = fakeNodeResponse(c1, n1, "17179869184", c2, n2, "4294967296")
		case strings.Contains(query, `kube_pod_container_resource_requests{resource="cpu"}`):
			body = fakeNodeResponse(c1, n1, "1", c2, n2, "3")
		case strings.Contains(query, `kube_pod_container_resource_requests{resource="memory"}`):
			body = fakeNodeResponse(c1, n1, "2147483648", c2, n2, "5368709120")
		case strings.Contains(query, `kube_pod_container_resource_requests:sum{resource="cpu"}`):
			body = fakeClusterResponse(c1, "1", c2, "3")
		case strings.Contains(query, `kube_pod_container_resource_requests:sum{resource="memory"}`):
			body = fakeClusterResponse(c1, "2147483648", c2, "5368709120")
		case strings.Contains(query, "ceph_cluster_total_bytes"):
			body = fakeClusterResponse(c1, "107374182400", c2, "42949672960")
		case strings.Contains(query, "ceph_cluster_total_avail_bytes"):
			body = fakeClusterResponse(c1, "75161927680", c2, "5368709120")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	return srv
}

// newObsClient builds an ObservabilityClient wired to an httptest server.
func newObsClient(serverURL string) *ObservabilityClient {
	return &ObservabilityClient{
		RestConfig: &rest.Config{Host: serverURL},
		ThanosHost: serverURL,
	}
}

// ── metricValue ─────────────────────────────────────────────────────────────

func TestMetricValue(t *testing.T) {
	tests := []struct {
		name    string
		row     [2]interface{}
		want    float64
		wantErr bool
	}{
		{"float string", [2]interface{}{float64(1e9), "42.5"}, 42.5, false},
		{"integer string", [2]interface{}{float64(1e9), "0"}, 0, false},
		{"not a string", [2]interface{}{float64(1e9), 99.0}, 0, true},
		{"invalid number", [2]interface{}{float64(1e9), "not-a-num"}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := metricValue(tt.row)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.InDelta(t, tt.want, got, 0.001)
			}
		})
	}
}

// ── applyMetric ─────────────────────────────────────────────────────────────

func TestApplyMetric(t *testing.T) {
	schedulable := map[nodeKey]bool{
		{cluster: "c1", node: "n1"}: true,
	}
	resp := makePromResp([]promRow{
		{cluster: "c1", node: "n1", value: "10.5"},
		{cluster: "c2", node: "n2", value: "20.0"}, // not schedulable — ignored
		{cluster: "c1", node: "n1", value: "bad"},  // invalid value — ignored
	})

	var results []float64
	applyMetric(resp, schedulable, func(_ nodeKey, v float64) { results = append(results, v) })
	require.Len(t, results, 1)
	assert.InDelta(t, 10.5, results[0], 0.001)
}

func TestApplyMetric_NilResp(t *testing.T) {
	applyMetric(nil, nil, func(_ nodeKey, _ float64) { t.Fatal("should not be called") })
}

// ── applySNOMetric ───────────────────────────────────────────────────────────

func TestApplySNOMetric(t *testing.T) {
	clusterNodes := map[string][]string{"c1": {"n1"}}
	nodeMap := map[nodeKey]*api.NodeMetrics{
		{cluster: "c1", node: "n1"}: {NodeName: "n1"},
	}

	resp := makeClusterPromResp([]clusterRow{
		{cluster: "c1", value: "5.0"},
		{cluster: "c2", value: "3.0"}, // cluster not in clusterNodes — ignored
	})

	var got float64
	covered := map[string]bool{}
	applySNOMetric(resp, covered, clusterNodes, nodeMap,
		func(nm *api.NodeMetrics, v float64) { got = v })
	assert.InDelta(t, 5.0, got, 0.001)

	// Already covered — skip
	got = 0
	covered["c1"] = true
	applySNOMetric(resp, covered, clusterNodes, nodeMap,
		func(nm *api.NodeMetrics, v float64) { got = v })
	assert.InDelta(t, 0, got, 0.001)
}

func TestApplySNOMetric_NilResp(t *testing.T) {
	applySNOMetric(nil, nil, nil, nil, func(_ *api.NodeMetrics, _ float64) {
		t.Fatal("should not be called")
	})
}

func TestApplySNOMetric_MultipleNodes(t *testing.T) {
	// Cluster with >1 node is not SNO — skip.
	clusterNodes := map[string][]string{"c1": {"n1", "n2"}}
	nodeMap := map[nodeKey]*api.NodeMetrics{
		{cluster: "c1", node: "n1"}: {NodeName: "n1"},
	}
	resp := makeClusterPromResp([]clusterRow{{cluster: "c1", value: "9.0"}})
	applySNOMetric(resp, map[string]bool{}, clusterNodes, nodeMap,
		func(_ *api.NodeMetrics, _ float64) { t.Fatal("multi-node cluster must be skipped") })
}

func TestApplySNOMetric_BadValue(t *testing.T) {
	clusterNodes := map[string][]string{"c1": {"n1"}}
	nodeMap := map[nodeKey]*api.NodeMetrics{
		{cluster: "c1", node: "n1"}: {NodeName: "n1"},
	}
	resp := makeClusterPromResp([]clusterRow{{cluster: "c1", value: "not-a-number"}})
	applySNOMetric(resp, map[string]bool{}, clusterNodes, nodeMap,
		func(_ *api.NodeMetrics, _ float64) { t.Fatal("bad value must be skipped") })
}

// ── baseURL ─────────────────────────────────────────────────────────────────

func TestObservabilityClientBaseURL_Default(t *testing.T) {
	c := &ObservabilityClient{RestConfig: &rest.Config{Host: "http://unused"}}
	assert.Equal(t, thanosQueryFrontend, c.baseURL())
}

func TestObservabilityClientBaseURL_Override(t *testing.T) {
	c := &ObservabilityClient{
		RestConfig: &rest.Config{Host: "http://unused"},
		ThanosHost: "http://custom-thanos:9090",
	}
	assert.Equal(t, "http://custom-thanos:9090", c.baseURL())
}

// ── CheckHealth ─────────────────────────────────────────────────────────────

func TestObservabilityClientCheckHealth_OK(t *testing.T) {
	srv := newFakeThanosServer(t)
	defer srv.Close()

	err := newObsClient(srv.URL).CheckHealth(context.Background())
	assert.NoError(t, err)
}

func TestObservabilityClientCheckHealth_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("down"))
	}))
	defer srv.Close()

	err := newObsClient(srv.URL).CheckHealth(context.Background())
	assert.Error(t, err)
}

func TestObservabilityClientCheckHealth_ThanosError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"error","error":"something broke"}`))
	}))
	defer srv.Close()

	err := newObsClient(srv.URL).CheckHealth(context.Background())
	assert.Error(t, err)
}

// ── FetchCephMetrics ────────────────────────────────────────────────────────

func TestFetchCephMetrics(t *testing.T) {
	srv := newFakeThanosServer(t)
	defer srv.Close()

	result, err := newObsClient(srv.URL).FetchCephMetrics(context.Background())
	require.NoError(t, err)
	require.Contains(t, result, "target-cluster")
	assert.Equal(t, int64(107374182400), result["target-cluster"].TotalBytes)
	assert.Equal(t, int64(75161927680), result["target-cluster"].AvailableBytes)
}

func TestFetchCephMetrics_TotalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newObsClient(srv.URL).FetchCephMetrics(context.Background())
	assert.Error(t, err)
}

func TestFetchCephMetrics_AvailError(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			// First call (ceph_cluster_total_bytes) succeeds.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
				`{"metric":{"cluster":"c1"},"value":[0,"100"]}]}}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newObsClient(srv.URL).FetchCephMetrics(context.Background())
	assert.Error(t, err)
}

func TestFetchCephMetrics_EmptyCluster(t *testing.T) {
	// Items without a cluster label are silently skipped.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
			`{"metric":{},"value":[0,"100"]}]}}`))
	}))
	defer srv.Close()

	result, err := newObsClient(srv.URL).FetchCephMetrics(context.Background())
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestFetchCephMetrics_BadValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
			`{"metric":{"cluster":"c1"},"value":[0,"not-a-number"]}]}}`))
	}))
	defer srv.Close()

	result, err := newObsClient(srv.URL).FetchCephMetrics(context.Background())
	require.NoError(t, err)
	// Row with bad value is silently skipped; cluster not added.
	assert.Empty(t, result)
}

// ── FetchNodeMetrics ────────────────────────────────────────────────────────

func TestFetchNodeMetrics(t *testing.T) {
	srv := newFakeThanosServer(t)
	defer srv.Close()

	result, err := newObsClient(srv.URL).FetchNodeMetrics(context.Background())
	require.NoError(t, err)
	require.Contains(t, result, "target-cluster")

	nodes := result["target-cluster"]
	require.Len(t, nodes, 1)
	n := nodes[0]
	assert.Equal(t, "node1", n.NodeName)
	assert.InDelta(t, 8.0, n.AllocatableCPUCores, 0.001)
	assert.Equal(t, int64(17179869184), n.AllocatableMemBytes)
	assert.InDelta(t, 1.0, n.RequestedCPUCores, 0.001)
	assert.Equal(t, int64(2147483648), n.RequestedMemBytes)
}

func TestFetchNodeMetrics_EmptySchedulable(t *testing.T) {
	// When the schedulable-node query returns no rows, FetchNodeMetrics returns
	// an empty map (the 0-node check is in fetchFreshClusterData, not here).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	result, err := newObsClient(srv.URL).FetchNodeMetrics(context.Background())
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestFetchNodeMetrics_SchedulableQueryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newObsClient(srv.URL).FetchNodeMetrics(context.Background())
	assert.Error(t, err)
}

func TestFetchNodeMetrics_SkipsEmptyLabels(t *testing.T) {
	// Rows missing cluster or node label are silently skipped.
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		call++
		if call == 1 {
			// schedulable query: one valid row + one with empty cluster + one with empty node
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
				`{"metric":{"cluster":"c1","node":"n1"},"value":[0,"1"]},` +
				`{"metric":{"cluster":"","node":"n2"},"value":[0,"1"]},` +
				`{"metric":{"cluster":"c1","node":""},"value":[0,"1"]}` +
				`]}}`))
			return
		}
		// all other queries: empty result
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	result, err := newObsClient(srv.URL).FetchNodeMetrics(context.Background())
	require.NoError(t, err)
	require.Contains(t, result, "c1")
	assert.Len(t, result["c1"], 1)
}

// ── httpClient caching (sync.Once) ──────────────────────────────────────────

func TestObservabilityClientHTTPClientCached(t *testing.T) {
	c := newObsClient("http://unused")
	hc1, err1 := c.httpClient()
	hc2, err2 := c.httpClient()
	assert.NoError(t, err1)
	assert.NoError(t, err2)
	assert.Same(t, hc1, hc2, "httpClient must return the same instance")
}

// TestObservabilityClient_ServiceCAPath_Default verifies that serviceCAPath()
// falls back to DefaultServiceCAPath when ServiceCAPath is empty.
func TestObservabilityClient_ServiceCAPath_Default(t *testing.T) {
	c := &ObservabilityClient{}
	assert.Equal(t, DefaultServiceCAPath, c.serviceCAPath())
}

// TestObservabilityClient_ServiceCAPath_Override verifies that an explicit
// ServiceCAPath is returned unchanged.
func TestObservabilityClient_ServiceCAPath_Override(t *testing.T) {
	c := &ObservabilityClient{ServiceCAPath: "/custom/ca.crt"}
	assert.Equal(t, "/custom/ca.crt", c.serviceCAPath())
}

// ── helpers for building fake promResponse structs ───────────────────────────

type promRow struct{ cluster, node, value string }
type clusterRow struct{ cluster, value string }

func makePromResp(rows []promRow) *promResponse {
	r := &promResponse{Status: promStatusSuccess}
	for _, row := range rows {
		r.Data.Result = append(r.Data.Result, struct {
			Metric map[string]string `json:"metric"`
			Value  [2]interface{}    `json:"value"`
		}{
			Metric: map[string]string{"cluster": row.cluster, "node": row.node},
			Value:  [2]interface{}{float64(0), row.value},
		})
	}
	return r
}

func makeClusterPromResp(rows []clusterRow) *promResponse {
	r := &promResponse{Status: promStatusSuccess}
	for _, row := range rows {
		r.Data.Result = append(r.Data.Result, struct {
			Metric map[string]string `json:"metric"`
			Value  [2]interface{}    `json:"value"`
		}{
			Metric: map[string]string{"cluster": row.cluster},
			Value:  [2]interface{}{float64(0), row.value},
		})
	}
	return r
}
