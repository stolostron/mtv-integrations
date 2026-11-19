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
		"cluster %q: access denied (%v). Grant kubevirt.io:admin in openshift-cnv on %q and %q (MRA) and label both acm/proxy-cclm=true.",
		failingCluster, err, clusterA, clusterB,
	)
}

func clusterAccessError(clusterA, clusterB, failing string, err error) (*api.CACLMExchangeResponse, int) {
	if isSpokeRBACDenied(err) {
		return failResponse(errCodeForbidden, spokeRBACDeniedMessage(clusterA, clusterB, failing, err)), http.StatusForbidden
	}
	return clusterError(failing, err), http.StatusBadGateway
}