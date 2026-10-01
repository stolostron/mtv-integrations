/*
Copyright 2025 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/tls"

	apiconfigv1 "github.com/openshift/api/config/v1"
	tlspkg "github.com/openshift/controller-runtime-common/pkg/tls"
)

// tlsConfigFromProfileSpec builds a tls.Config applier from the hub APIServer TLS profile spec.
// When profile.Groups is empty, CurvePreferences are not set (Go default group selection).
func tlsConfigFromProfileSpec(profileSpec apiconfigv1.TLSProfileSpec) (func(*tls.Config), []string) {
	return tlspkg.NewTLSConfigFromProfile(profileSpec)
}
