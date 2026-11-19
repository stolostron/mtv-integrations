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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func testCertPEM(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(48 * time.Hour),
		KeyUsage:     x509.KeyUsageCertSign,
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestRequireNonemptyCABundle(t *testing.T) {
	if err := requireNonemptyCABundle(nil); err == nil {
		t.Fatal("expected error for empty bundle")
	}
	if err := requireNonemptyCABundle([]byte("not-a-cert")); err == nil {
		t.Fatal("expected parse error")
	}
	ca := testCertPEM(t, "ok")
	if err := requireNonemptyCABundle(ca); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestBundleContainsAll(t *testing.T) {
	a := testCertPEM(t, "a")
	b := testCertPEM(t, "b")
	trust := append(a, b...)
	ok, err := bundleContainsAll(trust, b)
	if err != nil || !ok {
		t.Fatalf("expected trust, got %v err %v", ok, err)
	}
	ok, err = bundleContainsAll(a, b)
	if err != nil || ok {
		t.Fatalf("expected no trust, got %v", ok)
	}
}

func TestCertCountWarningThreshold(t *testing.T) {
	if CertCountWarningThreshold != 45 {
		t.Fatalf("expected 45, got %d", CertCountWarningThreshold)
	}
}

func TestPendingExternalCA(t *testing.T) {
	if !pendingExternalCABundle(" -----BEGIN") {
		t.Fatal("expected pending")
	}
	if pendingExternalCABundle("  \n  ") {
		t.Fatal("expected empty")
	}
}
