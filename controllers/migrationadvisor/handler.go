package migrationadvisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/stolostron/mtv-integrations/api"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)

// clusterDataCache holds a short-lived snapshot of ALL cluster-wide data so
// that repeated or concurrent requests do not re-query Thanos and the Search
// API for every individual VM lookup.
//
// StorageClasses are cached only for managed clusters that have
// acm/cnv-operator-install=true.
//
// Caching a name-filtered SC result would only be safe to reuse for requests
// with the exact same SC names — all other VMs would get stale/wrong SC data.
type clusterDataCache struct {
	mu       sync.RWMutex
	nodeData api.ClusterNodeMetrics
	cephData map[string]api.CephMetrics
	scData   map[string][]SCProvisioner
	// refreshGroup deduplicates concurrent cache rebuilds after a miss.
	refreshGroup singleflight.Group
	ttl          time.Duration
	expiresAt    time.Time
}

const defaultCacheTTL = 30 * time.Second

var managedClusterGVR = schema.GroupVersionResource{
	Group:    "cluster.open-cluster-management.io",
	Version:  "v1",
	Resource: "managedclusters",
}

const cnvOperatorInstallLabel = "acm/cnv-operator-install"

func (c *clusterDataCache) get() (
	api.ClusterNodeMetrics,
	map[string]api.CephMetrics,
	map[string][]SCProvisioner,
	bool,
) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if time.Now().Before(c.expiresAt) {
		return c.nodeData, c.cephData, c.scData, true
	}
	return nil, nil, nil, false
}

func (c *clusterDataCache) set(
	nodes api.ClusterNodeMetrics,
	ceph map[string]api.CephMetrics,
	scs map[string][]SCProvisioner,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodeData = nodes
	c.cephData = ceph
	c.scData = scs
	ttl := c.ttl
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	c.expiresAt = time.Now().Add(ttl)
}

// Handler handles the GET /api/v1/migration-targets HTTP endpoint.
type Handler struct {
	DynamicClient dynamic.Interface
	// RestConfig is used to build authenticated HTTP clients for Thanos and the Search API.
	RestConfig *rest.Config
	// SearchAPIEndpoint and ThanosHost allow overriding service endpoints for local testing.
	SearchAPIEndpoint string
	ThanosHost        string
	// ServiceCAPath is the path to the OpenShift service CA bundle PEM file.
	// It is added to the HTTP client's trust pool alongside the system root CAs so
	// that both in-cluster HTTPS services (service CA) and external Routes
	// (ingress/router CA via system pool) can be verified.
	// Defaults to DefaultServiceCAPath when empty.
	ServiceCAPath string
	// CacheTTL controls how long cluster-wide data (node metrics, SCs) is cached.
	// Defaults to defaultCacheTTL (30 s) when zero.
	CacheTTL time.Duration

	cache clusterDataCache
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := ctrl.LoggerFrom(r.Context()).WithName("migration-advisor")

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	req := api.MigrationTargetRequest{
		VMNamespace: q.Get("vmNamespace"),
		VMName:      q.Get("vmName"),
		ClusterName: q.Get("cluster"),
	}

	if req.VMNamespace == "" || req.VMName == "" || req.ClusterName == "" {
		http.Error(w, "vmNamespace, vmName, and cluster query parameters are required", http.StatusBadRequest)
		return
	}

	log = log.WithValues("vm", req.VMName, "namespace", req.VMNamespace, "cluster", req.ClusterName)
	ctx := ctrl.LoggerInto(r.Context(), log)

	resp, err := h.evaluate(ctx, req)
	if err != nil {
		log.Error(err, "evaluation failed")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error(err, "failed to encode response")
	}
}

