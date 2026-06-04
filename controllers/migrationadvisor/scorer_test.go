// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"testing"

	"github.com/stolostron/mtv-integrations/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyStorageType(t *testing.T) {
	tests := []struct {
		provisioner string
		expected    api.StorageType
	}{
		{"ebs.csi.aws.com", api.StorageTypeCloud},
		{"disk.csi.azure.com", api.StorageTypeCloud},
		{"pd.csi.storage.gke.io", api.StorageTypeCloud},
		{"rbd.csi.ceph.com", api.StorageTypeCeph},
		{"openshift-storage.rbd.csi.ceph.com", api.StorageTypeCeph},
		{"cephfs.csi.ceph.com", api.StorageTypeCeph},
		{"kubernetes.io/no-provisioner", api.StorageTypeOther},
		{"", api.StorageTypeOther},
	}
	for _, tt := range tests {
		t.Run(tt.provisioner, func(t *testing.T) {
			assert.Equal(t, tt.expected, classifyStorageType(tt.provisioner))
		})
	}
}

func TestComputeCPUScore(t *testing.T) {
	node := api.NodeMetrics{
		NodeName:            "worker-1",
		AllocatableCPUCores: 16,
		RequestedCPUCores:   4,
	}
	// available = 12, vm needs 2 → headroom = 10 / 16 = 62.5
	score := computeCPUScore(node, 2)
	assert.InDelta(t, 62.5, score, 0.1)
}

func TestComputeMemScore(t *testing.T) {
	node := api.NodeMetrics{
		NodeName:            "worker-1",
		AllocatableMemBytes: 32 * (1 << 30), // 32 GiB
		RequestedMemBytes:   8 * (1 << 30),  // 8 GiB
	}
	// available = 24 GiB, vm needs 4 GiB → headroom = 20/32 = 62.5
	score := computeMemScore(node, 4*(1<<30))
	assert.InDelta(t, 62.5, score, 0.1)
}

func TestComputeStorageScore_Cloud(t *testing.T) {
	score, avail, ok := computeStorageScore(api.StorageTypeCloud, nil, api.CephMetrics{})
	assert.True(t, ok)
	assert.Equal(t, float64(100), score)
	assert.Equal(t, int64(0), avail)
}

func TestComputeStorageScore_Ceph_Sufficient(t *testing.T) {
	volumes := []api.VMVolumeInfo{{SizeBytes: 30 * (1 << 30)}} // 30 GiB
	ceph := api.CephMetrics{
		TotalBytes:     500 * int64(1<<30),
		AvailableBytes: 200 * int64(1<<30),
	}
	score, avail, ok := computeStorageScore(api.StorageTypeCeph, volumes, ceph)
	assert.True(t, ok)
	assert.Equal(t, ceph.AvailableBytes, avail)
	assert.InDelta(t, 40.0, score, 0.1) // 200/500 = 40%
}

func TestComputeStorageScore_Ceph_Insufficient(t *testing.T) {
	volumes := []api.VMVolumeInfo{{SizeBytes: 300 * int64(1<<30)}} // 300 GiB
	ceph := api.CephMetrics{
		TotalBytes:     500 * int64(1<<30),
		AvailableBytes: 100 * int64(1<<30), // only 100 GiB available
	}
	_, _, ok := computeStorageScore(api.StorageTypeCeph, volumes, ceph)
	assert.False(t, ok)
}

