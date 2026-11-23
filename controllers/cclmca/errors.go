// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package cclmca

import (
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/stolostron/mtv-integrations/api"
)

func isSpokeRBACDenied(err error) bool {
	return apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
}

func spokeRBACDeniedMessage(clusterA, clusterB, failingCluster string, err error) string {
	return fmt.Sprintf(
		"cluster %q: spoke API access denied (%v). Confirm MTV provider secret %q in mtv-integrations "+
			"and ManagedServiceAccount RBAC on the spoke; callers still need kubevirt.io:admin UserPermission on %q and %q.",
		failingCluster, err, failingCluster+"-mtv", clusterA, clusterB,
	)
}

func clusterAccessError(clusterA, clusterB, failing string, err error) (*api.CACLMExchangeResponse, int) {
	if isSpokeRBACDenied(err) {
		return failResponse(errCodeForbidden, spokeRBACDeniedMessage(clusterA, clusterB, failing, err)), http.StatusForbidden
	}
	return clusterError(failing, err), http.StatusBadGateway
}
