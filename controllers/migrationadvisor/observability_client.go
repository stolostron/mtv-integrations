// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/stolostron/mtv-integrations/api"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/rest"
)

const (
	// thanosQueryFrontend is the in-cluster Thanos Query Frontend from MCO.
	// Port 9090 is plain HTTP; TLS is only used for the external rbac-query-proxy Route.
	thanosQueryFrontend = "http://observability-thanos-query-frontend.open-cluster-management-observability.svc:9090"

	// promStatusSuccess is the expected status value in a successful Prometheus API response.
	promStatusSuccess = "success"
)

// nodeKey identifies a node within a specific managed cluster.
type nodeKey struct{ cluster, node string }

// nodeMetricResponses holds the raw Prometheus responses from the 6 parallel
// node-metric queries fired by runParallelNodeQueries.
type nodeMetricResponses struct {
	cpuAlloc, memAlloc *promResponse
	cpuReq, cpuReqSNO  *promResponse
	memReq, memReqSNO  *promResponse
}

// applyMetric walks resp and calls fn for every row that belongs to a schedulable node.
func applyMetric(resp *promResponse, schedulableSet map[nodeKey]bool, fn func(k nodeKey, v float64)) {
	if resp == nil {
		return
	}
	for _, row := range resp.Data.Result {
		k := nodeKey{row.Metric["cluster"], row.Metric["node"]}
		if !schedulableSet[k] {
			continue
		}
		if val, err := metricValue(row.Value); err == nil {
			fn(k, val)
		}
	}
}

// applySNOMetric handles the SNO fallback path: it applies fn to the single
// schedulable node of each cluster whose data is not already in covered.
func applySNOMetric(
	resp *promResponse,
	covered map[string]bool,
	clusterNodes map[string][]string,
	nodeMap map[nodeKey]*api.NodeMetrics,
	fn func(*api.NodeMetrics, float64),
) {
	if resp == nil {
		return
	}
	for _, row := range resp.Data.Result {
		cluster := row.Metric["cluster"]
		if covered[cluster] {
			continue
		}
		nodes, ok := clusterNodes[cluster]
		if !ok || len(nodes) != 1 {
			continue
		}
		if val, err := metricValue(row.Value); err == nil {
			for _, node := range nodes {
				if nm := nodeMap[nodeKey{cluster, node}]; nm != nil {
					fn(nm, val)
				}
			}
		}
	}
}

// noCopy may be embedded in a struct to prevent it from being copied after
// first use. See https://golang.org/issues/8005#issuecomment-190753527 for
// the rationale and the vet check that enforces it.
type noCopy struct{}

func (*noCopy) Lock() {
	// Intentionally empty: satisfies sync.Locker so go vet can detect illegal
	// copies of any struct that embeds noCopy.
	// See https://golang.org/issues/8005#issuecomment-190753527.
}

func (*noCopy) Unlock() {
	// Intentionally empty: satisfies sync.Locker so go vet can detect illegal
	// copies of any struct that embeds noCopy.
	// See https://golang.org/issues/8005#issuecomment-190753527.
}

// ObservabilityClient queries Thanos for node and Ceph metrics across all managed clusters.
//
// After the first call the internal HTTP client is cached via sync.Once.
// Do not copy the struct after first use — the embedded noCopy field enforces
// this at vet time.
type ObservabilityClient struct {
	RestConfig *rest.Config
	ThanosHost string // overrides thanosQueryFrontend for testing
	// ServiceCAPath is the path to the OpenShift service CA bundle PEM file.
	// Defaults to DefaultServiceCAPath when empty.
	ServiceCAPath string
	// TLSOpts is an optional function applied to the TLS configuration of the
	// outbound HTTP client. Used to inherit the cluster's central TLS profile.
	TLSOpts func(*tls.Config)

	noCopy       noCopy
	clientOnce   sync.Once
	cachedClient *http.Client
	clientErr    error
}

func (o *ObservabilityClient) serviceCAPath() string {
	if o.ServiceCAPath != "" {
		return o.ServiceCAPath
	}
	return DefaultServiceCAPath
}

func (o *ObservabilityClient) baseURL() string {
	if o.ThanosHost != "" {
		return o.ThanosHost
	}
	return thanosQueryFrontend
}

// httpClient returns a cached *http.Client built from RestConfig.
// It is initialised exactly once; all parallel Thanos queries share the same
// client and therefore the same connection pool.
func (o *ObservabilityClient) httpClient() (*http.Client, error) {
	o.clientOnce.Do(func() {
		o.cachedClient, o.clientErr = buildHTTPClient(o.RestConfig, o.serviceCAPath(), o.TLSOpts)
	})
	return o.cachedClient, o.clientErr
}

