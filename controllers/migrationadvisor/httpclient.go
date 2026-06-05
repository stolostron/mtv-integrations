// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"
)

// DefaultServiceCAPath is the mount path for the OpenShift service CA bundle
// injected by annotating a ConfigMap with
// service.beta.openshift.io/inject-cabundle=true.
// The mtv-integrations Deployment mounts that ConfigMap at this directory.
const DefaultServiceCAPath = "/var/run/secrets/service-ca/service-ca.crt"

// buildHTTPClient creates an *http.Client that:
//   - authenticates with the bearer token from restConfig
//   - trusts the cluster API server CA (from restConfig.TLSClientConfig)
//   - trusts the OpenShift service CA from serviceCAPath, required for
//     in-cluster HTTPS services like search-search-api (signed by the OpenShift
//     Service CA, not the API server CA)
//   - trusts the system root CAs, required for external OpenShift Route TLS
//     certs like rbac-query-proxy (signed by the ingress/router CA)
//
// serviceCAPath is silently skipped when empty or the file does not exist
// (e.g. unit tests or non-OpenShift environments).
func buildHTTPClient(restConfig *rest.Config, serviceCAPath string) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		// SystemCertPool returns an error on some platforms (e.g. Windows).
		// Fall back to an empty pool; explicit CAs below will still be added.
		pool = x509.NewCertPool()
	}

	// Cluster API server CA
	if len(restConfig.CAData) > 0 {
		if !pool.AppendCertsFromPEM(restConfig.CAData) {
			return nil, fmt.Errorf("no certs found in restConfig.CAData")
		}
	}
	if restConfig.CAFile != "" {
		data, err := os.ReadFile(restConfig.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read cluster CA file %s: %w", restConfig.CAFile, err)
		}
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("no certs found in cluster CA file %s", restConfig.CAFile)
		}
	}

	// OpenShift service CA — silently optional when absent (unit tests, vanilla k8s),
	// but any other read error or unparseable content is fatal.
	if serviceCAPath != "" {
		data, err := os.ReadFile(serviceCAPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("read service CA %s: %w", serviceCAPath, err)
			}
		} else if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("no certs found in service CA bundle %s", serviceCAPath)
		}
	}

	// Clone DefaultTransport so proxy settings and other defaults are preserved;
	// only override the fields that must differ.
	//nolint:forcetypeassert // http.DefaultTransport is always *http.Transport
	baseTx := http.DefaultTransport.(*http.Transport).Clone()
	baseTx.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}
	baseTx.DialContext = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	baseTx.TLSHandshakeTimeout = 10 * time.Second
	baseTx.ExpectContinueTimeout = 1 * time.Second
	baseTx.MaxIdleConns = 100
	baseTx.IdleConnTimeout = 90 * time.Second

	// Wrap with auth round-trippers (bearer token, impersonation, etc.)
	transportCfg, err := restConfig.TransportConfig()
	if err != nil {
		return nil, fmt.Errorf("build transport config: %w", err)
	}
	rt, err := transport.HTTPWrappersForConfig(transportCfg, baseTx)
	if err != nil {
		return nil, fmt.Errorf("wrap transport with auth: %w", err)
	}

	return &http.Client{Transport: rt}, nil
}
