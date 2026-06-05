// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package api

// MigrationTargetRequest holds the parameters for querying migration target candidates.
type MigrationTargetRequest struct {
	VMNamespace string
	VMName      string
	ClusterName string
}

// VMVolumeInfo describes a single volume attached to the VM.
type VMVolumeInfo struct {
	Name         string `json:"name"`
	StorageClass string `json:"storageClass"`
	SizeBytes    int64  `json:"sizeBytes"`
}

// SourceVMInfo holds metadata extracted from the source VM.
// CPUCores is always a ceiling of the raw request (e.g. "500m" → 1) so it
// represents the minimum whole-core reservation needed on the target node.
type SourceVMInfo struct {
	Name        string         `json:"name"`
	Namespace   string         `json:"namespace"`
	Cluster     string         `json:"cluster"`
	CPUCores    int64          `json:"cpuCores"`
	MemoryBytes int64          `json:"memoryBytes"`
	Volumes     []VMVolumeInfo `json:"volumes"`
}

// StorageType classifies the storage backend used by a StorageClass.
type StorageType string

const (
	StorageTypeCloud StorageType = "cloud"
	StorageTypeCeph  StorageType = "ceph"
	StorageTypeOther StorageType = "other"
)

// CandidateCluster represents a target cluster that passed all filters with its scores.
type CandidateCluster struct {
	Cluster               string      `json:"cluster"`
	TotalScore            float64     `json:"totalScore"`
	CPUScore              float64     `json:"cpuScore"`
	MemoryScore           float64     `json:"memoryScore"`
	StorageScore          float64     `json:"storageScore"`
	MatchedStorageClasses []string    `json:"matchedStorageClasses"`
	BestNode              string      `json:"bestNode"`
	AvailableCPUCores     float64     `json:"availableCPUCores"`
	AvailableMemoryBytes  int64       `json:"availableMemoryBytes"`
	StorageType           StorageType `json:"storageType"`
	CephAvailableBytes    int64       `json:"cephAvailableBytes,omitempty"`
}

// ExcludedCluster captures a cluster that was filtered out and the reason.
type ExcludedCluster struct {
	Cluster string `json:"cluster"`
	Reason  string `json:"reason"`
}

// Recommendation holds the single best migration target derived from the top candidate.
// It is nil when no cluster passed all filters.
type Recommendation struct {
	Cluster              string      `json:"cluster"`
	Node                 string      `json:"node"`
	TotalScore           float64     `json:"totalScore"`
	AvailableCPUCores    float64     `json:"availableCPUCores"`
	AvailableMemoryBytes int64       `json:"availableMemoryBytes"`
	StorageType          StorageType `json:"storageType"`
	CephAvailableBytes   int64       `json:"cephAvailableBytes,omitempty"`
}

// MigrationTargetResponse is the full API response.
type MigrationTargetResponse struct {
	SourceVM SourceVMInfo `json:"sourceVM"`
	// Recommendation is the single best cluster+node to migrate to.
	// Nil when every candidate cluster was excluded.
	Recommendation   *Recommendation    `json:"recommendation,omitempty"`
	Candidates       []CandidateCluster `json:"candidates"`
	ExcludedClusters []ExcludedCluster  `json:"excludedClusters"`
}

// NodeMetrics holds the computed available resources for a single node.
//
// Requested* fields reflect what pods have reserved (what kube-scheduler sees).
// Scoring uses allocatable minus requested as the capacity gate.
type NodeMetrics struct {
	NodeName string

	AllocatableCPUCores float64
	AllocatableMemBytes int64

	// RequestedCPUCores / RequestedMemBytes — sum of all pod resource requests on
	// this node. This is what the scheduler uses; a node is considered full when
	// allocatable - requested < 0 even if actual usage is low.
	RequestedCPUCores float64
	RequestedMemBytes int64
}

// AvailableCPUCores returns request-based available CPU (hard capacity).
func (n NodeMetrics) AvailableCPUCores() float64 {
	if avail := n.AllocatableCPUCores - n.RequestedCPUCores; avail > 0 {
		return avail
	}
	return 0
}

// AvailableMemBytes returns request-based available memory (hard capacity).
func (n NodeMetrics) AvailableMemBytes() int64 {
	if avail := n.AllocatableMemBytes - n.RequestedMemBytes; avail > 0 {
		return avail
	}
	return 0
}

// ClusterNodeMetrics groups node metrics by cluster name.
type ClusterNodeMetrics map[string][]NodeMetrics

// CephMetrics holds Ceph cluster-level metrics for a managed cluster.
type CephMetrics struct {
	TotalBytes     int64
	AvailableBytes int64
}
