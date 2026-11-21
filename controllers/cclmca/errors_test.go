// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package cclmca

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stolostron/mtv-integrations/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestClusterAccessErrorSpokeRBACForbidden(t *testing.T) {
	err := apierrors.NewForbidden(
		schema.GroupResource{Group: "", Resource: "configmaps"},
		"kubevirt-external-ca",
		errors.New("disallowed"),
	)
	resp, code := clusterAccessError("cnv-1", "cnv-2", "cnv-1", err)
	require.Equal(t, http.StatusForbidden, code)
	require.Equal(t, errCodeForbidden, resp.Code)
	assert.Contains(t, resp.Message, "kubevirt.io:admin")
	assert.Contains(t, resp.Message, "cnv-1")
	assert.Contains(t, resp.Message, "cnv-2")
	assert.Contains(t, resp.Message, "acm/proxy-cclm=true")
}

func TestClusterAccessErrorOtherUsesUnreachable(t *testing.T) {
	err := errors.New("connection reset")
	resp, code := clusterAccessError("cnv-1", "cnv-2", "cnv-1", err)
	require.Equal(t, http.StatusBadGateway, code)
	assert.Equal(t, "CLUSTER_UNREACHABLE", resp.Code)
}

func TestMergeWriteErrorResponseSpokeRBAC(t *testing.T) {
	base := &api.CACLMExchangeResponse{
		ClusterA: &api.CACLMExchangeClusterStatus{Cluster: "cnv-1", WritePerformed: true},
		ClusterB: &api.CACLMExchangeClusterStatus{Cluster: "cnv-2"},
	}
	err := apierrors.NewForbidden(
		schema.GroupResource{Group: "", Resource: "configmaps"},
		"kubevirt-external-ca",
		errors.New("no patch"),
	)
	resp, code := mergeWriteErrorResponse(base, "cnv-2", err)
	require.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, errCodeForbidden, resp.Code)
	assert.True(t, resp.ClusterA.WritePerformed)
}