// promResponse is the Prometheus HTTP API query response envelope.
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]interface{}    `json:"value"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

func (o *ObservabilityClient) query(ctx context.Context, promQL string) (*promResponse, error) {
	endpoint := o.baseURL() + "/api/v1/query"

	httpClient, err := o.httpClient()
	if err != nil {
		return nil, fmt.Errorf("build HTTP client: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	q := url.Values{}
	q.Set("query", promQL)
	req.URL.RawQuery = q.Encode()

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("thanos query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("thanos returned %d: %s", resp.StatusCode, string(raw))
	}

	var pr promResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("decode thanos response: %w", err)
	}
	if pr.Status != promStatusSuccess {
		return nil, fmt.Errorf("thanos query failed: %s", pr.Error)
	}
	return &pr, nil
}

// CheckHealth verifies that the configured Thanos base URL is reachable and
// serves Prometheus query API responses.
func (o *ObservabilityClient) CheckHealth(ctx context.Context) error {
	_, err := o.query(ctx, "up")
	if err != nil {
		return fmt.Errorf("observability endpoint unhealthy: %w", err)
	}
	return nil
}

// metricValue extracts the float64 value from a Prometheus instant query result row.
// row is always [2]interface{} per the Prometheus HTTP API: [unixTimestamp, valueString].
func metricValue(row [2]interface{}) (float64, error) {
	s, ok := row[1].(string)
	if !ok {
		return 0, fmt.Errorf("value is not a string")
	}
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil {
		return 0, fmt.Errorf("parse metric value %q: %w", s, err)
	}
	return v, nil
}

// runParallelNodeQueries fires all 6 node-metric Thanos queries concurrently
// and returns the raw responses. Each goroutine writes to a distinct field of
// nodeMetricResponses; the mutex guards against the Go race detector flagging
// concurrent writes to the same struct value.
func (o *ObservabilityClient) runParallelNodeQueries(ctx context.Context) (*nodeMetricResponses, error) {
	var (
		res nodeMetricResponses
		mu  sync.Mutex
	)
	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		r, err := o.query(gctx, `kube_node_status_allocatable{resource="cpu"}`)
		if err != nil {
			return fmt.Errorf("CPU allocatable: %w", err)
		}
		mu.Lock()
		res.cpuAlloc = r
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		r, err := o.query(gctx, `kube_node_status_allocatable{resource="memory"}`)
		if err != nil {
			return fmt.Errorf("memory allocatable: %w", err)
		}
		mu.Lock()
		res.memAlloc = r
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		r, err := o.query(gctx,
			`sum by(cluster, node)(kube_pod_container_resource_requests{resource="cpu"})`)
		if err != nil {
			return fmt.Errorf("CPU requests per node: %w", err)
		}
		mu.Lock()
		res.cpuReq = r
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		r, err := o.query(gctx,
			`sum by(cluster)(kube_pod_container_resource_requests:sum{resource="cpu"})`)
		if err != nil {
			return fmt.Errorf("SNO CPU requests: %w", err)
		}
		mu.Lock()
		res.cpuReqSNO = r
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		r, err := o.query(gctx,
			`sum by(cluster, node)(kube_pod_container_resource_requests{resource="memory"})`)
		if err != nil {
			return fmt.Errorf("memory requests per node: %w", err)
		}
		mu.Lock()
		res.memReq = r
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		r, err := o.query(gctx,
			`sum by(cluster)(kube_pod_container_resource_requests:sum{resource="memory"})`)
		if err != nil {
			return fmt.Errorf("SNO memory requests: %w", err)
		}
		mu.Lock()
		res.memReqSNO = r
		mu.Unlock()
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("parallel Thanos queries: %w", err)
	}
	return &res, nil
}

// FetchNodeMetrics queries Thanos for per-node CPU/memory across all managed
// clusters. Only nodes that satisfy ALL three conditions are included:
//
//  1. labelled kubevirt.io/schedulable=true  (virt-handler passed HW checks)
//  2. NOT cordoned                           (kube_node_spec_unschedulable != 1)
//  3. Ready condition is true                (kube_node_status_condition Ready==1)
//
// Per-node requested CPU/memory (sum of pod resource requests) is collected as
// the kube-scheduler capacity gate.
//
// All 6 Thanos queries after the initial schedulable-node lookup are issued in
// parallel using errgroup to minimise latency at scale.
//
// SNO clusters only ship a pre-aggregated cluster-total metric; the cluster
// total is assigned to the single schedulable node from kube_node_labels.
func (o *ObservabilityClient) FetchNodeMetrics(ctx context.Context) (api.ClusterNodeMetrics, error) {
	// ── Step 1: schedulable node list (must be serial — all other steps depend on it) ──
	//
	// Three filters are combined:
	//   (a) kubevirt.io/schedulable=true  — only KubeVirt-capable nodes
	//   (b) kube_node_spec_unschedulable != 1  — exclude cordoned nodes
	//   (c) kube_node_status_condition{Ready,true} != 0  — exclude NotReady nodes
	//
	// Cordoned nodes retain kubevirt.io/schedulable=true but reject new pod scheduling.
	// NotReady nodes are unreachable / partitioned; the scheduler avoids them too.
	schedulableResp, err := o.query(ctx, `