func TestScoreFiltersSourceCluster(t *testing.T) {
	input := ScoringInput{
		SourceVM: api.SourceVMInfo{
			Name:        "my-vm",
			Namespace:   "default",
			Cluster:     "source-cluster",
			CPUCores:    2,
			MemoryBytes: 4 * (1 << 30),
			Volumes:     []api.VMVolumeInfo{{StorageClass: "ceph-rbd", SizeBytes: 10 * int64(1<<30)}},
		},
		ClusterSCs: map[string][]SCProvisioner{
			"source-cluster": {{Name: "ceph-rbd", Provisioner: "rbd.csi.ceph.com"}},
			"target-cluster": {{Name: "ceph-rbd", Provisioner: "rbd.csi.ceph.com"}},
		},
		NodeMetrics: api.ClusterNodeMetrics{
			"target-cluster": {
				{NodeName: "worker-1", AllocatableCPUCores: 16, RequestedCPUCores: 4,
					AllocatableMemBytes: 32 * int64(1<<30), RequestedMemBytes: 8 * int64(1<<30)},
			},
		},
		CephMetrics: map[string]api.CephMetrics{
			"target-cluster": {TotalBytes: 500 * int64(1<<30), AvailableBytes: 200 * int64(1<<30)},
		},
	}

	candidates, excluded := Score(input)
	assert.Len(t, candidates, 1)
	assert.Equal(t, "target-cluster", candidates[0].Cluster)
	assert.Equal(t, api.StorageTypeCeph, candidates[0].StorageType)
	assert.Empty(t, excluded)
}

func TestScoreExcludesNoSCMatch(t *testing.T) {
	input := ScoringInput{
		SourceVM: api.SourceVMInfo{
			Cluster:     "source",
			CPUCores:    2,
			MemoryBytes: 4 * (1 << 30),
			Volumes:     []api.VMVolumeInfo{{StorageClass: "ceph-rbd", SizeBytes: 10 * int64(1<<30)}},
		},
		ClusterSCs: map[string][]SCProvisioner{
			"target-no-ceph": {{Name: "gp2", Provisioner: "ebs.csi.aws.com"}},
		},
		NodeMetrics: api.ClusterNodeMetrics{},
		CephMetrics: map[string]api.CephMetrics{},
	}

	candidates, excluded := Score(input)
	assert.Empty(t, candidates)
	assert.Len(t, excluded, 1)
	assert.Contains(t, excluded[0].Reason, "No matching StorageClass")
}

func TestScoreExcludesInsufficientCapacity(t *testing.T) {
	input := ScoringInput{
		SourceVM: api.SourceVMInfo{
			Cluster:     "source",
			CPUCores:    32,
			MemoryBytes: 256 * int64(1<<30),
			Volumes:     []api.VMVolumeInfo{{StorageClass: "gp2", SizeBytes: 10 * int64(1<<30)}},
		},
		ClusterSCs: map[string][]SCProvisioner{
			"small-cluster": {{Name: "gp2", Provisioner: "ebs.csi.aws.com"}},
		},
		NodeMetrics: api.ClusterNodeMetrics{
			"small-cluster": {
				{NodeName: "worker-1", AllocatableCPUCores: 4, RequestedCPUCores: 2,
					AllocatableMemBytes: 8 * int64(1<<30), RequestedMemBytes: 2 * int64(1<<30)},
			},
		},
		CephMetrics: map[string]api.CephMetrics{},
	}

	candidates, excluded := Score(input)
	assert.Empty(t, candidates)
	assert.Len(t, excluded, 1)
	assert.Contains(t, excluded[0].Reason, "No node has sufficient CPU and memory")
}

func TestParseQuantityToBytes(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"4Gi", 4 * (1 << 30)},
		{"500Mi", 500 * (1 << 20)},
		{"1Ti", 1 << 40},
		{"1024Ki", 1024 * (1 << 10)},
		{"1000000000", 1_000_000_000},
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseQuantityToBytes(tt.input)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestMatchStorageClasses(t *testing.T) {
	source := map[string]bool{"ceph-rbd": true, "gp2": true}
	target := []SCProvisioner{
		{Name: "ceph-rbd", Provisioner: "rbd.csi.ceph.com"},
		{Name: "local-path", Provisioner: "rancher.io/local-path"},
	}
	names, provs := matchStorageClasses(source, target)
	assert.Equal(t, []string{"ceph-rbd"}, names)
	assert.Len(t, provs, 1)
	assert.Equal(t, "rbd.csi.ceph.com", provs[0].Provisioner)
}

