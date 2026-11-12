// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package addons_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProxyCCLMAddonChartParity(t *testing.T) {
	replacements := map[string]string{
		"{{`{{KV_TEMPLATE_START}}`}}": "{{KV_TEMPLATE_START}}",
	}
	standalone := loadManifest(t, "addons/proxy-cclm-addon/proxy-cclm-addon-template.yaml", nil)
	chart := loadManifest(t, "charts/templates/proxy-cclm-addon-template.yaml", replacements)
	if !reflect.DeepEqual(standalone["spec"], chart["spec"]) {
		t.Fatal("proxy-cclm AddOnTemplate spec differs between addon and chart")
	}
}

func TestProxyCCLMPlacementUsesProxyLabel(t *testing.T) {
	for _, tc := range []struct {
		name         string
		path         string
		replacements map[string]string
	}{
		{name: "standalone", path: "addons/proxy-cclm-addon/global-placement.yaml"},
		{
			name: "chart", path: "charts/templates/proxy-cclm-global-placement.yaml",
			replacements: map[string]string{
				"{{ .Values.global.namespace }}": "open-cluster-management-global-set",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			placement := loadManifest(t, tc.path, tc.replacements)
			predicates := nestedSlice(t, placement, "spec", "predicates")
			var proxyExpr map[string]interface{}
			for _, raw := range predicates {
				predicate := raw.(map[string]interface{})
				selector := nestedMap(t, predicate, "requiredClusterSelector")
				labelSelector, ok := selector["labelSelector"].(map[string]interface{})
				if !ok {
					continue
				}
				exprs, ok := labelSelector["matchExpressions"].([]interface{})
				if !ok {
					continue
				}
				for _, e := range exprs {
					expr := e.(map[string]interface{})
					if expr["key"] == "acm/proxy-cclm" {
						proxyExpr = expr
					}
				}
			}
			if proxyExpr == nil || proxyExpr["operator"] != "In" {
				t.Fatalf("missing acm/proxy-cclm=In expression: %#v", proxyExpr)
			}
		})
	}
}

func TestProxyCCLMKubeVirtConfigurationPolicy(t *testing.T) {
	addon := loadManifest(t, "addons/proxy-cclm-addon/proxy-cclm-addon-template.yaml", nil)
	manifests := nestedSlice(t, addon, "spec", "agentSpec", "workload", "manifests")
	var raw string
	for _, m := range manifests {
		manifest := m.(map[string]interface{})
		if manifest["kind"] != "ConfigurationPolicy" {
			continue
		}
		spec := nestedMap(t, manifest, "spec")
		raw, _ = spec["object-templates-raw"].(string)
		break
	}
	if raw == "" {
		t.Fatal("ConfigurationPolicy object-templates-raw missing")
	}
	for _, fragment := range []string{
		`lookup "kubevirt.io/v1" "KubeVirt"`,
		"DecentralizedLiveMigration",
		"CrossClusterMigrationProxy",
		"uniq $gates",
		"decentralizedLiveMigrationDatapath: Proxy",
		"kubevirt-kubevirt-hyperconverged",
	} {
		if !strings.Contains(raw, fragment) {
			t.Fatalf("object-templates-raw missing %q", fragment)
		}
	}
}

func TestProxyCCLMSyncEndpointManifests(t *testing.T) {
	addon := loadManifest(t, "addons/proxy-cclm-addon/proxy-cclm-addon-template.yaml", nil)
	manifests := nestedSlice(t, addon, "spec", "agentSpec", "workload", "manifests")
	var sawService, sawRoute bool
	for _, m := range manifests {
		manifest := m.(map[string]interface{})
		kind, _ := manifest["kind"].(string)
		meta := nestedMap(t, manifest, "metadata")
		name, _ := meta["name"].(string)
		switch kind {
		case "Service":
			if name == "virt-synchronization-controller" {
				sawService = true
			}
		case "Route":
			if name == "virt-synchronization-endpoint" {
				sawRoute = true
			}
		}
	}
	if !sawService || !sawRoute {
		t.Fatalf("expected sync Service and Route manifests, service=%v route=%v", sawService, sawRoute)
	}
}

func TestProxyCCLMDeploymentConfigDelimiter(t *testing.T) {
	cma := loadManifestDocument(t, "addons/proxy-cclm-addon/proxy-cclm-clustermanagementaddon.yaml", 1, nil)
	vars := nestedSlice(t, cma, "spec", "customizedVariables")
	found := false
	for _, v := range vars {
		entry := v.(map[string]interface{})
		if entry["name"] == "KV_TEMPLATE_START" && entry["value"] == "{{" {
			found = true
		}
	}
	if !found {
		t.Fatal("AddOnDeploymentConfig must define KV_TEMPLATE_START")
	}

	data, err := os.ReadFile(filepath.Join(repoRoot(), "addons/proxy-cclm-addon/proxy-cclm-addon-template.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "{{KV_TEMPLATE_START}}") {
		t.Fatal("proxy-cclm AddOnTemplate must use KV_TEMPLATE_START placeholder")
	}
}
