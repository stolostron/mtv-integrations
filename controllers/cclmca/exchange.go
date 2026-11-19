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
	"errors"
	"fmt"
	"time"

	"github.com/stolostron/mtv-integrations/api"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	CodeExchangeInProgress = "CA_EXCHANGE_IN_PROGRESS"
	CodeAlreadyTrusted     = "CA_ALREADY_TRUSTED"
	codeVerifyTimeout      = "VERIFY_TIMEOUT"
	statusFailed           = "failed"
	nearLimitMessage       = "The kubevirt-ca ConfigMap contains %d certificates and is approaching " +
		"the KubeVirt CA bundle limit. Review the bundle and follow the supported KubeVirt cleanup procedure."
	defaultVerifyTimeout = 30 * time.Second
	pollInterval         = time.Second
)

// AdvisorHTTPWriteTimeout is the migration advisor server WriteTimeout. It must exceed
// verification polling plus hub/spoke I/O and lock acquisition.
const AdvisorHTTPWriteTimeout = defaultVerifyTimeout + 60*time.Second

// verifyTimeoutForTests shortens verification polling in unit tests only.
var verifyTimeoutForTests time.Duration

func exchangeVerifyTimeout() time.Duration {
	if verifyTimeoutForTests > 0 {
		return verifyTimeoutForTests
	}
	return defaultVerifyTimeout
}

// SpokeClientFactory creates per-cluster spoke clients.
type SpokeClientFactory func(ctx context.Context, cluster string) (SpokeConfigMapClient, error)

// Exchanger runs bidirectional kubevirt-ca trust exchange.
type Exchanger struct {
	HubClient  kubernetes.Interface
	HubDynamic dynamic.Interface
	Factory    SpokeClientFactory
}

// Exchange performs the CA exchange between clusterA and clusterB.
func (e *Exchanger) Exchange(ctx context.Context, req api.CACLMExchangeRequest) (*api.CACLMExchangeResponse, int) {
	log := ctrl.LoggerFrom(ctx).WithValues("clusterA", req.ClusterA, "clusterB", req.ClusterB)

	if resp, status := e.preflightExchange(ctx, req); resp != nil {
		return resp, status
	}

	ns := KubeVirtNamespace(req.KubeVirtNamespace)
	timeout := exchangeVerifyTimeout()

	clientA, err := e.Factory(ctx, req.ClusterA)
	if err != nil {
		log.Error(err, "spoke client")
		return clusterAccessError(req.ClusterA, req.ClusterB, req.ClusterA, err)
	}
	clientB, err := e.Factory(ctx, req.ClusterB)
	if err != nil {
		log.Error(err, "spoke client")
		return clusterAccessError(req.ClusterA, req.ClusterB, req.ClusterB, err)
	}

	caA, err := readClusterCABundle(ctx, clientA, ns)
	if err != nil {
		log.Error(err, "read kubevirt-ca", "cluster", req.ClusterA)
		return clusterAccessError(req.ClusterA, req.ClusterB, req.ClusterA, err)
	}
	caB, err := readClusterCABundle(ctx, clientB, ns)
	if err != nil {
		log.Error(err, "read kubevirt-ca", "cluster", req.ClusterB)
		return clusterAccessError(req.ClusterA, req.ClusterB, req.ClusterB, err)
	}
	if err := requireNonemptyCABundle(caA); err != nil {
		return clusterAccessError(req.ClusterA, req.ClusterB, req.ClusterA, err)
	}
	if err := requireNonemptyCABundle(caB); err != nil {
		return clusterAccessError(req.ClusterA, req.ClusterB, req.ClusterB, err)
	}

	aTrustsB, err := bundleContainsAll(caA, caB)
	if err != nil {
		return failResponse("PARSE_ERROR", err.Error()), 500
	}
	bTrustsA, err := bundleContainsAll(caB, caA)
	if err != nil {
		return failResponse("PARSE_ERROR", err.Error()), 500
	}

	resp := &api.CACLMExchangeResponse{
		Status: "success",
		ClusterA: &api.CACLMExchangeClusterStatus{
			Cluster: req.ClusterA,
		},
		ClusterB: &api.CACLMExchangeClusterStatus{
			Cluster: req.ClusterB,
		},
	}

	needWriteA := !aTrustsB
	needWriteB := !bTrustsA

	if !needWriteA && !needWriteB {
		resp.Code = CodeAlreadyTrusted
		resp.Message = "Both clusters already trust each other's kubevirt-ca bundles"
		e.finalizeResponse(ctx, resp, clientA, clientB, ns, true, true)
		return resp, 200
	}

	if resp, code := e.preflightExternalCA(ctx, clientA, clientB, ns, req, needWriteA, needWriteB); code != 0 {
		return resp, code
	}

	locks, err := acquireLocksInOrder(ctx, e.HubClient, []string{req.ClusterA, req.ClusterB})
	if err != nil {
		if errors.Is(err, errLockBusy) {
			resp := failResponse(CodeExchangeInProgress,
				"The kubevirt-external-ca ConfigMap is currently in use or contains pending CA data. Please wait and try again.")
			return resp, 409
		}
		log.Error(err, "acquire exchange locks")
		return failResponse("LOCK_ERROR", err.Error()), 500
	}
	defer releaseAll(ctx, locks)

	if needWriteA {
		pending, err := checkExternalCAPending(ctx, clientA, ns)
		if err != nil {
			return clusterAccessError(req.ClusterA, req.ClusterB, req.ClusterA, err)
		}
		if pending {
			return inProgressResponse(), 409
		}
	}
	if needWriteB {
		pending, err := checkExternalCAPending(ctx, clientB, ns)
		if err != nil {
			return clusterAccessError(req.ClusterA, req.ClusterB, req.ClusterB, err)
		}
		if pending {
			return inProgressResponse(), 409
		}
	}

	snapshotA := append([]byte(nil), caA...)
	snapshotB := append([]byte(nil), caB...)

	if needWriteA {
		written, err := writeExternalBundle(ctx, clientA, ns, caB)
		if resp, code := mergeWriteErrorResponse(resp, req.ClusterA, err); code != 0 {
			return resp, code
		}
		resp.ClusterA.WritePerformed = written
	}
	if needWriteB {
		written, err := writeExternalBundle(ctx, clientB, ns, caA)
		if resp, code := mergeWriteErrorResponse(resp, req.ClusterB, err); code != 0 {
			return resp, code
		}
		resp.ClusterB.WritePerformed = written
	}

	if needWriteA || needWriteB {
		if resp, code := e.handleVerify(ctx, resp, clientA, clientB, ns, snapshotA, snapshotB, timeout); code != 0 {
			return resp, code
		}
	}

	e.finalizeResponse(ctx, resp, clientA, clientB, ns, resp.ClusterA.CAVerified, resp.ClusterB.CAVerified)
	log.Info("CA exchange completed", "status", resp.Status,
		"countA", resp.ClusterA.CertificateCount, "countB", resp.ClusterB.CertificateCount)
	return resp, 200
}