func TestMatchStorageClasses_NoMatch(t *testing.T) {
	names, provs := matchStorageClasses(map[string]bool{"fast": true},
		[]SCProvisioner{{Name: "slow", Provisioner: "x"}})
	assert.Empty(t, names)
	assert.Empty(t, provs)
}

func TestBestCapableNode(t *testing.T) {
	vm := api.SourceVMInfo{CPUCores: 2, MemoryBytes: 4 * (1 << 30)}
	nodes := []api.NodeMetrics{
		{NodeName: "small", AllocatableCPUCores: 2, RequestedCPUCores: 1,
			AllocatableMemBytes: 2 * int64(1<<30), RequestedMemBytes: 0}, // not enough mem
		{NodeName: "big", AllocatableCPUCores: 16, RequestedCPUCores: 2,
			AllocatableMemBytes: 32 * int64(1<<30), RequestedMemBytes: 4 * int64(1<<30)},
		{NodeName: "medium", AllocatableCPUCores: 8, RequestedCPUCores: 2,
			AllocatableMemBytes: 16 * int64(1<<30), RequestedMemBytes: 4 * int64(1<<30)},
	}
	best, found := bestCapableNode(nodes, vm)
	assert.True(t, found)
	assert.Equal(t, "big", best.NodeName)
}

func TestBestCapableNode_NoneCapable(t *testing.T) {
	vm := api.SourceVMInfo{CPUCores: 64, MemoryBytes: 256 * int64(1<<30)}
	nodes := []api.NodeMetrics{
		{NodeName: "tiny", AllocatableCPUCores: 2, AllocatableMemBytes: 4 * int64(1<<30)},
	}
	_, found := bestCapableNode(nodes, vm)
	assert.False(t, found)
}

func TestBestCapableNode_Empty(t *testing.T) {
	_, found := bestCapableNode(nil, api.SourceVMInfo{})
	assert.False(t, found)
}

func TestClassifyFromProvisioners_Ceph(t *testing.T) {
	p := []SCProvisioner{
		{Provisioner: "rbd.csi.ceph.com"},
		{Provisioner: "ebs.csi.aws.com"},
	}
	assert.Equal(t, api.StorageTypeCeph, classifyFromProvisioners(p))
}

func TestClassifyFromProvisioners_Cloud(t *testing.T) {
	p := []SCProvisioner{{Provisioner: "ebs.csi.aws.com"}}
	assert.Equal(t, api.StorageTypeCloud, classifyFromProvisioners(p))
}

func TestClassifyFromProvisioners_Other(t *testing.T) {
	p := []SCProvisioner{{Provisioner: "rancher.io/local-path"}}
	assert.Equal(t, api.StorageTypeOther, classifyFromProvisioners(p))
}

func TestClassifyFromProvisioners_Empty(t *testing.T) {
	assert.Equal(t, api.StorageTypeOther, classifyFromProvisioners(nil))
}

func TestClamp(t *testing.T) {
	assert.Equal(t, float64(0), clamp(-1))
	assert.Equal(t, float64(100), clamp(101))
	assert.InDelta(t, 50.0, clamp(50), 0.001)
}

func TestRound2(t *testing.T) {
	assert.InDelta(t, 3.14, round2(3.14159), 0.001)
	assert.InDelta(t, 0.0, round2(0.001), 0.001)
}

func TestComputeStorageScore_CephLowRatio(t *testing.T) {
	// Available ratio < 10% → filtered out.
	ceph := api.CephMetrics{TotalBytes: 1000, AvailableBytes: 5}
	_, _, ok := computeStorageScore(api.StorageTypeCeph, nil, ceph)
	assert.False(t, ok)
}

