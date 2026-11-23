// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migrationadvisor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

func TestCheckCallerAuthorizedForClusters_KubevirtAdminBothClusters(t *testing.T) {
	upSrv := newFakeUserPermissionServer(t, "kv-tok", map[string][]string{
		advisorRoleKubevirtAdmin(): {"cnv-1", "cnv-2"},
	})
	defer upSrv.Close()

	ok, err := CheckCallerAuthorizedForClusters(
		context.Background(), nil, &rest.Config{Host: upSrv.URL}, "kv-tok",
		[]string{"cnv-1", "cnv-2"},
	)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestCheckCallerAuthorizedForClusters_FleetWithoutKubevirtDenied(t *testing.T) {
	upSrv := newFakeUserPermissionServer(t, "fleet-tok", map[string][]string{
		advisorRoleVMFleetAdmin(): {hubClusterName},
	})
	defer upSrv.Close()

	ok, err := CheckCallerAuthorizedForClusters(
		context.Background(), nil, &rest.Config{Host: upSrv.URL}, "fleet-tok",
		[]string{"cnv-1", "cnv-2"},
	)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCheckCallerAuthorizedForClusters_KubevirtAdminMissingCluster(t *testing.T) {
	upSrv := newFakeUserPermissionServer(t, "kv-tok", map[string][]string{
		advisorRoleKubevirtAdmin(): {"cnv-1"},
	})
	defer upSrv.Close()

	ok, err := CheckCallerAuthorizedForClusters(
		context.Background(), nil, &rest.Config{Host: upSrv.URL}, "kv-tok",
		[]string{"cnv-1", "cnv-2"},
	)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCheckCallerAuthorizedForClusters_ManagedClusterAdminOnlyDenied(t *testing.T) {
	upSrv := newFakeUserPermissionServer(t, "mc-tok", map[string][]string{
		advisorRoleManagedClusterAdmin(): {"cnv-1", "cnv-2"},
	})
	defer upSrv.Close()

	ok, err := CheckCallerAuthorizedForClusters(
		context.Background(), nil, &rest.Config{Host: upSrv.URL}, "mc-tok",
		[]string{"cnv-1", "cnv-2"},
	)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCheckCallerAuthorizedForClusters_Denied(t *testing.T) {
	upSrv := newFakeUserPermissionServer(t, "none-tok", nil)
	defer upSrv.Close()

	ok, err := CheckCallerAuthorizedForClusters(
		context.Background(), nil, &rest.Config{Host: upSrv.URL}, "none-tok",
		[]string{"cnv-1", "cnv-2"},
	)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCheckCallerAuthorizedForClusters_EmptyClustersDenied(t *testing.T) {
	upSrv := newFakeUserPermissionServer(t, "kv-tok", map[string][]string{
		advisorRoleKubevirtAdmin(): {"cnv-1", "cnv-2"},
	})
	defer upSrv.Close()

	ok, err := CheckCallerAuthorizedForClusters(
		context.Background(), nil, &rest.Config{Host: upSrv.URL}, "kv-tok", nil,
	)
	require.NoError(t, err)
	assert.False(t, ok)
}
