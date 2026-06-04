// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNodeMetrics_AvailableCPUCores(t *testing.T) {
	tests := []struct {
		name      string
		alloc     float64
		requested float64
		want      float64
	}{
		{"positive headroom", 16, 4, 12},
		{"zero headroom", 4, 4, 0},
		{"over-requested", 4, 6, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := NodeMetrics{
				AllocatableCPUCores: tt.alloc,
				RequestedCPUCores:   tt.requested,
			}
			assert.InDelta(t, tt.want, n.AvailableCPUCores(), 0.001)
		})
	}
}

func TestNodeMetrics_AvailableMemBytes(t *testing.T) {
	tests := []struct {
		name      string
		alloc     int64
		requested int64
		want      int64
	}{
		{"positive headroom", 32 * (1 << 30), 8 * (1 << 30), 24 * (1 << 30)},
		{"zero headroom", 4 * (1 << 30), 4 * (1 << 30), 0},
		{"over-requested", 4 * (1 << 30), 6 * (1 << 30), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := NodeMetrics{
				AllocatableMemBytes: tt.alloc,
				RequestedMemBytes:   tt.requested,
			}
			assert.Equal(t, tt.want, n.AvailableMemBytes())
		})
	}
}