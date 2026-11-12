// Copyright (c) 2026 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package addons_test

import (
	"reflect"
	"testing"
)

func TestProxyCCLMAddonChartParity(t *testing.T) {
	standalone := loadManifest(t, "addons/proxy-cclm-addon/proxy-cclm-addon-template.yaml", nil)
	chart := loadManifest(t, "charts/templates/proxy-cclm-addon-template.yaml", nil)
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

func TestProxyCCLMConfigurationPolicyCreateOnly(t *testing.T) {
	addon := loadManifest(t, "addons/proxy-cclm-addon/proxy-cclm-addon-template.yaml", nil)
	configs := nestedSlice(t, addon, "spec", "agentSpec", "manifestConfigs")
	want := []map[string]interface{}{
		{
			"group":     "policy.open-cluster-management.io",
			"resource":  "configurationpolicies",
			"name":      "proxy-cclm-kubevirt-config",
			"namespace": "open-cluster-management-policies",
		},
		{
			"group":     "",
			"resource":  "services",
			"name":      "virt-synchronization-controller",
			"namespace": "openshift-cnv",
		},
		{
			"group":     "route.openshift.io",
			"resource":  "routes",
			"name":      "virt-synchronization-endpoint",
			"namespace": "openshift-cnv",
		},
	}
	if len(configs) != len(want) {
		t.Fatalf("expected %d manifestConfigs, got %d", len(want), len(configs))
	}
	for i, w := range want {
		cfg := configs[i].(map[string]interface{})
		id := nestedMap(t, cfg, "resourceIdentifier")
		for k, v := range w {
			if id[k] != v {
				t.Fatalf("manifestConfig[%d] resourceIdentifier[%s]: got %#v want %#v", i, k, id[k], v)
			}
		}
		strategy := nestedMap(t, cfg, "updateStrategy")
		if strategy["type"] != "CreateOnly" {
			t.Fatalf("manifestConfig[%d]: expected CreateOnly update strategy, got %#v", i, strategy)
		}
	}
}

func TestProxyCCLMPolicyManagerRBAC(t *testing.T) {
	addon := loadManifest(t, "addons/proxy-cclm-addon/proxy-cclm-addon-template.yaml", nil)
	manifests := nestedSlice(t, addon, "spec", "agentSpec", "workload", "manifests")
	if len(manifests) < 4 {
		t.Fatalf("expected Role, RoleBindings, and policy manifests, got %d", len(manifests))
	}
	role := manifests[0].(map[string]interface{})
	if role["kind"] != "Role" {
		t.Fatalf("first workload manifest should be Role, got %v", role["kind"])
	}
	roleMeta := nestedMap(t, role, "metadata")
	if roleMeta["name"] != "proxy-cclm-policy-manager" || roleMeta["namespace"] != "open-cluster-management-policies" {
		t.Fatalf("unexpected Role metadata: %#v", roleMeta)
	}
	rules := nestedSlice(t, role, "rules")
	if len(rules) != 1 {
		t.Fatalf("expected one Role rule, got %d", len(rules))
	}
	rule := rules[0].(map[string]interface{})
	resources, _ := rule["resources"].([]interface{})
	if len(resources) != 1 || resources[0] != "configurationpolicies" {
		t.Fatalf("proxy-cclm Role should scope configurationpolicies only: %#v", resources)
	}
	for i := 1; i <= 2; i++ {
		rb := manifests[i].(map[string]interface{})
		if rb["kind"] != "RoleBinding" {
			t.Fatalf("workload manifest[%d] should be RoleBinding, got %v", i, rb["kind"])
		}
	}
	policy := manifests[3].(map[string]interface{})
	if policy["kind"] != "ConfigurationPolicy" {
		t.Fatalf("fourth workload manifest should be ConfigurationPolicy, got %v", policy["kind"])
	}
}

func TestProxyCCLMKubeVirtConfigurationPolicy(t *testing.T) {
	addon := loadManifest(t, "addons/proxy-cclm-addon/proxy-cclm-addon-template.yaml", nil)
	manifests := nestedSlice(t, addon, "spec", "agentSpec", "workload", "manifests")
	var policySpec map[string]interface{}
	for _, m := range manifests {
		manifest := m.(map[string]interface{})
		if manifest["kind"] == "ConfigurationPolicy" {
			policySpec = nestedMap(t, manifest, "spec")
			break
		}
	}
	if policySpec == nil {
		t.Fatal("ConfigurationPolicy manifest missing")
	}
	templates := nestedSlice(t, policySpec, "object-templates")
	if len(templates) != 1 {
		t.Fatalf("expected one object-template, got %d", len(templates))
	}
	tpl := templates[0].(map[string]interface{})
	if tpl["complianceType"] != "musthave" {
		t.Fatalf("unexpected complianceType: %v", tpl["complianceType"])
	}
	def := nestedMap(t, tpl, "objectDefinition")
	gates := nestedSlice(t, def, "spec", "configuration", "developerConfiguration", "featureGates")
	wantGates := []interface{}{"CrossClusterMigrationProxy"}
	if !reflect.DeepEqual(gates, wantGates) {
		t.Fatalf("unexpected featureGates: %#v", gates)
	}
	migrations := nestedMap(t, def, "spec", "configuration", "migrations")
	if migrations["decentralizedLiveMigrationDatapath"] != "Proxy" {
		t.Fatalf("unexpected datapath: %#v", migrations["decentralizedLiveMigrationDatapath"])
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
