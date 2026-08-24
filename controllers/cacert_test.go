// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package controllers

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestMergeCACert(t *testing.T) {
	clusterCA := testCertPEM(t, 1)
	customCA := testCertPEM(t, 2)
	rotatedCA := testCertPEM(t, 3)
	bundle := concatPEM(customCA, clusterCA)

	t.Run("empty existing uses source", func(t *testing.T) {
		assert.Equal(t, clusterCA, mergeCACert(nil, clusterCA))
		assert.Equal(t, clusterCA, mergeCACert([]byte{}, clusterCA))
	})

	t.Run("identical bytes are unchanged", func(t *testing.T) {
		got := mergeCACert(clusterCA, clusterCA)
		assert.Equal(t, clusterCA, got)
	})

	t.Run("opaque non-PEM is replaced with source", func(t *testing.T) {
		assert.Equal(t, []byte("cert2"), mergeCACert([]byte("cert1"), []byte("cert2")))
	})

	t.Run("custom extra PEMs are preserved", func(t *testing.T) {
		got := mergeCACert(bundle, clusterCA)
		assert.Equal(t, bundle, got)
		assert.True(t, caBundleHasCert(got, customCA))
		assert.True(t, caBundleHasCert(got, clusterCA))
	})

	t.Run("missing MSA CA is appended to custom CA", func(t *testing.T) {
		got := mergeCACert(customCA, clusterCA)
		assert.True(t, caBundleHasCert(got, customCA))
		assert.True(t, caBundleHasCert(got, clusterCA))
		assert.True(t, bytes.HasPrefix(got, customCA))
	})

	t.Run("rotated MSA CA is appended without dropping extras", func(t *testing.T) {
		got := mergeCACert(bundle, rotatedCA)
		assert.True(t, caBundleHasCert(got, customCA))
		assert.True(t, caBundleHasCert(got, clusterCA))
		assert.True(t, caBundleHasCert(got, rotatedCA))
	})

	t.Run("merged bundle is accepted by a TLS cert pool", func(t *testing.T) {
		got := mergeCACert(customCA, clusterCA)
		pool := x509.NewCertPool()
		assert.True(t, pool.AppendCertsFromPEM(got), "Forklift uses AppendCertsFromPEM on cacert")
	})

	t.Run("trailing junk and invalid PEM blocks are stripped", func(t *testing.T) {
		dirty := concatPEM(customCA, clusterCA)
		dirty = append(dirty, []byte("-----BEGIN CERTIFICATE-----\ntest==\n-----END CERTIFICATE-----\n3")...)
		got := mergeCACert(dirty, clusterCA)
		assert.False(t, bytes.Contains(got, []byte("test==")))
		_, clean := tlsCertsFromPEM(got)
		assert.True(t, clean, "merged cacert must be only TLS-usable PEM certs")
		assert.True(t, caBundleHasCert(got, customCA))
		assert.True(t, caBundleHasCert(got, clusterCA))
		pool := x509.NewCertPool()
		assert.True(t, pool.AppendCertsFromPEM(got))
		have, _ := tlsCertsFromPEM(got)
		assert.Equal(t, 2, len(have))
	})

	t.Run("PEM that is not x509 is not preserved", func(t *testing.T) {
		invalid := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not-a-cert")})
		got := mergeCACert(concatPEM(customCA, invalid), clusterCA)
		assert.True(t, caBundleHasCert(got, customCA))
		assert.True(t, caBundleHasCert(got, clusterCA))
		assert.False(t, bytes.Contains(got, []byte("not-a-cert")))
	})

	t.Run("only non-x509 PEM is replaced with source", func(t *testing.T) {
		invalid := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not-a-cert")})
		assert.Equal(t, clusterCA, mergeCACert(invalid, clusterCA))
	})

	t.Run("non-certificate PEM blocks are stripped", func(t *testing.T) {
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")})
		got := mergeCACert(concatPEM(customCA, clusterCA, keyPEM), clusterCA)
		assert.False(t, bytes.Contains(got, []byte("PRIVATE KEY")))
		assert.True(t, caBundleHasCert(got, customCA))
		assert.True(t, caBundleHasCert(got, clusterCA))
	})
}

func TestSecretNeedsUpdate_CAMerge(t *testing.T) {
	reconciler := &ManagedClusterReconciler{}
	clusterCA := testCertPEM(t, 1)
	customCA := testCertPEM(t, 2)
	bundle := concatPEM(customCA, clusterCA)

	t.Run("custom extra CA is not drift when token matches", func(t *testing.T) {
		provider := secretWithData("cacert", bundle, []byte("tok"))
		source := secretWithData("ca.crt", clusterCA, []byte("tok"))
		assert.False(t, reconciler.secretNeedsUpdate(provider, source))
	})

	t.Run("token rotation still updates when custom CA is present", func(t *testing.T) {
		provider := secretWithData("cacert", bundle, []byte("old"))
		source := secretWithData("ca.crt", clusterCA, []byte("new"))
		assert.True(t, reconciler.secretNeedsUpdate(provider, source))
	})

	t.Run("default opaque mismatch still updates", func(t *testing.T) {
		provider := secretWithData("cacert", []byte("cert1"), []byte("tok"))
		source := secretWithData("ca.crt", []byte("cert2"), []byte("tok"))
		assert.True(t, reconciler.secretNeedsUpdate(provider, source))
	})

	t.Run("trailing junk is drift even when custom CA is present", func(t *testing.T) {
		dirty := append(concatPEM(customCA, clusterCA), []byte("3")...)
		provider := secretWithData("cacert", dirty, []byte("tok"))
		source := secretWithData("ca.crt", clusterCA, []byte("tok"))
		assert.True(t, reconciler.secretNeedsUpdate(provider, source))
	})
}

func testCertPEM(t *testing.T, serial int64) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func concatPEM(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

func caBundleHasCert(bundle, certPEM []byte) bool {
	want, _ := tlsCertsFromPEM(certPEM)
	if len(want) != 1 {
		return false
	}
	have, _ := tlsCertsFromPEM(bundle)
	for _, cert := range have {
		if bytes.Equal(cert.Raw, want[0].Raw) {
			return true
		}
	}
	return false
}

func secretWithData(caKey string, ca, token []byte) *corev1.Secret {
	return &corev1.Secret{Data: map[string][]byte{caKey: ca, "token": token}}
}
