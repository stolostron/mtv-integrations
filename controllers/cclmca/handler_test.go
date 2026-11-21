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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stolostron/mtv-integrations/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestNewHandler(t *testing.T) {
	h := NewHandler(nil, &rest.Config{Host: "http://unused"}, fake.NewSimpleClientset())
	require.NotNil(t, h.Exchanger)
	require.NotNil(t, h.Exchanger.Factory)
}

func TestHandler_InvalidJSON(t *testing.T) {
	up := fakeUserPermissionServer(map[string][]string{
		"acm-vm-fleet:admin": {"local-cluster"},
	})
	defer up.Close()

	h := NewHandler(nil, &rest.Config{Host: up.URL}, fake.NewSimpleClientset())
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{"))
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var body api.CACLMExchangeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "INVALID_REQUEST", body.Code)
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	h := &Handler{RestConfig: &rest.Config{Host: "http://unused"}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	var body api.CACLMExchangeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "METHOD_NOT_ALLOWED", body.Code)
}

func TestHandler_UnauthorizedJSON(t *testing.T) {
	h := &Handler{RestConfig: &rest.Config{Host: "http://unused"}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/proxy-cclm/ca-exchange", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	var body api.CACLMExchangeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "failed", body.Status)
	assert.Equal(t, errCodeUnauthorized, body.Code)
}

func fakeUserPermissionServer(roleBindings map[string][]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/apis/clusterview.open-cluster-management.io/v1alpha1/userpermissions/"
		if r.Method != http.MethodGet || len(r.URL.Path) <= len(prefix) {
			http.NotFound(w, r)
			return
		}
		roleName := r.URL.Path[len(prefix):]
		bindings := make([]interface{}, 0)
		for _, cluster := range roleBindings[roleName] {
			bindings = append(bindings, map[string]interface{}{
				"cluster":    cluster,
				"namespaces": []interface{}{"*"},
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"apiVersion": "clusterview.open-cluster-management.io/v1alpha1",
			"kind":       "UserPermission",
			"metadata":   map[string]interface{}{"name": roleName},
			"status":     map[string]interface{}{"bindings": bindings},
		})
	}))
}

func TestCheckCallerAuthorizedForExchange_Denied(t *testing.T) {
	up := fakeUserPermissionServer(nil)
	defer up.Close()

	ok, err := checkCallerAuthorizedForExchange(
		context.Background(), nil, &rest.Config{Host: up.URL}, "test", "a", "b",
	)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestHandler_MissingClustersBadRequest(t *testing.T) {
	up := fakeUserPermissionServer(nil)
	defer up.Close()

	h := NewHandler(nil, &rest.Config{Host: up.URL}, nil)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var body api.CACLMExchangeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "INVALID_REQUEST", body.Code)
	assert.Contains(t, body.Message, "clusterA and clusterB")
}

func TestHandler_ForbiddenJSON(t *testing.T) {
	up := fakeUserPermissionServer(nil)
	defer up.Close()

	h := NewHandler(nil, &rest.Config{Host: up.URL}, nil)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"clusterA":"a","clusterB":"b"}`))
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	var body api.CACLMExchangeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, errCodeForbidden, body.Code)
}

func TestHandler_ExchangeSuccessWithPerClusterNamespaces(t *testing.T) {
	up := fakeUserPermissionServer(map[string][]string{
		"acm-vm-fleet:admin": {"local-cluster"},
		"kubevirt.io:admin":  {"cluster-a", "cluster-b"},
	})
	defer up.Close()

	ca := string(testCertPEM(t, "shared-handler"))
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: ca}})
	b := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: ca}})

	h := NewHandler(hubDynamicLabeled("cluster-a", "cluster-b"), &rest.Config{Host: up.URL}, fake.NewSimpleClientset())
	h.SetSpokeFactory(func(ctx context.Context, cluster string) (SpokeConfigMapClient, error) {
		switch cluster {
		case "cluster-a":
			return a, nil
		case "cluster-b":
			return b, nil
		default:
			return nil, context.Canceled
		}
	})

	bodyBytes, err := json.Marshal(api.CACLMExchangeRequest{
		ClusterA:                  "cluster-a",
		ClusterB:                  "cluster-b",
		ClusterAKubeVirtNamespace: "openshift-cnv",
		ClusterBKubeVirtNamespace: "openshift-cnv",
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(bodyBytes)))
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp api.CACLMExchangeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "success", resp.Status)
	assert.Equal(t, CodeAlreadyTrusted, resp.Code)
}