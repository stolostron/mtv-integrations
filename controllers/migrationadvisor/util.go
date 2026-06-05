// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"fmt"
	"strconv"
	"strings"
)

// parseQuantityToBytes converts Kubernetes quantity strings (e.g. "4Gi", "500Mi", "2")
// to int64 bytes (or cores for CPU — callers handle units).
func parseQuantityToBytes(q string) (int64, error) {
	if q == "" {
		return 0, nil
	}

	suffixes := []struct {
		suffix     string
		multiplier int64
	}{
		{"Ti", 1 << 40},
		{"Gi", 1 << 30},
		{"Mi", 1 << 20},
		{"Ki", 1 << 10},
		{"T", 1e12},
		{"G", 1e9},
		{"M", 1e6},
		{"K", 1e3},
	}

	upper := strings.TrimSpace(q)
	for _, s := range suffixes {
		if strings.HasSuffix(upper, s.suffix) {
			num := strings.TrimSuffix(upper, s.suffix)
			val, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			if err != nil {
				return 0, fmt.Errorf("parse quantity %q: %w", q, err)
			}
			return int64(val * float64(s.multiplier)), nil
		}
	}

	// Plain integer (bytes or millicores etc.)
	val, err := strconv.ParseFloat(strings.TrimSpace(upper), 64)
	if err != nil {
		return 0, fmt.Errorf("parse quantity %q: %w", q, err)
	}
	return int64(val), nil
}

// parseCPUCores converts a Kubernetes CPU quantity string to a float64 number of cores.
// Supports "500m" (millicores) and plain integer/decimal strings.
func parseCPUCores(q string) (float64, error) {
	if q == "" {
		return 0, nil
	}
	q = strings.TrimSpace(q)
	if strings.HasSuffix(q, "m") {
		m, err := strconv.ParseFloat(strings.TrimSuffix(q, "m"), 64)
		if err != nil {
			return 0, fmt.Errorf("parse CPU quantity %q: %w", q, err)
		}
		return m / 1000.0, nil
	}
	return strconv.ParseFloat(q, 64)
}
