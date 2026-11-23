// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package cclmca

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stolostron/mtv-integrations/api"
)

func TestCleanupErrorResponse(t *testing.T) {
	resp, code := cleanupErrorResponse("spoke-1", context.DeadlineExceeded)
	if code != http.StatusGatewayTimeout || resp == nil || resp.Code != codeKubevirtCACleanup {
		t.Fatalf("deadline: code=%d resp=%+v", code, resp)
	}
	wantMsg := `cluster "spoke-1": timed out waiting for CNV operator to refresh kubevirt-ca to a single CA`
	if resp.Message != wantMsg {
		t.Fatalf("message=%q want=%q", resp.Message, wantMsg)
	}

	resp, code = cleanupErrorResponse("spoke-1", errors.New("other"))
	if resp != nil || code != 0 {
		t.Fatalf("non-deadline: resp=%+v code=%d", resp, code)
	}
}

func TestClearKubevirtCAIfRequestedDisabled(t *testing.T) {
	caA, caB, resp, code := clearKubevirtCAIfRequested(
		context.Background(),
		api.CACLMExchangeRequest{ClusterA: "a", ClusterB: "b"},
		nil, nil, "ns-a", "ns-b", true, true, 0,
	)
	if caA != nil || caB != nil || resp != nil || code != 0 {
		t.Fatalf("expected no-op, got caA=%v caB=%v resp=%v code=%d", caA, caB, resp, code)
	}
}

func TestClearKubevirtCAIfRequestedSuccess(t *testing.T) {
	caOwnA := testCertPEM(t, "own-a")
	caPeerExtra := testCertPEM(t, "stale-peer")
	caA := string(append(append([]byte(nil), caOwnA...), caPeerExtra...))
	caB := string(testCertPEM(t, "b"))
	const ns = "openshift-cnv"
	a := newFakeSpoke(map[string]map[string]string{ns: {
		kubevirtCAConfigMapName: caA, kubevirtExternalCAConfigMapName: "",
	}})
	a.operatorCA = string(caOwnA)
	b := newFakeSpoke(map[string]map[string]string{ns: {
		kubevirtCAConfigMapName: caB, kubevirtExternalCAConfigMapName: "",
	}})

	refreshedA, refreshedB, resp, code := clearKubevirtCAIfRequested(
		context.Background(),
		api.CACLMExchangeRequest{ClusterA: "cluster-a", ClusterB: "cluster-b", ClearKubevirtCA: true},
		a, b, ns, ns, true, true, 5*time.Second,
	)
	if code != 0 || resp != nil {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
	if len(refreshedA) == 0 || len(refreshedB) == 0 {
		t.Fatal("expected non-empty refreshed bundles")
	}
}

func TestClearKubevirtCAIfRequestedReadClusterBFails(t *testing.T) {
	caOwnA := testCertPEM(t, "own-a")
	caPeerExtra := testCertPEM(t, "stale-peer")
	caA := string(append(append([]byte(nil), caOwnA...), caPeerExtra...))
	const ns = "openshift-cnv"
	a := newFakeSpoke(map[string]map[string]string{ns: {
		kubevirtCAConfigMapName: caA, kubevirtExternalCAConfigMapName: "",
	}})
	a.operatorCA = string(caOwnA)
	b := newFakeSpoke(map[string]map[string]string{}) // cluster B missing kubevirt-ca

	_, _, resp, code := clearKubevirtCAIfRequested(
		context.Background(),
		api.CACLMExchangeRequest{ClusterA: "cluster-a", ClusterB: "cluster-b", ClearKubevirtCA: true},
		a, b, ns, ns, true, false, 5*time.Second,
	)
	if code != 502 || resp == nil {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}
