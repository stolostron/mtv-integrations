// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

// fakeSearchResponse builds a search API JSON response from the given items.
func fakeSearchResponse(items []map[string]interface{}) string {
	b, _ := json.Marshal(map[string]interface{}{
		"data": map[string]interface{}{
			"search": []interface{}{
				map[string]interface{}{"items": items},
			},
		},
	})
	return string(b)
}

// newSearchClient returns a SearchClient wired to the given test server URL.
func newSearchClient(serverURL string) *SearchClient {
	return &SearchClient{
		RestConfig:        &rest.Config{Host: serverURL},
		SearchAPIEndpoint: serverURL + "/searchapi/graphql",
	}
}

// ── endpoint ─────────────────────────────────────────────────────────────────

func TestSearchClientEndpoint_Default(t *testing.T) {
	c := &SearchClient{RestConfig: &rest.Config{}}
	assert.Equal(t, searchAPIService, c.endpoint())
}

func TestSearchClientEndpoint_Override(t *testing.T) {
	c := &SearchClient{
		RestConfig:        &rest.Config{},
		SearchAPIEndpoint: "https://custom-search.example.com/graphql",
	}
	assert.Equal(t, "https://custom-search.example.com/graphql", c.endpoint())
}

// ── ListStorageClassProvisionersByCluster ────────────────────────────────────

func TestListStorageClassProvisionersByCluster(t *testing.T) {
	items := []map[string]interface{}{
		{"cluster": "cluster-a", "name": "ceph-rbd", "provisioner": "rbd.csi.ceph.com"},
		{"cluster": "cluster-a", "name": "gp2", "provisioner": "ebs.csi.aws.com"},
		{"cluster": "cluster-b", "name": "ceph-rbd", "provisioner": "rbd.csi.ceph.com"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeSearchResponse(items)))
	}))
	defer srv.Close()

	result, err := newSearchClient(srv.URL).ListStorageClassProvisionersByCluster(context.Background())
	require.NoError(t, err)
	require.Contains(t, result, "cluster-a")
	assert.Len(t, result["cluster-a"], 2)
	require.Contains(t, result, "cluster-b")
	assert.Len(t, result["cluster-b"], 1)
	assert.Equal(t, "ceph-rbd", result["cluster-b"][0].Name)
	assert.Equal(t, "rbd.csi.ceph.com", result["cluster-b"][0].Provisioner)
}

func TestListStorageClassProvisionersByCluster_Pagination(t *testing.T) {
	// First response returns exactly searchPageSize items so a second page is requested.
	// Second response returns fewer items, ending the loop.
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page++
		var items []map[string]interface{}
		if page == 1 {
			// Fill a full page.
			for i := 0; i < searchPageSize; i++ {
				items = append(items, map[string]interface{}{
					"cluster": "c1", "name": "sc", "provisioner": "prov",
				})
			}
		} else {
			items = []map[string]interface{}{
				{"cluster": "c1", "name": "sc2", "provisioner": "prov2"},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeSearchResponse(items)))
	}))
	defer srv.Close()

	result, err := newSearchClient(srv.URL).ListStorageClassProvisionersByCluster(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, page, "should have fetched exactly 2 pages")
	assert.NotEmpty(t, result["c1"])
}

func TestListStorageClassProvisionersByCluster_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Empty search array — no more pages.
		_, _ = w.Write([]byte(`{"data":{"search":[]}}`))
	}))
	defer srv.Close()

	result, err := newSearchClient(srv.URL).ListStorageClassProvisionersByCluster(context.Background())
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestListStorageClassProvisionersByCluster_SkipsMissingFields(t *testing.T) {
	// Items with empty cluster or name are silently skipped.
	items := []map[string]interface{}{
		{"cluster": "", "name": "sc1", "provisioner": "prov"},     // empty cluster
		{"cluster": "c1", "name": "", "provisioner": "prov"},      // empty name
		{"cluster": "c1", "name": "sc-ok", "provisioner": "prov"}, // valid
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeSearchResponse(items)))
	}))
	defer srv.Close()

	result, err := newSearchClient(srv.URL).ListStorageClassProvisionersByCluster(context.Background())
	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "sc-ok", result["c1"][0].Name)
}

func TestListStorageClassProvisionersByCluster_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	_, err := newSearchClient(srv.URL).ListStorageClassProvisionersByCluster(context.Background())
	assert.Error(t, err)
}

func TestListStorageClassProvisionersByCluster_GraphQLErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"unauthorized"}]}`))
	}))
	defer srv.Close()

	_, err := newSearchClient(srv.URL).ListStorageClassProvisionersByCluster(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unauthorized")
}

func TestListStorageClassProvisionersByCluster_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	_, err := newSearchClient(srv.URL).ListStorageClassProvisionersByCluster(context.Background())
	assert.Error(t, err)
}

// ── serviceCAPath ─────────────────────────────────────────────────────────────

// TestSearchClient_ServiceCAPath_Default verifies that serviceCAPath() falls
// back to DefaultServiceCAPath when ServiceCAPath is empty.
func TestSearchClient_ServiceCAPath_Default(t *testing.T) {
	c := &SearchClient{}
	assert.Equal(t, DefaultServiceCAPath, c.serviceCAPath())
}

// TestSearchClient_ServiceCAPath_Override verifies that an explicit
// ServiceCAPath is returned unchanged.
func TestSearchClient_ServiceCAPath_Override(t *testing.T) {
	c := &SearchClient{ServiceCAPath: "/custom/ca.crt"}
	assert.Equal(t, "/custom/ca.crt", c.serviceCAPath())
}
