// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package controllers

import (
	"bytes"
	"encoding/pem"
)

// mergeCACert returns the provider secret cacert that should be stored.
//
// The ManagedServiceAccount CA (source) is always kept in the bundle. Extra PEM
// certificates already present in existing (for example a customer custom CA)
// are preserved instead of being replaced. When existing is empty, not PEM, or
// already identical to source, behavior matches a straight copy of source.
func mergeCACert(existing, source []byte) []byte {
	if len(existing) == 0 {
		return source
	}
	if bytes.Equal(existing, source) {
		return existing
	}

	existingDERs := pemCertificateDERs(existing)
	sourceBlocks := pemCertificateBlocks(source)
	if len(sourceBlocks) == 0 || len(existingDERs) == 0 {
		return source
	}

	have := make(map[string]struct{}, len(existingDERs))
	for _, der := range existingDERs {
		have[string(der)] = struct{}{}
	}

	var missing []byte
	for _, block := range sourceBlocks {
		if _, ok := have[string(block.Bytes)]; ok {
			continue
		}
		missing = append(missing, pem.EncodeToMemory(block)...)
		have[string(block.Bytes)] = struct{}{}
	}
	if len(missing) == 0 {
		return existing
	}

	out := append([]byte{}, existing...)
	if !bytes.HasSuffix(out, []byte("\n")) {
		out = append(out, '\n')
	}
	return append(out, missing...)
}

func pemCertificateDERs(data []byte) [][]byte {
	blocks := pemCertificateBlocks(data)
	ders := make([][]byte, 0, len(blocks))
	for _, block := range blocks {
		ders = append(ders, block.Bytes)
	}
	return ders
}

func pemCertificateBlocks(data []byte) []*pem.Block {
	var blocks []*pem.Block
	rest := data
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		if block.Type != "CERTIFICATE" {
			continue
		}
		blocks = append(blocks, block)
	}
	return blocks
}