max by(cluster, node)(kube_node_labels{label_kubevirt_io_schedulable="true"})
  unless on(cluster, node) (kube_node_spec_unschedulable == 1)
  unless on(cluster, node) (kube_node_status_condition{condition="Ready",status="true"} == 0)`)
	if err != nil {
		return nil, fmt.Errorf("query schedulable nodes: %w", err)
	}

	schedulableSet := make(map[nodeKey]bool)
	clusterNodes := make(map[string][]string)
	nodeMap := make(map[nodeKey]*api.NodeMetrics)

	for _, row := range schedulableResp.Data.Result {
		cluster, node := row.Metric["cluster"], row.Metric["node"]
		if cluster == "" || node == "" {
			continue
		}
		k := nodeKey{cluster, node}
		schedulableSet[k] = true
		clusterNodes[cluster] = append(clusterNodes[cluster], node)
		nodeMap[k] = &api.NodeMetrics{NodeName: node}
	}

	// ── Steps 2-7: all remaining queries in parallel ──────────────────────────
	qr, err := o.runParallelNodeQueries(ctx)
	if err != nil {
		return nil, err
	}

	// ── Merge all responses into nodeMap ──────────────────────────────────────
	applyMetric(qr.cpuAlloc, schedulableSet, func(k nodeKey, v float64) { nodeMap[k].AllocatableCPUCores = v })
	applyMetric(qr.memAlloc, schedulableSet, func(k nodeKey, v float64) { nodeMap[k].AllocatableMemBytes = int64(v) })

	// Per-node requests (multi-node clusters)
	cpuReqCovered := make(map[string]bool)
	applyMetric(qr.cpuReq, schedulableSet, func(k nodeKey, v float64) {
		nodeMap[k].RequestedCPUCores = v
		cpuReqCovered[k.cluster] = true
	})
	memReqCovered := make(map[string]bool)
	applyMetric(qr.memReq, schedulableSet, func(k nodeKey, v float64) {
		nodeMap[k].RequestedMemBytes = int64(v)
		memReqCovered[k.cluster] = true
	})

	// SNO fallback: cluster-total → single schedulable node
	applySNOMetric(qr.cpuReqSNO, cpuReqCovered, clusterNodes, nodeMap,
		func(nm *api.NodeMetrics, v float64) { nm.RequestedCPUCores = v })
	applySNOMetric(qr.memReqSNO, memReqCovered, clusterNodes, nodeMap,
		func(nm *api.NodeMetrics, v float64) { nm.RequestedMemBytes = int64(v) })

	// ── Assemble result ───────────────────────────────────────────────────────
	result := make(api.ClusterNodeMetrics)
	for k, nm := range nodeMap {
		result[k.cluster] = append(result[k.cluster], *nm)
	}
	return result, nil
}

// FetchCephMetrics queries Thanos for Ceph total and available bytes per managed cluster.
// ceph_cluster_total_avail_bytes is a single metric that avoids the race condition of
// computing available = total - used across two separate HTTP queries.
func (o *ObservabilityClient) FetchCephMetrics(ctx context.Context) (map[string]api.CephMetrics, error) {
	out := make(map[string]api.CephMetrics)

	totalResp, err := o.query(ctx, `ceph_cluster_total_bytes`)
	if err != nil {
		return nil, fmt.Errorf("query ceph total bytes: %w", err)
	}
	for _, row := range totalResp.Data.Result {
		cluster := row.Metric["cluster"]
		if cluster == "" {
			continue
		}
		val, err := metricValue(row.Value)
		if err != nil {
			continue
		}
		m := out[cluster]
		m.TotalBytes = int64(val)
		out[cluster] = m
	}

	availResp, err := o.query(ctx, `ceph_cluster_total_avail_bytes`)
	if err != nil {
		return nil, fmt.Errorf("query ceph available bytes: %w", err)
	}
	for _, row := range availResp.Data.Result {
		cluster := row.Metric["cluster"]
		if cluster == "" {
			continue
		}
		val, err := metricValue(row.Value)
		if err != nil {
			continue
		}
		m := out[cluster]
		m.AvailableBytes = int64(val)
		out[cluster] = m
	}

	return out, nil
}
