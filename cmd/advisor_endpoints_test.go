/*
Copyright (c) 2026 Red Hat, Inc.
Copyright Contributors to the Open Cluster Management project

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"testing"

	"github.com/stolostron/mtv-integrations/controllers/migrationadvisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func searchCR(namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "search.open-cluster-management.io/v1alpha1",
		"kind":       "Search",
		"metadata": map[string]interface{}{
			"name":      "search-v2-operator",
			"namespace": namespace,
		},
	}}
}

func searchRoute(namespace, host string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "route.openshift.io/v1",
		"kind":       "Route",
		"metadata": map[string]interface{}{
			"name":      "search-api",
			"namespace": namespace,
		},
		"spec": map[string]interface{}{"host": host},
	}}
}

func newAdvisorDynamicClient(t *testing.T, objects ...*unstructured.Unstructured) *fake.FakeDynamicClient {
	t.Helper()
	client := fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(), map[schema.GroupVersionResource]string{searchGVR: "SearchList"})
	for _, obj := range objects {
		gvr := searchGVR
		if obj.GetKind() == "Route" {
			gvr = routeGVR
		}
		require.NoError(t, client.Tracker().Create(gvr, obj, obj.GetNamespace()))
	}
	return client
}

func TestDiscoverSearchAPIEndpoint_CustomSearchNamespace(t *testing.T) {
	for _, tc := range []struct {
		name     string
		route    bool
		host     string
		expected string
	}{
		{"service", false, "", "https://search-search-api.ocm.svc:4010/searchapi/graphql"},
		{"route with empty host", true, "", "https://search-search-api.ocm.svc:4010/searchapi/graphql"},
		{"route", true, "search-api.ocm.example.com", "https://search-api.ocm.example.com/searchapi/graphql"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := []*unstructured.Unstructured{searchCR("ocm")}
			if tc.route {
				objects = append(objects, searchRoute("ocm", tc.host))
			}
			client := newAdvisorDynamicClient(t, objects...)
			searchEndpoint, err := discoverSearchAPIEndpoint(context.Background(), client)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, searchEndpoint)
		})
	}
}

func TestDiscoverSearchAPIEndpoint_InvalidRouteHost(t *testing.T) {
	route := searchRoute("ocm", "search-api.ocm.example.com")
	route.Object["spec"] = map[string]interface{}{"host": int64(123)}
	client := newAdvisorDynamicClient(t, searchCR("ocm"), route)

	_, err := discoverSearchAPIEndpoint(context.Background(), client)
	require.ErrorContains(t, err, "read Route ocm/search-api spec.host")
	require.ErrorContains(t, err, "expected string")
}

func TestDiscoverSearchAPIEndpoint_DefaultSearchNamespace(t *testing.T) {
	client := newAdvisorDynamicClient(t, searchCR(migrationadvisor.DefaultSearchNamespace))
	searchEndpoint, err := discoverSearchAPIEndpoint(context.Background(), client)
	require.NoError(t, err)
	defaultEndpoint := migrationadvisor.SearchAPIServiceEndpoint(migrationadvisor.DefaultSearchNamespace)
	assert.Equal(t, defaultEndpoint, searchEndpoint)
}

func TestDiscoverSearchAPIEndpoint_NoSearchCR(t *testing.T) {
	client := newAdvisorDynamicClient(t)
	searchEndpoint, err := discoverSearchAPIEndpoint(context.Background(), client)
	require.NoError(t, err)
	defaultEndpoint := migrationadvisor.SearchAPIServiceEndpoint(migrationadvisor.DefaultSearchNamespace)
	assert.Equal(t, defaultEndpoint, searchEndpoint)
}

func TestDiscoverSearchAPIEndpoint_SearchInstalledAfterFirstLookup(t *testing.T) {
	client := newAdvisorDynamicClient(t)
	initialEndpoint, err := discoverSearchAPIEndpoint(context.Background(), client)
	require.NoError(t, err)
	defaultEndpoint := migrationadvisor.SearchAPIServiceEndpoint(migrationadvisor.DefaultSearchNamespace)
	assert.Equal(t, defaultEndpoint, initialEndpoint)

	cr := searchCR("ocm")
	require.NoError(t, client.Tracker().Create(searchGVR, cr, cr.GetNamespace()))
	newEndpoint, err := discoverSearchAPIEndpoint(context.Background(), client)
	require.NoError(t, err)
	assert.Equal(t, "https://search-search-api.ocm.svc:4010/searchapi/graphql", newEndpoint)
}

func TestResolveThanosEndpoint_ExplicitOverride(t *testing.T) {
	client := newAdvisorDynamicClient(t)
	thanosEndpoint, err := resolveThanosEndpoint(client, "override-thanos")
	require.NoError(t, err)
	assert.Equal(t, "override-thanos", thanosEndpoint)
	assert.Empty(t, client.Actions())
}

func TestResolveThanosEndpoint_SearchLookupDeniedDoesNotBlockStartup(t *testing.T) {
	client := newAdvisorDynamicClient(t)
	client.PrependReactor("list", "searches", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(searchGVR.GroupResource(), "", nil)
	})
	thanosEndpoint, err := resolveThanosEndpoint(client, "")
	require.NoError(t, err)
	assert.Empty(t, thanosEndpoint)
}

func TestDiscoverSearchAPIEndpoint_SearchLookupDenied(t *testing.T) {
	client := newAdvisorDynamicClient(t)
	client.PrependReactor("list", "searches", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(searchGVR.GroupResource(), "", nil)
	})
	_, err := discoverSearchAPIEndpoint(context.Background(), client)
	require.ErrorContains(t, err, "list Search CRs")
}

func TestDiscoverSearchAPIEndpoint_MultipleSearchCRs(t *testing.T) {
	client := newAdvisorDynamicClient(t, searchCR("ocm"), searchCR("other"))
	_, err := discoverSearchAPIEndpoint(context.Background(), client)
	require.ErrorContains(t, err, "found 2 Search CRs")
}
