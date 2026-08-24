// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package controllers

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
)

// mergeCACert returns the provider secret cacert that should be stored.
//
// Certificates are accepted with the same rules as crypto/x509.CertPool.AppendCertsFromPEM
// (what Forklift/TLS uses): PEM CERTIFICATE blocks with no headers whose DER parses as
// x509. Extra custom CAs that pass that check are preserved. Junk (trailing non-PEM,
// invalid PEM, non-certificate blocks, and PEM that is not x509) is stripped. When
// existing has no TLS-usable certificate, behavior matches a straight copy of source.
func mergeCACert(existing, source []byte) []byte {
	if len(existing) == 0 {
		return source
	}
	if bytes.Equal(existing, source) {
		return existing
	}

	existingCerts, existingClean := tlsCertsFromPEM(existing)
	sourceCerts, _ := tlsCertsFromPEM(source)
	if len(sourceCerts) == 0 || len(existingCerts) == 0 {
		return source
	}

	have := make(map[string]struct{}, len(existingCerts))
	for _, cert := range existingCerts {
		have[string(cert.Raw)] = struct{}{}
	}

	missing := make([]*x509.Certificate, 0, len(sourceCerts))
	for _, cert := range sourceCerts {
		if _, ok := have[string(cert.Raw)]; ok {
			continue
		}
		missing = append(missing, cert)
		have[string(cert.Raw)] = struct{}{}
	}

	if existingClean && len(missing) == 0 {
		return existing
	}
	if existingClean {
		out := append([]byte{}, existing...)
		if !bytes.HasSuffix(out, []byte("\n")) {
			out = append(out, '\n')
		}
		return append(out, encodeTLSCerts(missing)...)
	}

	out := encodeTLSCerts(existingCerts)
	if len(missing) > 0 {
		out = append(out, encodeTLSCerts(missing)...)
	}
	return out
}

// tlsCertsFromPEM returns certificates AppendCertsFromPEM would add, and whether
// data contained only those certificates (no leftover junk or skipped blocks).
func tlsCertsFromPEM(data []byte) ([]*x509.Certificate, bool) {
	rest := bytes.TrimLeft(data, " \t\n\r")
	if len(rest) == 0 {
		return nil, false
	}

	clean := true
	var certs []*x509.Certificate
	for len(rest) > 0 {
		begin := bytes.Index(rest, []byte("-----BEGIN "))
		if begin < 0 {
			if len(bytes.TrimSpace(rest)) != 0 {
				clean = false
			}
			break
		}
		if begin > 0 {
			if len(bytes.TrimSpace(rest[:begin])) != 0 {
				clean = false
			}
			rest = rest[begin:]
		}

		block, next := pem.Decode(rest)
		if block == nil {
			clean = false
			break
		}
		rest = next

		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			clean = false
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			clean = false
			continue
		}
		certs = append(certs, cert)
	}
	return certs, clean
}

func encodeTLSCerts(certs []*x509.Certificate) []byte {
	var out []byte
	seen := make(map[string]struct{}, len(certs))
	for _, cert := range certs {
		key := string(cert.Raw)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})...)
	}
	return out
}
