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
	"strings"
	"testing"
	"time"

	"github.com/stolostron/mtv-integrations/api"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func setTestVerifyTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	verifyTimeoutForTests = d
	t.Cleanup(func() { verifyTimeoutForTests = 0 })
}

func testExchanger(a, b *fakeSpoke) *Exchanger {
	return &Exchanger{
		HubClient:  fake.NewSimpleClientset(),
		HubDynamic: hubDynamicLabeled("cluster-a", "cluster-b"),
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
}

func TestExchangeAlreadyTrusted(t *testing.T) {
	ca := testCertPEM(t, "shared")
	bundle := string(ca)
	a := newFakeSpoke(map[string]map[string]string{
		defaultKubeVirtNamespace: {kubevirtCAConfigMapName: bundle},
	})
	b := newFakeSpoke(map[string]map[string]string{
		defaultKubeVirtNamespace: {kubevirtCAConfigMapName: bundle},
	})
	b.cms["openshift-cnv/"+kubevirtCAConfigMapName].Data[caBundleDataKey] = bundle

	ex := testExchanger(a, b)
	resp, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{
		ClusterA: "cluster-a", ClusterB: "cluster-b",
	})
	if code != 200 || resp.Code != CodeAlreadyTrusted {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}

func TestExchangeBidirectional(t *testing.T) {
	caA := string(testCertPEM(t, "a"))
	caB := string(testCertPEM(t, "b"))
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {
		kubevirtCAConfigMapName: caA, kubevirtExternalCAConfigMapName: "",
	}})
	b := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {
		kubevirtCAConfigMapName: caB, kubevirtExternalCAConfigMapName: "",
	}})

	setTestVerifyTimeout(t, 5*time.Second)
	ex := testExchanger(a, b)
	resp, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{
		ClusterA: "cluster-a", ClusterB: "cluster-b",
	})
	if code != 200 || resp.Status != "success" {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
	if !resp.ClusterA.CAVerified || !resp.ClusterB.CAVerified {
		t.Fatal("expected verified")
	}
}

func TestExchangeClearKubevirtCA(t *testing.T) {
	caOwnA := testCertPEM(t, "own-a")
	caPeerExtra := testCertPEM(t, "stale-peer")
	caA := string(append(append([]byte(nil), caOwnA...), caPeerExtra...))
	caB := string(testCertPEM(t, "b"))
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {
		kubevirtCAConfigMapName: caA, kubevirtExternalCAConfigMapName: "",
	}})
	a.operatorCA = string(caOwnA)
	b := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {
		kubevirtCAConfigMapName: caB, kubevirtExternalCAConfigMapName: "",
	}})

	setTestVerifyTimeout(t, 5*time.Second)
	ex := testExchanger(a, b)
	resp, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{
		ClusterA:        "cluster-a",
		ClusterB:        "cluster-b",
		ClearKubevirtCA: true,
	})
	if code != 200 || resp.Status != "success" {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}

func TestExchangeExternalCAPending(t *testing.T) {
	caA := string(testCertPEM(t, "a"))
	caB := string(testCertPEM(t, "b"))
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: caA}})
	b := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {
		kubevirtCAConfigMapName:         caB,
		kubevirtExternalCAConfigMapName: "",
	}})
	a.cms["openshift-cnv/"+kubevirtExternalCAConfigMapName] = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: kubevirtExternalCAConfigMapName, Namespace: "openshift-cnv"},
		Data:       map[string]string{caBundleDataKey: "pending"},
	}

	ex := testExchanger(a, b)
	_, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{ClusterA: "cluster-a", ClusterB: "cluster-b"})
	if code != 409 {
		t.Fatalf("expected 409, got %d", code)
	}
}

func TestExchangeOneDirectionOnly(t *testing.T) {
	caA := testCertPEM(t, "a")
	caB := testCertPEM(t, "b")
	trustA := string(append(caA, caB...))
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: trustA}})
	b := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {
		kubevirtCAConfigMapName: string(caB), kubevirtExternalCAConfigMapName: "",
	}})

	setTestVerifyTimeout(t, 5*time.Second)
	ex := testExchanger(a, b)
	resp, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{
		ClusterA: "cluster-a", ClusterB: "cluster-b",
	})
	if code != 200 {
		t.Fatalf("code=%d", code)
	}
	if resp.ClusterA.WritePerformed {
		t.Fatal("A should not write")
	}
	if !resp.ClusterB.WritePerformed {
		t.Fatal("B should write")
	}
}

