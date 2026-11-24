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
	"context"

	"github.com/stolostron/mtv-integrations/controllers/migrationadvisor"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func checkCallerAuthorizedForExchange(
	ctx context.Context,
	controllerClient dynamic.Interface,
	baseConfig *rest.Config,
	bearerToken string,
	clusterA, clusterB string,
) (bool, error) {
	return migrationadvisor.CheckCallerAuthorizedForClusters(
		ctx, controllerClient, baseConfig, bearerToken, []string{clusterA, clusterB},
	)
}
