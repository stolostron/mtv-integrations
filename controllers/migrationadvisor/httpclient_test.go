// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

// selfSignedCAPEM generates a minimal self-signed CA certificate in PEM form.
func selfSignedCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-ca"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeTempFile(t *testing.T, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "ca-*.pem")
	require.NoError(t, err)
	_, err = f.Write(data)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

// TestBuildHTTPClient_EmptyConfig verifies that an empty rest.Config with no
// serviceCAPath builds a client without error.
func TestBuildHTTPClient_EmptyConfig(t *testing.T) {
	c, err := buildHTTPClient(&rest.Config{}, "")
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// TestBuildHTTPClient_ValidCAData verifies that a valid PEM cert in CAData is
// accepted.
func TestBuildHTTPClient_ValidCAData(t *testing.T) {
	cfg := &rest.Config{}
	cfg.CAData = selfSignedCAPEM(t)
	c, err := buildHTTPClient(cfg, "")
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// TestBuildHTTPClient_InvalidCAData verifies that non-PEM content in CAData
// returns an error containing the expected message.
func TestBuildHTTPClient_InvalidCAData(t *testing.T) {
	cfg := &rest.Config{}
	cfg.CAData = []byte("not-a-pem")
	_, err := buildHTTPClient(cfg, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no certs found in restConfig.CAData")
}

// TestBuildHTTPClient_ValidCAFile verifies that a valid PEM cert file path in
// CAFile is accepted.
func TestBuildHTTPClient_ValidCAFile(t *testing.T) {
	path := writeTempFile(t, selfSignedCAPEM(t))
	cfg := &rest.Config{}
	cfg.CAFile = path
	c, err := buildHTTPClient(cfg, "")
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// TestBuildHTTPClient_CAFileMissing verifies that a non-existent CAFile path
// returns an error containing the expected message.
func TestBuildHTTPClient_CAFileMissing(t *testing.T) {
	cfg := &rest.Config{}
	cfg.CAFile = "/nonexistent/ca.pem"
	_, err := buildHTTPClient(cfg, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read cluster CA file")
}

// TestBuildHTTPClient_InvalidCAFile verifies that a CAFile containing
// non-PEM content returns an error containing the expected message.
func TestBuildHTTPClient_InvalidCAFile(t *testing.T) {
	path := writeTempFile(t, []byte("not-a-pem"))
	cfg := &rest.Config{}
	cfg.CAFile = path
	_, err := buildHTTPClient(cfg, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no certs found in cluster CA file")
}

// TestBuildHTTPClient_ServiceCANotExist verifies that a non-existent
// serviceCAPath is silently ignored (not a fatal error).
func TestBuildHTTPClient_ServiceCANotExist(t *testing.T) {
	c, err := buildHTTPClient(&rest.Config{}, "/nonexistent/service-ca.crt")
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// TestBuildHTTPClient_ServiceCAInvalidPEM verifies that a serviceCAPath
// pointing to a file with non-PEM content returns an error.
func TestBuildHTTPClient_ServiceCAInvalidPEM(t *testing.T) {
	path := writeTempFile(t, []byte("not-a-pem"))
	_, err := buildHTTPClient(&rest.Config{}, path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no certs found in service CA bundle")
}

// TestBuildHTTPClient_ValidServiceCA verifies that a valid PEM service CA file
// is accepted.
func TestBuildHTTPClient_ValidServiceCA(t *testing.T) {
	path := writeTempFile(t, selfSignedCAPEM(t))
	c, err := buildHTTPClient(&rest.Config{}, path)
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// TestBuildHTTPClient_ServiceCAReadError verifies that a serviceCAPath that
// exists but cannot be read as a regular file (EISDIR — a directory) returns
// an error rather than silently continuing, because the failure is NOT an
// os.IsNotExist error and must be treated as fatal.
func TestBuildHTTPClient_ServiceCAReadError(t *testing.T) {
	dir := t.TempDir() // os.ReadFile on a directory returns syscall.EISDIR
	_, err := buildHTTPClient(&rest.Config{}, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read service CA")
}
