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
	"net/http"
	"time"

	"github.com/stolostron/mtv-integrations/controllers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
)

// HealthPath is the unauthenticated GET endpoint for proxy-cclm CA exchange readiness.
const HealthPath = "/api/v1/proxy-cclm/health"

const healthLeaseProbeName = "proxy-cclm-ca-exchange-health-probe"

// HealthHandler checks hub prerequisites for CA exchange (lease namespace and lease get RBAC).
type HealthHandler struct {
	HubClient kubernetes.Interface
}

// NewHealthHandler builds a HealthHandler.
func NewHealthHandler(hub kubernetes.Interface) *HealthHandler {
	return &HealthHandler{HubClient: hub}
}

// ServeHTTP implements GET HealthPath (no auth).
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.HubClient == nil {
		http.Error(w, "hub client not configured", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	log := ctrl.LoggerFrom(r.Context())
	ns := controllers.MTVIntegrationsNamespace
	if _, err := h.HubClient.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			log.Error(err, "CA exchange health: hub namespace missing", "namespace", ns)
			http.Error(w, "hub namespace for CA exchange is not ready", http.StatusServiceUnavailable)
			return
		}
		log.Error(err, "CA exchange health: hub namespace lookup failed", "namespace", ns)
		http.Error(w, "hub namespace check failed", http.StatusServiceUnavailable)
		return
	}

	// NotFound means the API and lease get permission work; Forbidden would surface here if RBAC is missing.
	_, err := h.HubClient.CoordinationV1().Leases(ns).Get(ctx, healthLeaseProbeName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "CA exchange health: hub lease RBAC check failed", "namespace", ns)
		http.Error(w, "hub lease access check failed", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}