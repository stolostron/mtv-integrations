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
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
)

const (
	pemCertificateType              = "CERTIFICATE"
	caBundleDataKey                 = "ca-bundle"
	kubevirtCAConfigMapName         = "kubevirt-ca"
	kubevirtExternalCAConfigMapName = "kubevirt-external-ca"
	defaultKubeVirtNamespace        = "openshift-cnv"
	// CertCountWarningThreshold warns when kubevirt-ca grows large (operator merge limit is higher).
	CertCountWarningThreshold = 45
)

// certFingerprint is SHA-256 over DER bytes (stable, order-independent).
func certFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func parseCertificates(pemBundle []byte) ([]*x509.Certificate, error) {
	rest := bytes.TrimSpace(pemBundle)
	if len(rest) == 0 {
		return nil, nil
	}
	var certs []*x509.Certificate
	for len(rest) > 0 {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		if block.Type != pemCertificateType {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 && len(bytes.TrimSpace(pemBundle)) > 0 {
		return nil, fmt.Errorf("no certificates found in PEM bundle")
	}
	return certs, nil
}

func fingerprintSet(pemBundle []byte) (map[string]struct{}, error) {
	certs, err := parseCertificates(pemBundle)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(certs))
	for _, c := range certs {
		set[certFingerprint(c)] = struct{}{}
	}
	return set, nil
}

// bundleContainsAll returns true when every certificate in required is present in trust (by DER).
func bundleContainsAll(trustPEM, requiredPEM []byte) (bool, error) {
	trust, err := fingerprintSet(trustPEM)
	if err != nil {
		return false, err
	}
	required, err := parseCertificates(requiredPEM)
	if err != nil {
		return false, err
	}
	for _, c := range required {
		if _, ok := trust[certFingerprint(c)]; !ok {
			return false, nil
		}
	}
	return true, nil
}

func certificateCount(pemBundle []byte) (int, error) {
	certs, err := parseCertificates(pemBundle)
	if err != nil {
		return 0, err
	}
	return len(certs), nil
}

// requireNonemptyCABundle rejects empty or certificate-less kubevirt-ca bundles.
func requireNonemptyCABundle(bundle []byte) error {
	n, err := certificateCount(bundle)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("kubevirt-ca bundle is empty")
	}
	return nil
}

func pendingExternalCABundle(data string) bool {
	return len(bytes.TrimSpace([]byte(data))) > 0
}

// mergeExpectedFingerprints returns fingerprints that must appear in the trust store after exchange.
func expectedFingerprintsFromSource(sourcePEM []byte) (map[string]struct{}, error) {
	return fingerprintSet(sourcePEM)
}

// fingerprintsPresent reports whether every expected fingerprint exists in trustPEM.
func fingerprintsPresent(trustPEM string, expected map[string]struct{}) (bool, error) {
	trust, err := fingerprintSet([]byte(trustPEM))
	if err != nil {
		return false, err
	}
	for fp := range expected {
		if _, ok := trust[fp]; !ok {
			return false, nil
		}
	}
	return true, nil
}
