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

package api

// CACLMExchangeRequest triggers bidirectional kubevirt-ca trust exchange between two managed clusters.
type CACLMExchangeRequest struct {
	ClusterA          string `json:"clusterA"`
	ClusterB          string `json:"clusterB"`
	KubeVirtNamespace string `json:"kubeVirtNamespace,omitempty"`
	// ClearKubevirtCA, when true and a spoke needs a write, clears kubevirt-ca ca-bundle once so the CNV
	// operator can republish a single certificate before peer material is written to kubevirt-external-ca.
	// Destructive: removes peer CAs on that spoke until the operator reconciles. Default false.
	ClearKubevirtCA bool `json:"clearKubevirtCA,omitempty"`
}

// CACLMExchangeWarning describes a non-fatal condition after a successful exchange.
type CACLMExchangeWarning struct {
	Cluster string `json:"cluster"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CACLMExchangeClusterStatus reports per-cluster exchange and verification outcome.
type CACLMExchangeClusterStatus struct {
	Cluster          string `json:"cluster"`
	CAVerified       bool   `json:"caVerified"`
	CertificateCount int    `json:"certificateCount"`
	WritePerformed   bool   `json:"writePerformed,omitempty"`
}

// CACLMExchangeResponse is returned by POST /api/v1/proxy-cclm/ca-exchange.
type CACLMExchangeResponse struct {
	Status   string                      `json:"status"`
	Code     string                      `json:"code,omitempty"`
	Message  string                      `json:"message,omitempty"`
	ClusterA *CACLMExchangeClusterStatus `json:"clusterA,omitempty"`
	ClusterB *CACLMExchangeClusterStatus `json:"clusterB,omitempty"`
	Warnings []CACLMExchangeWarning      `json:"warnings,omitempty"`
}
