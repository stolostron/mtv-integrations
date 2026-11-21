// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cclmca

import (
	"context"
	"testing"

	"github.com/stolostron/mtv-integrations/api"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func testManagedCluster(name string, labels map[string]string) *unstructured.Unstructured {
	l := map[string]interface{}{}
	for k, v := range labels {
		l[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cluster.open-cluster-management.io/v1",
		"kind":       "ManagedCluster",
		"metadata": map[string]interface{}{
			"name":   name,
			"labels": l,
		},
	}}
}

func testHubDynamic(t *testing.T, objects ...*unstructured.Unstructured) dynamic.Interface {
	t.Helper()
	gvrToListKind := map[schema.GroupVersionResource]string{
		managedClusterGVR: "ManagedClusterList",
	}
	objs := make([]runtime.Object, len(objects))
	for i, o := range objects {
		objs[i] = o
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind, objs...)
}

func hubDynamicLabeled(names ...string) dynamic.Interface {
	objs := make([]runtime.Object, 0, len(names))
	for _, n := range names {
		objs = append(objs, testManagedCluster(n, map[string]string{proxyCCLMLabelKey: proxyCCLMLabelValue}))
	}
	gvrToListKind := map[schema.GroupVersionResource]string{
		managedClusterGVR: "ManagedClusterList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind, objs...)
}

func TestValidateProxyCCLMClusters(t *testing.T) {
	hub := testHubDynamic(t,
		testManagedCluster("ok-a", map[string]string{proxyCCLMLabelKey: proxyCCLMLabelValue}),
		testManagedCluster("ok-b", map[string]string{proxyCCLMLabelKey: proxyCCLMLabelValue}),
		testManagedCluster("missing-label", map[string]string{"other": "x"}),
	)
	ctx := context.Background()

	t.Run("both labeled", func(t *testing.T) {
		msg, status, code := validateProxyCCLMClusters(ctx, hub, "ok-a", "ok-b")
		if status != 0 || msg != "" || code != "" {
			t.Fatalf("unexpected %q %d %q", msg, status, code)
		}
	})

	t.Run("missing label", func(t *testing.T) {
		msg, status, code := validateProxyCCLMClusters(ctx, hub, "ok-a", "missing-label")
		if status != 400 || code != CodeProxyCCLMNotEnabled {
			t.Fatalf("status=%d code=%s msg=%s", status, code, msg)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, status, code := validateProxyCCLMClusters(ctx, hub, "ok-a", "no-such-cluster")
		if status != 400 || code != "INVALID_REQUEST" {
			t.Fatalf("status=%d code=%s", status, code)
		}
	})
}

func TestExchangeRequiresProxyCCLMLabel(t *testing.T) {
	caA := string(testCertPEM(t, "a"))
	caB := string(testCertPEM(t, "b"))
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: caA}})
	b := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: caB}})

	hub := testHubDynamic(t,
		testManagedCluster("cluster-a", map[string]string{proxyCCLMLabelKey: proxyCCLMLabelValue}),
		testManagedCluster("cluster-b", nil),
	)
	ex := &Exchanger{
		HubClient:  nil,
		HubDynamic: hub,
		Factory: func(ctx context.Context, cluster string) (SpokeConfigMapClient, error) {
			switch cluster {
			case "cluster-a":
				return a, nil
			case "cluster-b":
				return b, nil
			default:
				return nil, context.Canceled
			}
		},
	}
	resp, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{
		ClusterA: "cluster-a", ClusterB: "cluster-b",
	})
	if code != 400 || resp.Code != CodeProxyCCLMNotEnabled {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}