func (e *Exchanger) handleVerify(
	ctx context.Context,
	resp *api.CACLMExchangeResponse,
	clientA, clientB SpokeConfigMapClient,
	ns string,
	snapshotA, snapshotB []byte,
	timeout time.Duration,
) (*api.CACLMExchangeResponse, int) {
	expectBInA, _ := expectedFingerprintsFromSource(snapshotB)
	expectAInB, _ := expectedFingerprintsFromSource(snapshotA)

	aVerified, bVerified, rotA, rotB, verr := verifyBidirectional(
		ctx, clientA, clientB, ns,
		expectBInA, expectAInB,
		snapshotA, snapshotB,
		timeout,
	)

	if rotA || rotB {
		resp.Status = statusFailed
		resp.Code = "CA_ROTATED_DURING_EXCHANGE"
		resp.Message = "KubeVirt CA bundle changed during exchange; retry after reconcile"
		aVerifiedFinal := aVerified && !rotA && !rotB
		bVerifiedFinal := bVerified && !rotB && !rotA
		resp.ClusterA.CAVerified = aVerifiedFinal
		resp.ClusterB.CAVerified = bVerifiedFinal
		e.finalizeResponse(ctx, resp, clientA, clientB, ns, aVerifiedFinal, bVerifiedFinal)
		return resp, 409
	}

	resp.ClusterA.CAVerified = aVerified
	resp.ClusterB.CAVerified = bVerified

	if verr != nil || !aVerified || !bVerified {
		return e.verifyFailureResponse(ctx, resp, clientA, clientB, ns, aVerified, bVerified, verr)
	}
	return resp, 0
}

func (e *Exchanger) verifyFailureResponse(
	ctx context.Context,
	resp *api.CACLMExchangeResponse,
	clientA, clientB SpokeConfigMapClient,
	ns string,
	aVerified, bVerified bool,
	verr error,
) (*api.CACLMExchangeResponse, int) {
	if aVerified && !bVerified {
		resp.Status = "partial"
		resp.Code = "VERIFY_PARTIAL"
		resp.Message = "Cluster A verified peer trust; cluster B verification timed out"
		e.finalizeResponse(ctx, resp, clientA, clientB, ns, aVerified, bVerified)
		return resp, 504
	}
	if bVerified && !aVerified {
		resp.Status = "partial"
		resp.Code = "VERIFY_PARTIAL"
		resp.Message = "Cluster B verified peer trust; cluster A verification timed out"
		e.finalizeResponse(ctx, resp, clientA, clientB, ns, aVerified, bVerified)
		return resp, 504
	}
	resp.Status = "timeout"
	resp.Code = codeVerifyTimeout
	if verr != nil {
		resp.Message = "Timed out waiting for kubevirt-ca to reflect exchanged trust"
	} else {
		resp.Message = "Expected certificates were not observed in kubevirt-ca before timeout"
	}
	e.finalizeResponse(ctx, resp, clientA, clientB, ns, aVerified, bVerified)
	return resp, 504
}

