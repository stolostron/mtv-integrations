// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"math"
	"sort"
	"strings"

	"github.com/stolostron/mtv-integrations/api"
)

const (
	// Weight factors for the total score (must sum to 1.0 for pure CPU+Mem case).
	weightCPU     = 0.35
	weightMemory  = 0.35
	weightStorage = 0.30

	// Minimum ceph available ratio to pass the storage check (10%).
	minCephAvailableRatio = 0.10

	awsEBSProvisioner  = "ebs.csi.aws.com"
	cephRBDProvisioner = "rbd.csi.ceph.com"
)

// cloudProvisionerPrefixes lists known cloud-based CSI provisioner prefixes.
var cloudProvisionerPrefixes = []string{
	awsEBSProvisioner,
	"efs.csi.aws.com",
	"disk.csi.azure.com",
	"file.csi.azure.com",
	"pd.csi.storage.gke.io",
	"filestore.csi.storage.gke.io",
	"csi.vsphere.volume",
}

// cephProvisionerPrefixes lists known Ceph CSI provisioner prefixes.
// Using HasPrefix (same strategy as cloud) prevents false-positive matches
// from third-party provisioners whose names merely contain a Ceph substring.
var cephProvisionerPrefixes = []string{
	cephRBDProvisioner,
	"cephfs.csi.ceph.com",
	"openshift-storage." + cephRBDProvisioner,
	"openshift-storage.cephfs.csi.ceph.com",
}

// classifyStorageType determines the storage backend type from the provisioner string.
func classifyStorageType(provisioner string) api.StorageType {
	p := strings.ToLower(provisioner)
	for _, prefix := range cephProvisionerPrefixes {
		if strings.HasPrefix(p, prefix) {
			return api.StorageTypeCeph
		}
	}
	for _, prefix := range cloudProvisionerPrefixes {
		if strings.HasPrefix(p, prefix) {
			return api.StorageTypeCloud
		}
	}
	return api.StorageTypeOther
}

// ScoringInput bundles all data the scorer needs.
type ScoringInput struct {
	SourceVM api.SourceVMInfo
	// ClusterSCs maps cluster name -> []SCProvisioner (from ACM Search API)
	ClusterSCs map[string][]SCProvisioner
	// NodeMetrics maps cluster name -> []NodeMetrics (from Thanos)
	NodeMetrics api.ClusterNodeMetrics
	// CephMetrics maps cluster name -> CephMetrics (from Thanos)
	CephMetrics map[string]api.CephMetrics
}

// Score evaluates all candidate clusters and returns ranked candidates plus excluded entries.
func Score(input ScoringInput) ([]api.CandidateCluster, []api.ExcludedCluster) {
	sourceVolumesSCs := make(map[string]bool)
	for _, v := range input.SourceVM.Volumes {
		if v.StorageClass != "" {
			sourceVolumesSCs[v.StorageClass] = true
		}
	}

	var candidates = make([]api.CandidateCluster, 0, len(input.ClusterSCs))
	var excluded = make([]api.ExcludedCluster, 0, len(input.ClusterSCs))

	for cluster, scs := range input.ClusterSCs {
		// Skip the source cluster itself
		if cluster == input.SourceVM.Cluster {
			continue
		}

		// --- Filter 1: StorageClass compatibility ---
		matchedSCs, matchedProvisioners := matchStorageClasses(sourceVolumesSCs, scs)
		if len(matchedSCs) == 0 {
			excluded = append(excluded, api.ExcludedCluster{
				Cluster: cluster,
				Reason:  "No matching StorageClass found on target cluster",
			})
			continue
		}

		// --- Filter 2: Node capacity ---
		nodes := input.NodeMetrics[cluster]
		if len(nodes) == 0 {
			excluded = append(excluded, api.ExcludedCluster{
				Cluster: cluster,
				Reason:  "No schedulable node metrics available (observability may be unhealthy)",
			})
			continue
		}
		bestNode, hasCap := bestCapableNode(nodes, input.SourceVM)
		if !hasCap {
			excluded = append(excluded, api.ExcludedCluster{
				Cluster: cluster,
				Reason:  "No node has sufficient CPU and memory for the VM",
			})
			continue
		}

		// --- Classify storage type from the matched StorageClasses ---
		storageType := classifyFromProvisioners(matchedProvisioners)

		// --- Compute scores ---
		cpuScore := computeCPUScore(bestNode, input.SourceVM.CPUCores)
		memScore := computeMemScore(bestNode, input.SourceVM.MemoryBytes)

		if storageType == api.StorageTypeOther {
			excluded = append(excluded, api.ExcludedCluster{
				Cluster: cluster,
				Reason:  "Unsupported storage type: only cloud and Ceph storage classes are supported",
			})
			continue
		}

		cephMetrics := input.CephMetrics[cluster]
		if storageType == api.StorageTypeCeph && cephMetrics.TotalBytes == 0 {
			excluded = append(excluded, api.ExcludedCluster{
				Cluster: cluster,
				Reason:  "Ceph storage capacity data is unavailable (metrics not reported for this cluster)",
			})
			continue
		}

		storageScore, cephAvail, storageOK := computeStorageScore(
			storageType,
			input.SourceVM.Volumes,
			cephMetrics,
		)
		if !storageOK {
			excluded = append(excluded, api.ExcludedCluster{
				Cluster: cluster,
				Reason:  "Insufficient storage space on target cluster",
			})
			continue
		}

		totalScore := weightCPU*cpuScore + weightMemory*memScore + weightStorage*storageScore

		candidates = append(candidates, api.CandidateCluster{
			Cluster:               cluster,
			TotalScore:            round2(totalScore),
			CPUScore:              round2(cpuScore),
			MemoryScore:           round2(memScore),
			StorageScore:          round2(storageScore),
			MatchedStorageClasses: matchedSCs,
			BestNode:              bestNode.NodeName,
			AvailableCPUCores:     round2(bestNode.AvailableCPUCores()),
			AvailableMemoryBytes:  bestNode.AvailableMemBytes(),
			StorageType:           storageType,
			CephAvailableBytes:    cephAvail,
		})
	}

	// Sort candidates by total score descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].TotalScore > candidates[j].TotalScore
	})

	return candidates, excluded
}

