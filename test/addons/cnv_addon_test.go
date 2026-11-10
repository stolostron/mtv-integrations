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

package addons_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"text/template"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

const (
	operatorPolicyName      = "kubevirt-hyperconverged-operator"
	operatorPolicyNamespace = "open-cluster-management-policies"
)

func repoRoot() string {
	_, testFile, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(testFile), "../.."))
}

func loadManifest(t *testing.T, path string, replacements map[string]string) map[string]interface{} {
	return loadManifestDocument(t, path, 0, replacements)
}

func loadManifestDocument(
	t *testing.T, path string, document int, replacements map[string]string,
) map[string]interface{} {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(repoRoot(), path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	manifestYAML := string(data)
	for old, new := range replacements {
		manifestYAML = strings.ReplaceAll(manifestYAML, old, new)
	}
	documents := strings.Split(manifestYAML, "\n---\n")
	if document >= len(documents) {
		t.Fatalf("%s has %d YAML documents, need document %d", path, len(documents), document)
	}

	manifest := make(map[string]interface{})
	if err := yaml.Unmarshal([]byte(documents[document]), &manifest); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	return manifest
}

func nestedMap(t *testing.T, object map[string]interface{}, fields ...string) map[string]interface{} {
	t.Helper()

	value, found, err := unstructured.NestedMap(object, fields...)
	if err != nil {
		t.Fatalf("read %s: %v", strings.Join(fields, "."), err)
	}
	if !found {
		t.Fatalf("missing %s", strings.Join(fields, "."))
	}

	return value
}

func nestedSlice(t *testing.T, object map[string]interface{}, fields ...string) []interface{} {
	t.Helper()

	value, found, err := unstructured.NestedSlice(object, fields...)
	if err != nil {
		t.Fatalf("read %s: %v", strings.Join(fields, "."), err)
	}
	if !found {
		t.Fatalf("missing %s", strings.Join(fields, "."))
	}

	return value
}

func TestCNVAddonConfigurationPolicyCreatesHCO(t *testing.T) {
	for _, tc := range []struct {
		name         string
		path         string
		replacements map[string]string
	}{
		{name: "standalone", path: "addons/cnv-addon/cnv-addon-template.yaml"},
		{
			name: "chart", path: "charts/templates/cnv-addon-template.yaml",
			replacements: map[string]string{
				"{{ `{{CLUSTER_NAME}}` }}":     "{{CLUSTER_NAME}}",
				"{{`{{HCO_TEMPLATE_START}}`}}": "{{HCO_TEMPLATE_START}}",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addon := loadManifest(t, tc.path, tc.replacements)
			manifests := nestedSlice(t, addon, "spec", "agentSpec", "workload", "manifests")
			operatorCount, configurationCount := 0, 0
			for _, raw := range manifests {
				manifest := raw.(map[string]interface{})
				switch manifest["kind"] {
				case "HyperConverged":
					t.Fatal("the addon ManifestWork must not include HCO directly")
				case "OperatorPolicy":
					operatorCount++
				case "Policy":
					t.Fatal("the addon ManifestWork must not include a hub Policy")
				case "ConfigurationPolicy":
					configurationCount++
					spec := nestedMap(t, manifest, "spec")
					if spec["remediationAction"] != "enforce" {
						t.Fatalf("ConfigurationPolicy must create HCO, got %v", spec["remediationAction"])
					}
					if _, ok := spec["object-templates-raw"].(string); !ok {
						t.Fatal("ConfigurationPolicy must include a raw HCO object template")
					}
				}
			}
			if operatorCount != 1 || configurationCount != 1 {
				t.Fatalf("expected one OperatorPolicy and one ConfigurationPolicy, got %d and %d",
					operatorCount, configurationCount)
			}
			configs := nestedSlice(t, addon, "spec", "agentSpec", "manifestConfigs")
			if len(configs) != 2 {
				t.Fatalf("expected OperatorPolicy and ConfigurationPolicy strategies, got %d", len(configs))
			}
			strategies := map[string]string{}
			for _, raw := range configs {
				config := raw.(map[string]interface{})
				id := nestedMap(t, config, "resourceIdentifier")
				strategy := nestedMap(t, config, "updateStrategy")
				strategies[id["resource"].(string)] = strategy["type"].(string)
			}
			if strategies["operatorpolicies"] != "CreateOnly" ||
				strategies["configurationpolicies"] != "CreateOnly" {
				t.Fatalf("unexpected manifest strategies: %#v", strategies)
			}
			deleteOption := nestedMap(t, addon, "spec", "agentSpec", "deleteOption")
			if deleteOption["propagationPolicy"] != "SelectivelyOrphan" {
				t.Fatalf("unexpected ManifestWork delete option: %#v", deleteOption)
			}
		})
	}
}

func TestCNVAddonConfigurationMatchesHelmChart(t *testing.T) {
	standalone := loadManifest(t, "addons/cnv-addon/cnv-addon-template.yaml", nil)
	chart := loadManifest(t, "charts/templates/cnv-addon-template.yaml", map[string]string{
		"{{ `{{CLUSTER_NAME}}` }}":     "{{CLUSTER_NAME}}",
		"{{`{{HCO_TEMPLATE_START}}`}}": "{{HCO_TEMPLATE_START}}",
	})
	if !reflect.DeepEqual(addonConfigurationPolicy(t, standalone), addonConfigurationPolicy(t, chart)) {
		t.Fatal("standalone and Helm CNV ConfigurationPolicies have diverged")
	}
}

func TestCNVAddonWorkAgentsCanCreatePolicies(t *testing.T) {
	for _, tc := range []struct {
		name         string
		path         string
		replacements map[string]string
	}{
		{name: "standalone", path: "addons/cnv-addon/cnv-addon-template.yaml"},
		{
			name: "chart", path: "charts/templates/cnv-addon-template.yaml",
			replacements: map[string]string{
				"{{ `{{CLUSTER_NAME}}` }}":     "{{CLUSTER_NAME}}",
				"{{`{{HCO_TEMPLATE_START}}`}}": "{{HCO_TEMPLATE_START}}",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addon := loadManifest(t, tc.path, tc.replacements)
			manifests := nestedSlice(t, addon, "spec", "agentSpec", "workload", "manifests")
			roleFound := false
			bindings := map[string]string{
				"operatorpolicy-manager-binding":        "klusterlet-work-sa",
				"operatorpolicy-manager-hosted-binding": "klusterlet-{{CLUSTER_NAME}}-work-sa",
			}
			for _, raw := range manifests {
				manifest := raw.(map[string]interface{})
				metadata := nestedMap(t, manifest, "metadata")
				switch manifest["kind"] {
				case "Role":
					if metadata["name"] != "operatorpolicy-manager" {
						continue
					}
					roleFound = true
					rules := nestedSlice(t, manifest, "rules")
					if len(rules) != 1 {
						t.Fatalf("expected one policy Role rule, got %#v", rules)
					}
					rule := rules[0].(map[string]interface{})
					if !reflect.DeepEqual(nestedSlice(t, rule, "resources"),
						[]interface{}{"operatorpolicies", "configurationpolicies"}) {
						t.Fatalf("work agents need both policy resources, got %#v", rule["resources"])
					}
				case "RoleBinding":
					name := metadata["name"].(string)
					wantSubject, ok := bindings[name]
					if !ok {
						continue
					}
					if nestedMap(t, manifest, "roleRef")["name"] != "operatorpolicy-manager" {
						t.Fatalf("%s must bind the policy Role", name)
					}
					subjects := nestedSlice(t, manifest, "subjects")
					if len(subjects) != 1 || subjects[0].(map[string]interface{})["name"] != wantSubject {
						t.Fatalf("%s has unexpected subjects: %#v", name, subjects)
					}
					delete(bindings, name)
				}
			}
			if !roleFound || len(bindings) != 0 {
				t.Fatalf("missing policy Role or work-agent bindings: role=%v, bindings=%#v", roleFound, bindings)
			}
		})
	}
}

func TestCNVAddonTemplateDelimiterConfig(t *testing.T) {
	standalone := loadManifestDocument(t, "addons/cnv-addon/cnv-clustermanagementaddon.yaml", 1, nil)
	data, err := os.ReadFile(filepath.Join(repoRoot(), "charts/templates/cnv-clustermanagementaddon.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	render := func(source string) string {
		t.Helper()
		parsed, err := template.New("chart").Parse(source)
		if err != nil {
			t.Fatalf("parse chart template: %v", err)
		}
		var output bytes.Buffer
		values := map[string]interface{}{
			"Values": map[string]interface{}{
				"global": map[string]interface{}{"namespace": "open-cluster-management-global-set"},
			},
		}
		if err := parsed.Execute(&output, values); err != nil {
			t.Fatalf("render chart template: %v", err)
		}
		return output.String()
	}

	// Chart automation renders the source chart and rewrites its YAML before
	// the operator renders the generated chart. The delimiter must survive both.
	documents := strings.Split(render(string(data)), "\n---\n")
	if len(documents) != 2 {
		t.Fatalf("expected two chart documents, got %d", len(documents))
	}
	chart := make(map[string]interface{})
	if err := yaml.Unmarshal([]byte(documents[1]), &chart); err != nil {
		t.Fatalf("parse generated config: %v", err)
	}
	generated, err := yaml.Marshal(chart)
	if err != nil {
		t.Fatalf("rewrite generated config: %v", err)
	}
	if err := yaml.Unmarshal([]byte(render(string(generated))), &chart); err != nil {
		t.Fatalf("parse operator-rendered config: %v", err)
	}
	if !reflect.DeepEqual(standalone, chart) {
		t.Fatal("standalone and Helm delimiter configurations have diverged")
	}
	variables := nestedSlice(t, standalone, "spec", "customizedVariables")
	if len(variables) != 1 || variables[0].(map[string]interface{})["value"] != "{{" {
		t.Fatalf("expected one literal opening delimiter, got %#v", variables)
	}
	addon := loadManifest(t, "addons/cnv-addon/cnv-clustermanagementaddon.yaml", nil)
	configs := nestedSlice(t, addon, "spec", "defaultConfigs")
	found := false
	for _, raw := range configs {
		config := raw.(map[string]interface{})
		if config["resource"] == "addondeploymentconfigs" &&
			config["name"] == "kubevirt-hyperconverged-template-values" {
			found = true
		}
	}
	if !found {
		t.Fatal("CNV addon must use the delimiter AddOnDeploymentConfig")
	}
}

func TestCNVConfigurationPolicyOperatorCompliance(t *testing.T) {
	raw := rawCNVPolicyTemplate(t)
	for _, tc := range []struct {
		name          string
		operatorReady bool
		wantObjects   int
	}{
		{name: "operator pending"},
		{name: "operator compliant", operatorReady: true, wantObjects: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := renderCNVPolicyTemplate(t, raw, tc.operatorReady)
			if len(objects) != tc.wantObjects {
				t.Fatalf("expected %d HCO templates, got %#v", tc.wantObjects, objects)
			}
			if tc.wantObjects == 0 {
				return
			}
			hco := nestedMap(t, objects[0], "objectDefinition")
			_, hasSpec, err := unstructured.NestedMap(hco, "spec")
			if err != nil || !hasSpec {
				t.Fatalf("unexpected HCO spec presence %v (error %v)", hasSpec, err)
			}
			gates := nestedSlice(t, hco, "spec", "featureGates")
			wantGates := map[string]string{
				"decentralizedLiveMigration": "Enabled",
				"downwardMetrics":            "Disabled",
				"disableMDevConfiguration":   "Disabled",
				"deployKubeSecondaryDNS":     "Disabled",
				"alignCPUs":                  "Disabled",
				"persistentReservation":      "Disabled",
			}
			for _, gate := range gates {
				entry := gate.(map[string]interface{})
				name := entry["name"].(string)
				if wantGates[name] != entry["state"] {
					t.Errorf("unexpected feature gate %s: %v", name, entry["state"])
				}
				delete(wantGates, name)
			}
			if len(wantGates) != 0 {
				t.Fatalf("missing HCO feature gates: %#v", wantGates)
			}
		})
	}
}

func TestCNVConfigurationPolicyUsesCustomizedHCO(t *testing.T) {
	raw := rawCNVPolicyTemplate(t)
	custom := strings.Replace(raw, "vmiCPUAllocationRatio: 10", "vmiCPUAllocationRatio: 12", 1)
	if custom == raw {
		t.Fatal("default HCO CPU allocation was not found")
	}
	objects := renderCNVPolicyTemplate(t, custom, true)
	if len(objects) != 1 {
		t.Fatalf("expected customized HCO, got %#v", objects)
	}
	hco := nestedMap(t, objects[0], "objectDefinition")
	virtualization := nestedMap(t, hco, "spec", "virtualization")
	if virtualization["vmiCPUAllocationRatio"] != float64(12) {
		t.Fatalf("custom CPU allocation was not used: %v", virtualization["vmiCPUAllocationRatio"])
	}
}

func TestCNVConfigurationPolicyPreservesExistingHCO(t *testing.T) {
	objects := renderCNVPolicyTemplate(t, rawCNVPolicyTemplate(t), true, true)
	if len(objects) != 1 {
		t.Fatalf("expected one HCO identity template, got %#v", objects)
	}
	hco := nestedMap(t, objects[0], "objectDefinition")
	metadata := nestedMap(t, hco, "metadata")
	if metadata["name"] != "kubevirt-hyperconverged" || metadata["namespace"] != "openshift-cnv" {
		t.Fatalf("unexpected existing HCO identity: %#v", metadata)
	}
	if _, found, err := unstructured.NestedMap(hco, "spec"); err != nil || found {
		t.Fatalf("existing HCO spec must not be enforced: %v", err)
	}
	if _, found, err := unstructured.NestedMap(hco, "metadata", "annotations"); err != nil || found {
		t.Fatalf("existing HCO annotations must not be enforced: %v", err)
	}
}

func rawCNVPolicyTemplate(t *testing.T) string {
	t.Helper()
	addon := loadManifest(t, "addons/cnv-addon/cnv-addon-template.yaml", nil)
	policy := addonConfigurationPolicy(t, addon)
	spec := nestedMap(t, policy, "spec")
	if nestedMap(t, spec, "evaluationInterval")["compliant"] != "1m" {
		t.Fatal("ConfigurationPolicy must reevaluate while waiting")
	}
	raw, ok := spec["object-templates-raw"].(string)
	if !ok {
		t.Fatal("ConfigurationPolicy has no raw HCO template")
	}
	return strings.ReplaceAll(raw, "{{HCO_TEMPLATE_START}}", "{{")
}

func renderCNVPolicyTemplate(
	t *testing.T, raw string, operatorReady bool, existingHCO ...bool,
) []map[string]interface{} {
	t.Helper()
	lookup := func(apiVersion, kind, namespace, name string) interface{} {
		switch kind {
		case "OperatorPolicy":
			if apiVersion != "policy.open-cluster-management.io/v1beta1" ||
				namespace != operatorPolicyNamespace || name != operatorPolicyName {
				t.Errorf("unexpected OperatorPolicy lookup: %s %s/%s", apiVersion, namespace, name)
			}
			if operatorReady {
				return map[string]interface{}{"status": map[string]interface{}{"compliant": "Compliant"}}
			}
			return ""
		case "HyperConverged":
			if apiVersion != "hco.kubevirt.io/v1" || namespace != "openshift-cnv" ||
				name != "kubevirt-hyperconverged" {
				t.Errorf("unexpected HCO lookup: %s %s/%s", apiVersion, namespace, name)
			}
			if len(existingHCO) != 0 && existingHCO[0] {
				return map[string]interface{}{
					"metadata": map[string]interface{}{"name": name},
					"spec": map[string]interface{}{
						"virtualization": map[string]interface{}{
							"liveMigrationConfig": map[string]interface{}{"allowPostCopy": true},
						},
					},
				}
			}
			return ""
		default:
			t.Errorf("unexpected lookup: %s %s %s/%s", apiVersion, kind, namespace, name)
			return ""
		}
	}
	parsed, err := template.New("hco").Funcs(template.FuncMap{"lookup": lookup}).Parse(raw)
	if err != nil {
		t.Fatalf("parse HCO policy template: %v", err)
	}
	var rendered bytes.Buffer
	if err := parsed.Execute(&rendered, nil); err != nil {
		t.Fatalf("render HCO policy template: %v", err)
	}
	var objects []map[string]interface{}
	if err := yaml.Unmarshal(rendered.Bytes(), &objects); err != nil {
		t.Fatalf("parse rendered HCO objects: %v\n%s", err, rendered.String())
	}
	return objects
}

func addonConfigurationPolicy(t *testing.T, addon map[string]interface{}) map[string]interface{} {
	t.Helper()
	for _, raw := range nestedSlice(t, addon, "spec", "agentSpec", "workload", "manifests") {
		manifest := raw.(map[string]interface{})
		if manifest["kind"] == "ConfigurationPolicy" {
			return manifest
		}
	}
	t.Fatal("AddOnTemplate is missing ConfigurationPolicy")
	return nil
}

func TestCNVCleanupScriptUsesDirectConfigurationPolicy(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(), "update_cleanup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, required := range []string{
		"kind: AddOnDeploymentConfig",
		"cnv-addon-template.yaml",
		"split(\"{{HCO_TEMPLATE_START}}\") | join(\"{{\")",
		"spec: $addon.spec.agentSpec | {manifestConfigs, workload, deleteOption}",
		"oc delete policy kubevirt-hyperconverged",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("cleanup script is missing %q", required)
		}
	}
}