func (e *Exchanger) finalizeResponse(
	ctx context.Context,
	resp *api.CACLMExchangeResponse,
	clientA, clientB SpokeConfigMapClient,
	ns string,
	aVerified, bVerified bool,
) {
	if n, err := countCABundle(ctx, clientA, ns); err == nil {
		resp.ClusterA.CertificateCount = n
		if aVerified {
			resp.ClusterA.CAVerified = true
		}
	}
	if n, err := countCABundle(ctx, clientB, ns); err == nil {
		resp.ClusterB.CertificateCount = n
		if bVerified {
			resp.ClusterB.CAVerified = true
		}
	}
	for _, tc := range []struct {
		name   string
		status *api.CACLMExchangeClusterStatus
	}{
		{resp.ClusterA.Cluster, resp.ClusterA},
		{resp.ClusterB.Cluster, resp.ClusterB},
	} {
		if tc.status != nil && tc.status.CertificateCount > CertCountWarningThreshold {
			resp.Warnings = append(resp.Warnings, api.CACLMExchangeWarning{
				Cluster: tc.name,
				Code:    "KUBEVIRT_CA_NEAR_LIMIT",
				Message: fmt.Sprintf(nearLimitMessage, tc.status.CertificateCount),
			})
		}
	}
}

func inProgressResponse() *api.CACLMExchangeResponse {
	return failResponse(CodeExchangeInProgress,
		"The kubevirt-external-ca ConfigMap is currently in use or contains pending CA data. Please wait and try again.")
}

func readClusterCABundle(ctx context.Context, client SpokeConfigMapClient, ns string) ([]byte, error) {
	cm, err := client.GetConfigMap(ctx, ns, kubevirtCAConfigMapName)
	if err != nil {
		return nil, err
	}
	return readCABundle(cm)
}

func countCABundle(ctx context.Context, client SpokeConfigMapClient, ns string) (int, error) {
	bundle, err := readClusterCABundle(ctx, client, ns)
	if err != nil {
		return 0, err
	}
	return certificateCount(bundle)
}

func failResponse(code, msg string) *api.CACLMExchangeResponse {
	return &api.CACLMExchangeResponse{Status: statusFailed, Code: code, Message: msg}
}

func clusterError(cluster string, err error) *api.CACLMExchangeResponse {
	return &api.CACLMExchangeResponse{
		Status:  "failed",
		Code:    "CLUSTER_UNREACHABLE",
		Message: fmt.Sprintf("cluster %q: %v", cluster, err),
	}
}

func exchangeWriteError(clusterA, clusterB, cluster string, err error) (*api.CACLMExchangeResponse, int) {
	if err == nil {
		return nil, 0
	}
	if errors.Is(err, errKubevirtExternalCANotFound) {
		return failResponse("KUBEVIRT_EXTERNAL_CA_NOT_FOUND",
			fmt.Sprintf("cluster %q: kubevirt-external-ca ConfigMap must exist before exchange", cluster)), 400
	}
	if errors.Is(err, errConfigMapConflict) {
		return inProgressResponse(), 409
	}
	return clusterAccessError(clusterA, clusterB, cluster, err)
}

func checkExternalCAPending(ctx context.Context, client SpokeConfigMapClient, ns string) (bool, error) {
	cm, err := client.GetConfigMap(ctx, ns, kubevirtExternalCAConfigMapName)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	bundle, err := readCABundle(cm)
	if err != nil {
		return false, err
	}
	return pendingExternalCABundle(string(bundle)), nil
}

func (e *Exchanger) preflightExternalCA(
	ctx context.Context,
	clientA, clientB SpokeConfigMapClient,
	ns string,
	req api.CACLMExchangeRequest,
	needWriteA, needWriteB bool,
) (*api.CACLMExchangeResponse, int) {
	if needWriteA {
		if resp, code := e.requireExternalCAConfigMap(ctx, clientA, ns, req.ClusterA, req.ClusterB, req.ClusterA); code != 0 {
			return resp, code
		}
	}
	if needWriteB {
		if resp, code := e.requireExternalCAConfigMap(ctx, clientB, ns, req.ClusterA, req.ClusterB, req.ClusterB); code != 0 {
			return resp, code
		}
	}
	return nil, 0
}