// TestScoreExcludesUnsupportedStorage verifies that clusters are excluded when
// either Ceph metrics are unavailable (TotalBytes=0) or the storage type is
// unsupported (StorageTypeOther).
func TestScoreExcludesUnsupportedStorage(t *testing.T) {
	node := api.NodeMetrics{
		NodeName:            "worker-1",
		AllocatableCPUCores: 16, RequestedCPUCores: 4,
		AllocatableMemBytes: 32 * int64(1<<30), RequestedMemBytes: 8 * int64(1<<30),
	}

	tests := []struct {
		name            string
		clusterName     string
		storageClass    string
		provisioner     string
		wantReasonSubst string
	}{
		{
			name:            "ceph no metrics",
			clusterName:     "ceph-cluster",
			storageClass:    "ceph-rbd",
			provisioner:     "rbd.csi.ceph.com",
			wantReasonSubst: "Ceph storage capacity data is unavailable",
		},
		{
			name:            "other storage type",
			clusterName:     "other-cluster",
			storageClass:    "local-path",
			provisioner:     "rancher.io/local-path",
			wantReasonSubst: "only cloud and Ceph storage classes are supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := ScoringInput{
				SourceVM: api.SourceVMInfo{
					Cluster:     "source",
					CPUCores:    2,
					MemoryBytes: 4 * int64(1<<30),
					Volumes:     []api.VMVolumeInfo{{StorageClass: tt.storageClass, SizeBytes: 10 * int64(1<<30)}},
				},
				ClusterSCs: map[string][]SCProvisioner{
					tt.clusterName: {{Name: tt.storageClass, Provisioner: tt.provisioner}},
				},
				NodeMetrics: api.ClusterNodeMetrics{
					tt.clusterName: {node},
				},
				CephMetrics: map[string]api.CephMetrics{},
			}

			candidates, excluded := Score(input)
			assert.Empty(t, candidates)
			require.Len(t, excluded, 1)
			assert.Equal(t, tt.clusterName, excluded[0].Cluster)
			assert.Contains(t, excluded[0].Reason, tt.wantReasonSubst)
		})
	}
}

func TestComputeCPUScore_ZeroAllocatable(t *testing.T) {
	score := computeCPUScore(api.NodeMetrics{AllocatableCPUCores: 0}, 2)
	assert.Equal(t, float64(0), score)
}

func TestComputeMemScore_ZeroAllocatable(t *testing.T) {
	score := computeMemScore(api.NodeMetrics{AllocatableMemBytes: 0}, 2)
	assert.Equal(t, float64(0), score)
}

func TestScoreMultipleCandidates_SortedByScore(t *testing.T) {
	input := ScoringInput{
		SourceVM: api.SourceVMInfo{
			Cluster:     "src",
			CPUCores:    1,
			MemoryBytes: 1 * int64(1<<30),
			Volumes:     []api.VMVolumeInfo{{StorageClass: "gp2", SizeBytes: 1 * int64(1<<30)}},
		},
		ClusterSCs: map[string][]SCProvisioner{
			"big":   {{Name: "gp2", Provisioner: "ebs.csi.aws.com"}},
			"small": {{Name: "gp2", Provisioner: "ebs.csi.aws.com"}},
		},
		NodeMetrics: api.ClusterNodeMetrics{
			"big": {
				{NodeName: "n1", AllocatableCPUCores: 64, RequestedCPUCores: 0,
					AllocatableMemBytes: 256 * int64(1<<30), RequestedMemBytes: 0},
			},
			"small": {
				{NodeName: "n1", AllocatableCPUCores: 4, RequestedCPUCores: 0,
					AllocatableMemBytes: 8 * int64(1<<30), RequestedMemBytes: 0},
			},
		},
		CephMetrics: map[string]api.CephMetrics{},
	}

	candidates, excluded := Score(input)
	assert.Empty(t, excluded)
	require.Len(t, candidates, 2)
	assert.Equal(t, "big", candidates[0].Cluster, "higher headroom cluster must come first")
}

func TestParseCPUCores(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
	}{
		{"2", 2.0},
		{"500m", 0.5},
		{"1000m", 1.0},
		{"", 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseCPUCores(tt.input)
			assert.NoError(t, err)
			assert.InDelta(t, tt.expected, got, 0.001)
		})
	}
}
