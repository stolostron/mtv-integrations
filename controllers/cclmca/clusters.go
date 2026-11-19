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
	"fmt"

	"github.com/stolostron/mtv-integrations/api"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

const (
	proxyCCLMLabelKey   = "acm/proxy-cclm"
	proxyCCLMLabelValue = "true"
	// CodeProxyCCLMNotEnabled is returned when a cluster lacks the proxy-cclm addon label.
	CodeProxyCCLMNotEnabled = "PROXY_CCLM_NOT_ENABLED"
)

func managedClusterHasProxyCCLM(labels map[string]string) bool {
	if labels == nil {
		return false
	}
	return labels[proxyCCLMLabelKey] == proxyCCLMLabelValue
}

// validateProxyCCLMClusters ensures both ManagedClusters exist and carry acm/proxy-cclm=true.
// Returns message, HTTP status, and API error code; all zero when validation passes.
func validateProxyCCLMClusters(
	ctx context.Context, hub dynamic.Interface, clusterA, clusterB string,
) (string, int, string) {
	if hub == nil {
		return "hub cluster client is not configured", 500, "INTERNAL_ERROR"
	}
	for _, name := range []string{clusterA, clusterB} {
		mc, err := hub.Resource(managedClusterGVR).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return fmt.Sprintf("managed cluster %q not found", name), 400, "INVALID_REQUEST"
		}
		if err != nil {
			return fmt.Sprintf("get managed cluster %q: %v", name, err), 502, "CLUSTER_LOOKUP_FAILED"
		}
		if !managedClusterHasProxyCCLM(mc.GetLabels()) {
			return fmt.Sprintf(
				"cluster %q must be labeled %s=%s before kubevirt-ca exchange (deploy the proxy-cclm addon)",
				name, proxyCCLMLabelKey, proxyCCLMLabelValue,
			), 400, CodeProxyCCLMNotEnabled
		}
	}
	return "", 0, ""
}

// validateExchangeClusterFields checks clusterA/clusterB before hub authorization or spoke I/O.
func validateExchangeClusterFields(req api.CACLMExchangeRequest) (*api.CACLMExchangeResponse, int) {
	if req.ClusterA == "" || req.ClusterB == "" {
		return failResponse("INVALID_REQUEST", "clusterA and clusterB are required"), 400
	}
	if req.ClusterA == req.ClusterB {
		return failResponse("INVALID_REQUEST", "clusterA and clusterB must differ"), 400
	}
	return nil, 0
}

// preflightExchange validates the request before spoke access (names, proxy-cclm labels).
func (e *Exchanger) preflightExchange(
	ctx context.Context, req api.CACLMExchangeRequest,
) (*api.CACLMExchangeResponse, int) {
	if resp, status := validateExchangeClusterFields(req); resp != nil {
		return resp, status
	}
	if msg, status, errCode := validateProxyCCLMClusters(ctx, e.HubDynamic, req.ClusterA, req.ClusterB); status != 0 {
		return failResponse(errCode, msg), status
	}
	return nil, 0
}
