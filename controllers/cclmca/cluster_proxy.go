// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package cclmca

import (
	"fmt"
	"os"
	"strings"

	"k8s.io/client-go/rest"
)

// defaultClusterProxyUserServer is the in-cluster cluster-proxy user-server base URL
// (no trailing slash, no managed cluster name). Override with MTV_CLUSTER_PROXY_USER_SERVER.
// MCE installs cluster-proxy in multicluster-engine (port 9092). Override with MTV_CLUSTER_PROXY_USER_SERVER.
const defaultClusterProxyUserServer = "https://cluster-proxy-addon-user.multicluster-engine.svc:9092"

// clusterProxyModeDirect is the MTV_CCLM_CLUSTER_PROXY_MODE value for kind/e2e that
// dials the spoke API URL on ManagedCluster (single apiserver); production hubs must
// use the default cluster-proxy user-server.
const clusterProxyModeDirect = "direct"

func clusterProxyUserServerBase() string {
	if v := strings.TrimSpace(os.Getenv("MTV_CLUSTER_PROXY_USER_SERVER")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultClusterProxyUserServer
}

func clusterProxyMode() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("MTV_CCLM_CLUSTER_PROXY_MODE")))
}

// restConfigForClusterProxy builds a client-go config that sends Kubernetes API traffic to
// a managed cluster through the cluster-proxy user-server using the MTV provider token
// (ManagedServiceAccount on the spoke). Request paths become {base}/{cluster}/api/v1/...
// (see cluster-proxy service-proxy docs).
func restConfigForClusterProxy(
	hub *rest.Config,
	clusterName, bearerToken, serviceCAPath string,
) (*rest.Config, error) {
	if strings.TrimSpace(bearerToken) == "" {
		return nil, fmt.Errorf("bearer token required for spoke access")
	}
	if strings.TrimSpace(clusterName) == "" {
		return nil, fmt.Errorf("managed cluster name required")
	}

	cfg := rest.CopyConfig(hub)
	cfg.BearerToken = bearerToken
	cfg.BearerTokenFile = ""
	cfg.Impersonate = rest.ImpersonationConfig{}
	cfg.CertFile = ""
	cfg.KeyFile = ""
	cfg.CertData = nil
	cfg.KeyData = nil

	base := clusterProxyUserServerBase()
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	cfg.Host = strings.TrimRight(base, "/") + "/" + clusterName

	if err := mergeOpenShiftServiceCA(&cfg.TLSClientConfig, serviceCAPath); err != nil {
		return nil, fmt.Errorf("cluster %q cluster-proxy TLS: %w", clusterName, err)
	}
	return cfg, nil
}

// mergeOpenShiftServiceCA appends the OpenShift service CA PEM (user-server serving cert) to the
// hub API CA bundle on cfg. serviceCAPath is optional; missing file is ignored (tests, vanilla k8s).
func mergeOpenShiftServiceCA(cfg *rest.TLSClientConfig, serviceCAPath string) error {
	path := strings.TrimSpace(serviceCAPath)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read service CA %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil
	}
	if len(cfg.CAData) > 0 {
		cfg.CAData = append(append([]byte{}, cfg.CAData...), data...)
	} else if cfg.CAFile != "" {
		existing, readErr := os.ReadFile(cfg.CAFile)
		if readErr != nil {
			return fmt.Errorf("read cluster CA file %s: %w", cfg.CAFile, readErr)
		}
		cfg.CAData = append(append([]byte{}, existing...), data...)
		cfg.CAFile = ""
	} else {
		cfg.CAData = append([]byte{}, data...)
	}
	return nil
}
