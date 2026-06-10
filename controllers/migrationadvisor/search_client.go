// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"k8s.io/client-go/rest"
)

const (
	// searchAPIService is the in-cluster address of the ACM Search API service.
	searchAPIService = "https://search-search-api.open-cluster-management.svc:4010/searchapi/graphql"
)

// SearchClient queries the ACM Search API via GraphQL.
type SearchClient struct {
	RestConfig *rest.Config
	// SearchAPIEndpoint overrides searchAPIService for testing.
	SearchAPIEndpoint string
	// ServiceCAPath is the path to the OpenShift service CA bundle PEM file.
	// Defaults to DefaultServiceCAPath when empty.
	ServiceCAPath string
	// TLSOpts is an optional function applied to the TLS configuration of the
	// outbound HTTP client. Used to inherit the cluster's central TLS profile.
	TLSOpts func(*tls.Config)
}

func (s *SearchClient) serviceCAPath() string {
	if s.ServiceCAPath != "" {
		return s.ServiceCAPath
	}
	return DefaultServiceCAPath
}

func (s *SearchClient) endpoint() string {
	if s.SearchAPIEndpoint != "" {
		return s.SearchAPIEndpoint
	}
	return searchAPIService
}

type graphQLRequest struct {
	Query string `json:"query"`
}

type searchResult struct {
	Data struct {
		Search []struct {
			Items []map[string]interface{} `json:"items"`
		} `json:"search"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// SCProvisioner holds the name and provisioner of a StorageClass on a given cluster.
type SCProvisioner struct {
	Name        string
	Provisioner string
}

// searchPageSize is the number of items fetched per Search API page.
// The ACM Search API supports limit/offset pagination; keeping pages at 1 000
// prevents oversized JSON responses while still handling thousands of clusters.
const searchPageSize = 1000

// ListStorageClassProvisionersByCluster returns a map of cluster -> []SCProvisioner,
// including the provisioner field so the scorer can classify cloud vs Ceph vs other.
//
// It paginates automatically so the result is correct regardless of how many
// managed clusters are registered (no hard 10 000-item ceiling).
func (s *SearchClient) ListStorageClassProvisionersByCluster(
	ctx context.Context,
) (map[string][]SCProvisioner, error) {
	httpClient, err := buildHTTPClient(s.RestConfig, s.serviceCAPath(), s.TLSOpts)
	if err != nil {
		return nil, fmt.Errorf("build HTTP client: %w", err)
	}

	out := make(map[string][]SCProvisioner)
	offset := 0

	for {
		const filterBlock = `filters: [{ property: "kind", values: ["StorageClass"] }]`

		gqlQuery := fmt.Sprintf(`{
  search(input: [{
    %s
    limit: %d
    offset: %d
  }]) {
    items
  }
}`, filterBlock, searchPageSize, offset)

		body, err := json.Marshal(graphQLRequest{Query: gqlQuery})
		if err != nil {
			return nil, fmt.Errorf("marshal search query: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint(), bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build search request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("search API request: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("search API returned %d: %s", resp.StatusCode, string(raw))
		}

		var result searchResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("decode search response: %w", err)
		}
		_ = resp.Body.Close()

		if len(result.Errors) > 0 {
			return nil, fmt.Errorf("search API errors: %s", result.Errors[0].Message)
		}

		if len(result.Data.Search) == 0 {
			break
		}

		items := result.Data.Search[0].Items
		for _, item := range items {
			cluster, _ := item["cluster"].(string)
			name, _ := item["name"].(string)
			provisioner, _ := item["provisioner"].(string)
			if cluster == "" || name == "" {
				continue
			}
			out[cluster] = append(out[cluster], SCProvisioner{Name: name, Provisioner: provisioner})
		}

		// If fewer items than the page size were returned we have reached the end.
		if len(items) < searchPageSize {
			break
		}
		offset += searchPageSize
	}

	return out, nil
}