func (e *Exchanger) requireExternalCAConfigMap(
	ctx context.Context,
	client SpokeConfigMapClient,
	ns, clusterA, clusterB, failing string,
) (*api.CACLMExchangeResponse, int) {
	_, err := client.GetConfigMap(ctx, ns, kubevirtExternalCAConfigMapName)
	if apierrors.IsNotFound(err) {
		return exchangeWriteError(clusterA, clusterB, failing, errKubevirtExternalCANotFound)
	}
	if err != nil {
		return clusterAccessError(clusterA, clusterB, failing, err)
	}
	return nil, 0
}

// mergeWriteErrorResponse returns a write error response while preserving per-cluster progress.
func mergeWriteErrorResponse(
	base *api.CACLMExchangeResponse,
	cluster string,
	err error,
) (*api.CACLMExchangeResponse, int) {
	clusterA, clusterB := "", ""
	if base.ClusterA != nil {
		clusterA = base.ClusterA.Cluster
	}
	if base.ClusterB != nil {
		clusterB = base.ClusterB.Cluster
	}
	out, code := exchangeWriteError(clusterA, clusterB, cluster, err)
	if code == 0 {
		return base, 0
	}
	out.ClusterA = base.ClusterA
	out.ClusterB = base.ClusterB
	return out, code
}

func writeExternalBundle(ctx context.Context, dest SpokeConfigMapClient, ns string, sourceBundle []byte) (bool, error) {
	if err := upsertExternalCABundle(ctx, dest, ns, sourceBundle); err != nil {
		return false, err
	}
	return true, nil
}

func verifyBidirectional(
	ctx context.Context,
	clientA, clientB SpokeConfigMapClient,
	ns string,
	expectBInA, expectAInB map[string]struct{},
	snapshotA, snapshotB []byte,
	timeout time.Duration,
) (aOK, bOK, rotA, rotB bool, err error) {
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		ok  bool
		err error
	}
	chA := make(chan result, 1)
	chB := make(chan result, 1)

	go func() {
		ok, e := pollTrustEstablished(verifyCtx, clientA, ns, expectBInA, pollInterval)
		chA <- result{ok: ok, err: e}
	}()
	go func() {
		ok, e := pollTrustEstablished(verifyCtx, clientB, ns, expectAInB, pollInterval)
		chB <- result{ok: ok, err: e}
	}()

	var resA, resB result
	received := 0
	for received < 2 {
		select {
		case r := <-chA:
			resA = r
			received++
		case r := <-chB:
			resB = r
			received++
		case <-verifyCtx.Done():
			rotA, rotErrA := bundleRotated(ctx, clientA, ns, snapshotA)
			rotB, rotErrB := bundleRotated(ctx, clientB, ns, snapshotB)
			if rotErrA != nil {
				return resA.ok, resB.ok, rotA, rotB, rotErrA
			}
			if rotErrB != nil {
				return resA.ok, resB.ok, rotA, rotB, rotErrB
			}
			return resA.ok, resB.ok, rotA, rotB, verifyCtx.Err()
		}
	}

	rotA, rotErrA := bundleRotated(ctx, clientA, ns, snapshotA)
	rotB, rotErrB := bundleRotated(ctx, clientB, ns, snapshotB)
	if rotErrA != nil {
		return resA.ok, resB.ok, rotA, rotB, rotErrA
	}
	if rotErrB != nil {
		return resA.ok, resB.ok, rotA, rotB, rotErrB
	}
	if (rotA && resA.ok) || (rotB && resB.ok) {
		return resA.ok && !rotA, resB.ok && !rotB, rotA, rotB, fmt.Errorf("kubevirt-ca rotated during verification")
	}

	if resA.err != nil && !errors.Is(resA.err, context.DeadlineExceeded) && !errors.Is(resA.err, context.Canceled) {
		return resA.ok, resB.ok, rotA, rotB, resA.err
	}
	if resB.err != nil && !errors.Is(resB.err, context.DeadlineExceeded) && !errors.Is(resB.err, context.Canceled) {
		return resA.ok, resB.ok, rotA, rotB, resB.err
	}
	if !resA.ok || !resB.ok {
		return resA.ok, resB.ok, rotA, rotB, context.DeadlineExceeded
	}
	return true, true, rotA, rotB, nil
}

func bundleRotated(ctx context.Context, client SpokeConfigMapClient, ns string, snapshot []byte) (bool, error) {
	cur, err := readClusterCABundle(ctx, client, ns)
	if err != nil {
		return false, err
	}
	snapshotFP, err := fingerprintSet(snapshot)
	if err != nil {
		return false, err
	}
	currentFP, err := fingerprintSet(cur)
	if err != nil {
		return false, err
	}
	for fp := range snapshotFP {
		if _, ok := currentFP[fp]; !ok {
			return true, nil
		}
	}
	return false, nil
}
