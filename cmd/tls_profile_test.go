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
	"testing"

	apiconfigv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTLSConfigFromProfileSpec_groupsUnset(t *testing.T) {
	t.Parallel()

	spec := apiconfigv1.TLSProfileSpec{
		MinTLSVersion: apiconfigv1.VersionTLS12,
		Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
		Groups:        nil,
	}

	apply, unsupported := tlsConfigFromProfileSpec(spec)
	require.Empty(t, unsupported, "custom profile with only supported ciphers must not report unsupported entries")

	cfg := &tls.Config{}
	apply(cfg)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion, "profile min TLS version must be applied to tls.Config")
	assert.Nil(
		t,
		cfg.CurvePreferences,
		"omitted groups must leave CurvePreferences unset so Go keeps default group selection",
	)
}

func TestTLSConfigFromProfileSpec_groupsFilter(t *testing.T) {
	t.Parallel()

	spec := apiconfigv1.TLSProfileSpec{
		MinTLSVersion: apiconfigv1.VersionTLS13,
		Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
		Groups: []apiconfigv1.TLSGroup{
			apiconfigv1.TLSGroupX25519,
			apiconfigv1.TLSGroupSecP256r1,
		},
	}

	apply, unsupported := tlsConfigFromProfileSpec(spec)
	require.Empty(t, unsupported, "supported TLS groups must not appear in the unsupported list")

	cfg := &tls.Config{}
	apply(cfg)
	require.NotEmpty(t, cfg.CurvePreferences, "configured groups must restrict CurvePreferences")
	assert.Contains(t, cfg.CurvePreferences, tls.X25519, "X25519 group must be allowed when listed in the profile")
	assert.Contains(t, cfg.CurvePreferences, tls.CurveP256, "secp256r1 group must be allowed when listed in the profile")
}

func TestTLSConfigFromProfileSpec_intermediateProfileIncludesGroups(t *testing.T) {
	t.Parallel()

	profile := *apiconfigv1.TLSProfiles[apiconfigv1.TLSProfileIntermediateType]
	require.NotEmpty(t, profile.Groups, "named profiles should carry default groups from openshift/api")

	apply, _ := tlsConfigFromProfileSpec(profile)
	cfg := &tls.Config{}
	apply(cfg)
	assert.NotEmpty(
		t,
		cfg.CurvePreferences,
		"Intermediate profile groups must map to CurvePreferences on TLS servers and clients",
	)
}
