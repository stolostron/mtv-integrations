// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package cclmca

import (
	"testing"

	"k8s.io/client-go/rest"
)

func TestRestConfigForClusterProxyHost(t *testing.T) {
	t.Setenv("MTV_CLUSTER_PROXY_USER_SERVER", "https://proxy.test:9092")
	cfg, err := restConfigForClusterProxy(&rest.Config{Host: "https://hub:6443"}, "cnv-1", "tok", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "https://proxy.test:9092/cnv-1" {
		t.Fatalf("Host=%q, want https://proxy.test:9092/cnv-1", cfg.Host)
	}
	if cfg.BearerToken != "tok" {
		t.Fatalf("BearerToken=%q", cfg.BearerToken)
	}
}

func TestRestConfigForClusterProxyDefaultBase(t *testing.T) {
	t.Setenv("MTV_CLUSTER_PROXY_USER_SERVER", "")
	cfg, err := restConfigForClusterProxy(&rest.Config{}, "spoke-a", "abc", "")
	if err != nil {
		t.Fatal(err)
	}
	want := defaultClusterProxyUserServer + "/spoke-a"
	if cfg.Host != want {
		t.Fatalf("Host=%q, want %q", cfg.Host, want)
	}
}
