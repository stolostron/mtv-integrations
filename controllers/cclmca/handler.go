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
	"strings"

	"github.com/stolostron/mtv-integrations/api"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	errCodeUnauthorized = "UNAUTHORIZED"
	errCodeForbidden    = "FORBIDDEN"
)

// Handler implements POST /api/v1/proxy-cclm/ca-exchange.
type Handler struct {
	DynamicClient dynamic.Interface
	RestConfig    *rest.Config
	HubClient     kubernetes.Interface
	ServiceCAPath string
	Exchanger     *Exchanger
}

// NewHandler builds a Handler with a default spoke client factory.
func NewHandler(
	dyn dynamic.Interface,
	cfg *rest.Config,
	hub kubernetes.Interface,
	serviceCAPath string,
) *Handler {
	ex := &Exchanger{
		HubClient:  hub,
		HubDynamic: dyn,
		Factory: func(ctx context.Context, cluster string) (SpokeConfigMapClient, error) {
			return NewSpokeClient(ctx, cfg, hub, dyn, cluster, serviceCAPath)
		},
	}
	return &Handler{
		DynamicClient: dyn,
		RestConfig:    cfg,
		HubClient:     hub,
		ServiceCAPath: serviceCAPath,
		Exchanger:     ex,
	}
}

func writeCAExchangeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(&api.CACLMExchangeResponse{
		Status:  "failed",
		Code:    code,
		Message: message,
	})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := ctrl.LoggerFrom(r.Context()).WithName("proxy-cclm-ca-exchange")

	if r.Method != http.MethodPost {
		writeCAExchangeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		writeCAExchangeError(w, http.StatusUnauthorized, errCodeUnauthorized, "authorization required")
		return
	}

	var req api.CACLMExchangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCAExchangeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
		return
	}

	ctx := ctrl.LoggerInto(r.Context(), log)

	if resp, status := validateExchangeClusterFields(req); resp != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	allowed, err := checkCallerAuthorizedForExchange(
		ctx, h.DynamicClient, h.RestConfig, parts[1], req.ClusterA, req.ClusterB,
	)
	if err != nil {
		if apierrors.IsUnauthorized(err) {
			writeCAExchangeError(w, http.StatusUnauthorized, errCodeUnauthorized, "authorization required")
			return
		}
		log.Error(err, "authorization check failed")
		writeCAExchangeError(w, http.StatusInternalServerError, "INTERNAL_ERROR",
			http.StatusText(http.StatusInternalServerError))
		return
	}
	if !allowed {
		writeCAExchangeError(w, http.StatusForbidden, errCodeForbidden,
			"forbidden: requires kubevirt.io:admin UserPermission on both clusters")
		return
	}

	resp, status := h.Exchanger.Exchange(ctx, req)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error(err, "encode response")
	}
}

// SetSpokeFactory replaces the spoke client factory (used in unit tests).
func (h *Handler) SetSpokeFactory(factory SpokeClientFactory) {
	h.Exchanger.Factory = factory
}