func TestExchangeEmptyCABundle(t *testing.T) {
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: ""}})
	b := newFakeSpoke(map[string]map[string]string{
		"openshift-cnv": {kubevirtCAConfigMapName: string(testCertPEM(t, "b"))},
	})

	ex := testExchanger(a, b)
	_, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{ClusterA: "cluster-a", ClusterB: "cluster-b"})
	if code != 502 {
		t.Fatalf("expected 502 on empty kubevirt-ca, got %d", code)
	}
}

func TestExchangeMissingExternalCA(t *testing.T) {
	caA := string(testCertPEM(t, "a"))
	caB := string(testCertPEM(t, "b"))
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: caA}})
	b := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: caB}})

	ex := testExchanger(a, b)
	resp, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{ClusterA: "cluster-a", ClusterB: "cluster-b"})
	if code != 400 || resp.Code != "KUBEVIRT_EXTERNAL_CA_NOT_FOUND" {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}

func TestExchangeConfigMapConflict(t *testing.T) {
	caA := string(testCertPEM(t, "a"))
	caB := string(testCertPEM(t, "b"))
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {
		kubevirtCAConfigMapName:         caA,
		kubevirtExternalCAConfigMapName: "",
	}})
	b := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {
		kubevirtCAConfigMapName: caB, kubevirtExternalCAConfigMapName: "",
	}})
	a.conflictNext = true

	ex := testExchanger(a, b)
	_, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{ClusterA: "cluster-a", ClusterB: "cluster-b"})
	if code != 409 {
		t.Fatalf("expected 409 conflict, got %d", code)
	}
}

func TestCertCountWarning46(t *testing.T) {
	var parts []string
	for i := 0; i < 46; i++ {
		parts = append(parts, string(testCertPEM(t, string(rune('a'+i%26)))))
	}
	big := strings.Join(parts, "")
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: big}})
	b := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: big}})

	ex := testExchanger(a, b)
	resp, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{ClusterA: "cluster-a", ClusterB: "cluster-b"})
	if code != 200 || len(resp.Warnings) == 0 {
		t.Fatalf("expected warning code=%d warnings=%v", code, resp.Warnings)
	}
}

func TestCertCount45NoWarning(t *testing.T) {
	var parts []string
	for i := 0; i < 45; i++ {
		parts = append(parts, string(testCertPEM(t, string(rune('a'+i%26)))))
	}
	big := strings.Join(parts, "")
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: big}})
	b := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {kubevirtCAConfigMapName: big}})

	ex := testExchanger(a, b)
	resp, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{ClusterA: "cluster-a", ClusterB: "cluster-b"})
	if code != 200 || len(resp.Warnings) != 0 {
		t.Fatalf("expected no warning code=%d warnings=%v", code, resp.Warnings)
	}
}

func TestExchangeVerifyTimeout(t *testing.T) {
	caA := string(testCertPEM(t, "a"))
	caB := string(testCertPEM(t, "b"))
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {
		kubevirtCAConfigMapName: caA, kubevirtExternalCAConfigMapName: "",
	}})
	b := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {
		kubevirtCAConfigMapName: caB, kubevirtExternalCAConfigMapName: "",
	}})
	// Disable auto-merge so verification never completes
	a.disableMerge = true
	b.disableMerge = true

	setTestVerifyTimeout(t, time.Second)
	ex := testExchanger(a, b)
	resp, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{
		ClusterA: "cluster-a", ClusterB: "cluster-b",
	})
	if code != 504 || resp.Code != "VERIFY_TIMEOUT" {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}

func TestExchangeRetryAfterPartial(t *testing.T) {
	caA := testCertPEM(t, "a")
	caB := testCertPEM(t, "b")
	trustA := string(append(caA, caB...))
	a := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {
		kubevirtCAConfigMapName: trustA, kubevirtExternalCAConfigMapName: "",
	}})
	b := newFakeSpoke(map[string]map[string]string{"openshift-cnv": {
		kubevirtCAConfigMapName: string(caB), kubevirtExternalCAConfigMapName: "",
	}})

	setTestVerifyTimeout(t, 3*time.Second)
	ex := testExchanger(a, b)
	_, _ = ex.Exchange(context.Background(), api.CACLMExchangeRequest{
		ClusterA: "cluster-a", ClusterB: "cluster-b",
	})
	resp, code := ex.Exchange(context.Background(), api.CACLMExchangeRequest{
		ClusterA: "cluster-a", ClusterB: "cluster-b",
	})
	if code != 200 {
		t.Fatalf("retry code=%d", code)
	}
	if resp.ClusterA.WritePerformed {
		t.Fatal("second call should be idempotent on A")
	}
}
