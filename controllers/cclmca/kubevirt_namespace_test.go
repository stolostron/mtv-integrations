// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package cclmca

import (
	"testing"

	"github.com/stolostron/mtv-integrations/api"
)

func TestKubeVirtNamespace(t *testing.T) {
	t.Setenv("MTV_KUBEVIRT_NAMESPACE", "env-ns")

	if got := KubeVirtNamespace(""); got != "env-ns" {
		t.Fatalf("empty override: got %q", got)
	}
	if got := KubeVirtNamespace("  custom  "); got != "custom" {
		t.Fatalf("trim override: got %q", got)
	}
}

func TestKubeVirtNamespaceForCluster(t *testing.T) {
	req := api.CACLMExchangeRequest{
		ClusterA:                  "cluster-a",
		ClusterB:                  "cluster-b",
		KubeVirtNamespace:         "shared",
		ClusterAKubeVirtNamespace: "ns-a",
		ClusterBKubeVirtNamespace: "ns-b",
	}
	if got := KubeVirtNamespaceForCluster(req, "cluster-a"); got != "ns-a" {
		t.Fatalf("cluster A: got %q", got)
	}
	if got := KubeVirtNamespaceForCluster(req, "cluster-b"); got != "ns-b" {
		t.Fatalf("cluster B: got %q", got)
	}
	if got := KubeVirtNamespaceForCluster(req, "other"); got != "shared" {
		t.Fatalf("unknown cluster: got %q", got)
	}

	req2 := api.CACLMExchangeRequest{ClusterA: "cluster-a", ClusterB: "cluster-b"}
	if got := KubeVirtNamespaceForCluster(req2, "cluster-a"); got != defaultKubeVirtNamespace {
		t.Fatalf("default A: got %q", got)
	}
}