// evaluate runs the full pipeline: fetch VM info, gather cluster data, score.
//
// Branch A (VM-specific, always fresh) and Branch B (cluster-wide, 30 s cached)
// run concurrently via errgroup. The outer variables (sourceVM, snapshot) are
// written from within the goroutines but are safe to read after g.Wait() because
// errgroup.Wait() provides a happens-before guarantee.
func (h *Handler) evaluate(ctx context.Context, req api.MigrationTargetRequest) (*api.MigrationTargetResponse, error) {
	log := ctrl.LoggerFrom(ctx)

	var (
		sourceVM *api.SourceVMInfo
		snapshot clusterSnapshot
	)

	g, gctx := errgroup.WithContext(ctx)

	// ── Branch A: VM-specific data (always fresh, O(1) network calls) ─────────
	g.Go(func() error {
		vm, err := (&VMFetcher{DynamicClient: h.DynamicClient}).FetchVMInfo(gctx, req)
		if err != nil {
			return err
		}
		sourceVM = vm
		return nil
	})

	// ── Branch B: cluster-wide data (cached, refreshed at most every configured TTL) ──
	g.Go(func() error {
		s, err := h.getClusterSnapshot(gctx)
		if err != nil {
			return err
		}
		snapshot = s
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	log.Info("fetched source VM info",
		"cpuCores", sourceVM.CPUCores,
		"memoryBytes", sourceVM.MemoryBytes,
		"volumes", len(sourceVM.Volumes))

	// ── Score ──────────────────────────────────────────────────────────────────
	candidates, excluded := Score(ScoringInput{
		SourceVM:    *sourceVM,
		ClusterSCs:  snapshot.scs,
		NodeMetrics: snapshot.nodes,
		CephMetrics: snapshot.ceph,
	})

	var recommendation *api.Recommendation
	if len(candidates) > 0 {
		top := candidates[0]
		recommendation = &api.Recommendation{
			Cluster:              top.Cluster,
			Node:                 top.BestNode,
			TotalScore:           top.TotalScore,
			AvailableCPUCores:    top.AvailableCPUCores,
			AvailableMemoryBytes: top.AvailableMemoryBytes,
			StorageType:          top.StorageType,
			CephAvailableBytes:   top.CephAvailableBytes,
		}
	}

	return &api.MigrationTargetResponse{
		SourceVM:         *sourceVM,
		Recommendation:   recommendation,
		Candidates:       candidates,
		ExcludedClusters: excluded,
	}, nil
}

// getClusterSnapshot returns cluster-wide data from cache when fresh, or
// fetches and caches it via a singleflight to deduplicate concurrent misses.
//
// Fetches ALL StorageClasses (unfiltered) so any VM can be served from the
// same cache entry. Caching a name-filtered result would only be safe to reuse
// for VMs with the exact same SC names — every other VM would get wrong data.
func (h *Handler) getClusterSnapshot(ctx context.Context) (clusterSnapshot, error) {
	log := ctrl.LoggerFrom(ctx)

	h.cache.mu.Lock()
	h.cache.ttl = h.CacheTTL
	h.cache.mu.Unlock()

	nodes, ceph, scs, hit := h.cache.get()
	if hit {
		log.V(1).Info("cluster data cache hit")
		return clusterSnapshot{nodes: nodes, ceph: ceph, scs: scs}, nil
	}

	v, err, shared := h.cache.refreshGroup.Do("cluster-data", func() (interface{}, error) {
		if cachedNodes, cachedCeph, cachedSCs, cachedHit := h.cache.get(); cachedHit {
			return clusterSnapshot{nodes: cachedNodes, ceph: cachedCeph, scs: cachedSCs}, nil
		}
		// context.WithoutCancel inherits values (logger, tracing spans) from the
		// triggering request while stripping its cancellation and deadline.  This
		// prevents a single client disconnect or per-request timeout from aborting
		// a shared in-flight refresh that other concurrent requests are waiting on.
		return h.fetchFreshClusterData(context.WithoutCancel(ctx))
	})
	if err != nil {
		return clusterSnapshot{}, err
	}
	s, ok := v.(clusterSnapshot)
	if !ok {
		return clusterSnapshot{}, fmt.Errorf("unexpected cluster snapshot type %T", v)
	}
	log.Info("cluster data cache refreshed",
		"sharedRefresh", shared,
		"clusters", len(s.scs), "nodesWithMetrics", len(s.nodes))
	return s, nil
}

// fetchFreshClusterData fetches node metrics, Ceph metrics, and StorageClasses
// concurrently, filters SCs to eligible clusters, populates the cache, and
// returns the resulting snapshot.
//
// Ceph metrics are non-fatal: missing Ceph data causes the scorer to use a
// neutral storage score rather than failing the whole request.
func (h *Handler) fetchFreshClusterData(ctx context.Context) (clusterSnapshot, error) {
	obsClient := &ObservabilityClient{RestConfig: h.RestConfig, ThanosHost: h.ThanosHost, ServiceCAPath: h.ServiceCAPath}
	searchClient := &SearchClient{
		RestConfig:        h.RestConfig,
		SearchAPIEndpoint: h.SearchAPIEndpoint,
		ServiceCAPath:     h.ServiceCAPath,
	}

	var (
		refreshNodes api.ClusterNodeMetrics
		refreshCeph  map[string]api.CephMetrics
		refreshSCs   map[string][]SCProvisioner
	)
	var innerMu sync.Mutex
	inner, ictx := errgroup.WithContext(ctx)

	inner.Go(func() error {
		n, err := obsClient.FetchNodeMetrics(ictx)
		if err != nil {
			return fmt.Errorf("fetch node metrics: %w", err)
		}
		innerMu.Lock()
		refreshNodes = n
		innerMu.Unlock()
		return nil
	})
	inner.Go(func() error {
		c, err := obsClient.FetchCephMetrics(ictx)
		if err != nil {
			ctrl.LoggerFrom(ictx).Error(err, "failed to fetch Ceph metrics, proceeding without")
			return nil // non-fatal
		}
		innerMu.Lock()
		refreshCeph = c
		innerMu.Unlock()
		return nil
	})
	inner.Go(func() error {
		// No name filter — fetch all SCs so the cache is valid for any VM.
		s, err := searchClient.ListStorageClassProvisionersByCluster(ictx)
		if err != nil {
			return err // fatal — cannot score without SC data
		}
		innerMu.Lock()
		refreshSCs = s
		innerMu.Unlock()
		return nil
	})

	if err := inner.Wait(); err != nil {
		return clusterSnapshot{}, err
	}
	if len(refreshNodes) == 0 {
		return clusterSnapshot{}, fmt.Errorf("node metrics unavailable: fetched 0 nodes")
	}

	eligibleClusters, err := h.listEligibleManagedClusters(ctx)
	if err != nil {
		return clusterSnapshot{}, err
	}
	refreshSCs = filterClusterSCsByEligibility(refreshSCs, eligibleClusters)

	h.cache.set(refreshNodes, refreshCeph, refreshSCs)
	return clusterSnapshot{nodes: refreshNodes, ceph: refreshCeph, scs: refreshSCs}, nil
}

func (h *Handler) listEligibleManagedClusters(ctx context.Context) (map[string]struct{}, error) {
	list, err := h.DynamicClient.Resource(managedClusterGVR).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=true", cnvOperatorInstallLabel),
	})
	if err != nil {
		return nil, fmt.Errorf("list managed clusters with %s=true: %w", cnvOperatorInstallLabel, err)
	}

	eligible := make(map[string]struct{}, len(list.Items))
	for _, item := range list.Items {
		name := item.GetName()
		if name != "" {
			eligible[name] = struct{}{}
		}
	}
	return eligible, nil
}

func filterClusterSCsByEligibility(
	clusterSCs map[string][]SCProvisioner,
	eligible map[string]struct{},
) map[string][]SCProvisioner {
	filtered := make(map[string][]SCProvisioner)
	for cluster, scs := range clusterSCs {
		if _, ok := eligible[cluster]; ok {
			filtered[cluster] = scs
		}
	}
	return filtered
}

type clusterSnapshot struct {
	nodes api.ClusterNodeMetrics
	ceph  map[string]api.CephMetrics
	scs   map[string][]SCProvisioner
}