// matchStorageClasses returns the intersection of source SC names with target SC names,
// also returning the matching SCProvisioner objects.
func matchStorageClasses(sourceVolumesSCs map[string]bool, targetSCs []SCProvisioner) ([]string, []SCProvisioner) {
	var matchedNames []string
	var matchedProvs []SCProvisioner
	for _, sc := range targetSCs {
		if sourceVolumesSCs[sc.Name] {
			matchedNames = append(matchedNames, sc.Name)
			matchedProvs = append(matchedProvs, sc)
		}
	}
	return matchedNames, matchedProvs
}

// bestCapableNode finds the node with the most available CPU that still satisfies
// the VM's CPU and memory requirements. Returns the node and whether one was found.
func bestCapableNode(nodes []api.NodeMetrics, vm api.SourceVMInfo) (api.NodeMetrics, bool) {
	vmCPU := float64(vm.CPUCores)
	vmMem := vm.MemoryBytes

	var best api.NodeMetrics
	found := false
	for _, n := range nodes {
		if n.AvailableCPUCores() >= vmCPU && n.AvailableMemBytes() >= vmMem {
			if !found || n.AvailableCPUCores() > best.AvailableCPUCores() {
				best = n
				found = true
			}
		}
	}
	return best, found
}

// classifyFromProvisioners returns the storage type based on the matched provisioners.
// Ceph takes priority, then cloud, then other.
func classifyFromProvisioners(provs []SCProvisioner) api.StorageType {
	hasCeph, hasCloud := false, false
	for _, p := range provs {
		t := classifyStorageType(p.Provisioner)
		switch t {
		case api.StorageTypeCeph:
			hasCeph = true
		case api.StorageTypeCloud:
			hasCloud = true
		}
	}
	if hasCeph {
		return api.StorageTypeCeph
	}
	if hasCloud {
		return api.StorageTypeCloud
	}
	return api.StorageTypeOther
}

// computeCPUScore returns a 0-100 score representing CPU headroom on the best node.
func computeCPUScore(node api.NodeMetrics, vmCPU int64) float64 {
	if node.AllocatableCPUCores == 0 {
		return 0
	}
	avail := node.AvailableCPUCores() - float64(vmCPU)
	score := (avail / node.AllocatableCPUCores) * 100
	return clamp(score)
}

// computeMemScore returns a 0-100 score representing memory headroom on the best node.
func computeMemScore(node api.NodeMetrics, vmMem int64) float64 {
	if node.AllocatableMemBytes == 0 {
		return 0
	}
	avail := float64(node.AvailableMemBytes() - vmMem)
	score := (avail / float64(node.AllocatableMemBytes)) * 100
	return clamp(score)
}

// computeStorageScore returns the storage score (0-100), ceph available bytes (if applicable),
// and whether the cluster passes the storage capacity check.
func computeStorageScore(
	storageType api.StorageType,
	volumes []api.VMVolumeInfo,
	ceph api.CephMetrics,
) (score float64, cephAvail int64, ok bool) {
	switch storageType {
	case api.StorageTypeCloud:
		// Cloud storage is elastic; score is maximum since capacity is not a constraint.
		return 100, 0, true

	case api.StorageTypeCeph:
		ratio := float64(ceph.AvailableBytes) / float64(ceph.TotalBytes)
		if ratio < minCephAvailableRatio {
			return 0, ceph.AvailableBytes, false
		}
		// Check that total volume size fits in Ceph available space
		var totalVolumeSize int64
		for _, v := range volumes {
			totalVolumeSize += v.SizeBytes
		}
		if ceph.AvailableBytes < totalVolumeSize {
			return 0, ceph.AvailableBytes, false
		}
		return clamp(ratio * 100), ceph.AvailableBytes, true

	}
	// StorageTypeOther and any unknown types are excluded before reaching this
	// function, so this path is unreachable in practice.
	return 0, 0, false
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
