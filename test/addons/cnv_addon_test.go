package addons_test

import (
	"bytes"
	"encoding/json"
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
	hubPolicyName           = "kubevirt-hyperconverged"
	hubPolicyNamespace      = "open-cluster-management-global-set"
	cnvPlacementName        = "openshift-cnv"
)

func repoRoot() string {
	_, testFile, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(testFile), "../.."))
}

func loadManifest(t *testing.T, path string, replacements map[string]string) map[string]interface{} {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(repoRoot(), path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	manifestYAML := string(data)
	for old, new := range replacements {
		manifestYAML = strings.ReplaceAll(manifestYAML, old, new)
	}

	manifest := make(map[string]interface{})
	if err := yaml.Unmarshal([]byte(manifestYAML), &manifest); err != nil {
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

func TestCNVAddonTemplatesUseReadOnlyHyperConvergedHandoff(t *testing.T) {
	templates := []struct {
		name         string
		path         string
		replacements map[string]string
	}{
		{
			name: "standalone addon",
			path: "addons/cnv-addon/cnv-addon-template.yaml",
		},
		{
			name: "Helm chart",
			path: "charts/templates/cnv-addon-template.yaml",
			replacements: map[string]string{
				"{{ `{{CLUSTER_NAME}}` }}": "{{CLUSTER_NAME}}",
			},
		},
	}

	for _, template := range templates {
		t.Run(template.name, func(t *testing.T) {
			addon := loadManifest(t, template.path, template.replacements)
			manifests := nestedSlice(t, addon, "spec", "agentSpec", "workload", "manifests")
			operatorPolicies := 0
			configurationPolicies := 0
			hyperConvergedManifests := 0
			for _, rawManifest := range manifests {
				manifest, ok := rawManifest.(map[string]interface{})
				if !ok {
					t.Fatalf("manifest has unexpected type %T", rawManifest)
				}
				if manifest["kind"] == "HyperConverged" {
					hyperConvergedManifests++
					metadata := nestedMap(t, manifest, "metadata")
					if manifest["apiVersion"] != "hco.kubevirt.io/v1" || metadata["name"] != "kubevirt-hyperconverged" || metadata["namespace"] != "openshift-cnv" {
						t.Fatalf("unexpected HyperConverged handoff identity: %#v", manifest)
					}
					if _, found, err := unstructured.NestedMap(manifest, "spec"); err != nil || found {
						t.Fatalf("HyperConverged handoff manifest must not configure the CR, got spec %#v (error %v)", manifest["spec"], err)
					}
				}
				if manifest["kind"] == "OperatorPolicy" {
					operatorPolicies++
				}
				if manifest["kind"] == "ConfigurationPolicy" {
					configurationPolicies++
					metadata := nestedMap(t, manifest, "metadata")
					if metadata["name"] != "kubevirt-hyperconverged-config" || metadata["namespace"] != operatorPolicyNamespace {
						t.Fatalf("unexpected addon ConfigurationPolicy identity: %#v", metadata)
					}
					spec := nestedMap(t, manifest, "spec")
					if spec["remediationAction"] != "inform" {
						t.Fatalf("addon ConfigurationPolicy must not enforce HyperConverged: %#v", spec)
					}
					objects := nestedSlice(t, spec, "object-templates")
					if len(objects) != 1 {
						t.Fatalf("expected one customizable HyperConverged object, got %d", len(objects))
					}
				}
			}
			if operatorPolicies != 1 {
				t.Fatalf("expected exactly one OperatorPolicy in the addon ManifestWork, got %d", operatorPolicies)
			}
			if configurationPolicies != 1 {
				t.Fatalf("expected one customizable ConfigurationPolicy in the addon ManifestWork, got %d", configurationPolicies)
			}
			if hyperConvergedManifests != 1 {
				t.Fatalf("expected one identity-only HyperConverged handoff manifest, got %d", hyperConvergedManifests)
			}
			assertSelectiveOrphanRule(t, addon, "spec", "agentSpec", "deleteOption")

			manifestConfigs := nestedSlice(t, addon, "spec", "agentSpec", "manifestConfigs")
			readOnlyHCO := false
			createOnlyOperatorPolicy := false
			for _, rawConfig := range manifestConfigs {
				config, ok := rawConfig.(map[string]interface{})
				if !ok {
					t.Fatalf("manifest config has unexpected type %T", rawConfig)
				}
				identifier := nestedMap(t, config, "resourceIdentifier")
				strategy := nestedMap(t, config, "updateStrategy")
				switch identifier["group"] {
				case "hco.kubevirt.io":
					readOnlyHCO = identifier["resource"] == "hyperconvergeds" && identifier["name"] == "kubevirt-hyperconverged" && identifier["namespace"] == "openshift-cnv" && strategy["type"] == "ReadOnly"
				case "policy.open-cluster-management.io":
					createOnlyOperatorPolicy = identifier["resource"] == "operatorpolicies" && identifier["name"] == "kubevirt-hyperconverged-operator" && strategy["type"] == "CreateOnly"
				}
			}
			if !readOnlyHCO || !createOnlyOperatorPolicy {
				t.Fatalf("expected ReadOnly HyperConverged handoff and CreateOnly OperatorPolicy configs, got %#v", manifestConfigs)
			}
		})
	}
}

func TestCNVAddonConfigurationMatchesHelmChart(t *testing.T) {
	standalone := loadManifest(t, "addons/cnv-addon/cnv-addon-template.yaml", nil)
	chart := loadManifest(t, "charts/templates/cnv-addon-template.yaml", map[string]string{
		"{{ `{{CLUSTER_NAME}}` }}": "{{CLUSTER_NAME}}",
	})
	if !reflect.DeepEqual(addonConfigurationPolicy(t, standalone), addonConfigurationPolicy(t, chart)) {
		t.Fatal("the standalone and Helm CNV addon configurations have diverged")
	}
}

func TestCNVHubPolicyWaitsForOperatorPolicyAndPreservesCreateOnly(t *testing.T) {
	staticPolicyPath := "addons/cnv-addon/cnv-hyperconverged-policy.yaml"
	chartPolicyPath := "charts/templates/cnv-hyperconverged-policy.yaml"
	staticPolicy := loadManifest(t, staticPolicyPath, nil)
	chartPolicy := loadManifest(t, chartPolicyPath, map[string]string{
		"{{ .Values.global.namespace }}": hubPolicyNamespace,
		"{{`{{":                          "{{",
		"}}`}}":                          "}}",
	})

	if !reflect.DeepEqual(staticPolicy, chartPolicy) {
		t.Fatal("the standalone and Helm CNV hub Policies have diverged")
	}
	if staticPolicy["kind"] != "Policy" || staticPolicy["apiVersion"] != "policy.open-cluster-management.io/v1" {
		t.Fatalf("expected a hub Policy, got %v %v", staticPolicy["apiVersion"], staticPolicy["kind"])
	}
	metadata := nestedMap(t, staticPolicy, "metadata")
	if metadata["name"] != hubPolicyName || metadata["namespace"] != hubPolicyNamespace {
		t.Fatalf("unexpected hub Policy identity: %v/%v", metadata["namespace"], metadata["name"])
	}

	spec := nestedMap(t, staticPolicy, "spec")
	dependencies, ok := spec["dependencies"].([]interface{})
	if !ok || len(dependencies) != 1 {
		t.Fatalf("expected one OperatorPolicy dependency, got %v", spec["dependencies"])
	}
	dependency, ok := dependencies[0].(map[string]interface{})
	if !ok {
		t.Fatalf("dependency has unexpected type %T", dependencies[0])
	}
	wantDependency := map[string]interface{}{
		"apiVersion": "policy.open-cluster-management.io/v1beta1",
		"kind":       "OperatorPolicy",
		"name":       operatorPolicyName,
		"namespace":  operatorPolicyNamespace,
		"compliance": "Compliant",
	}
	if !reflect.DeepEqual(dependency, wantDependency) {
		t.Fatalf("unexpected dependency: got %#v, want %#v", dependency, wantDependency)
	}

	templates := nestedSlice(t, staticPolicy, "spec", "policy-templates")
	if len(templates) != 1 {
		t.Fatalf("expected one ConfigurationPolicy template, got %d", len(templates))
	}
	template, ok := templates[0].(map[string]interface{})
	if !ok {
		t.Fatalf("policy template has unexpected type %T", templates[0])
	}
	definition := nestedMap(t, template, "objectDefinition")
	if definition["kind"] != "ConfigurationPolicy" {
		t.Fatalf("expected a ConfigurationPolicy template, got %v", definition["kind"])
	}
	configurationPolicySpec := nestedMap(t, definition, "spec")
	if configurationPolicySpec["remediationAction"] != "enforce" {
		t.Fatalf("expected ConfigurationPolicy to enforce HyperConverged, got %v", configurationPolicySpec["remediationAction"])
	}
	if nestedMap(t, configurationPolicySpec, "evaluationInterval")["compliant"] != "1m" {
		t.Fatal("hub ConfigurationPolicy must reevaluate while waiting for addon configuration or webhook readiness")
	}
	rawTemplates, ok := configurationPolicySpec["object-templates-raw"].(string)
	if !ok {
		t.Fatalf("expected ConfigurationPolicy object-templates-raw, got %T", configurationPolicySpec["object-templates-raw"])
	}
	addonConfiguration := addonConfigurationPolicy(t, loadManifest(t, "addons/cnv-addon/cnv-addon-template.yaml", nil))

	existingTemplates := renderHyperConvergedTemplate(t, rawTemplates, true, nil, addonConfiguration)
	if len(existingTemplates) != 1 {
		t.Fatalf("expected identity-only template for existing HyperConverged, got %d", len(existingTemplates))
	}
	existingObject := existingTemplates[0]
	existingDefinition := nestedMap(t, existingObject, "objectDefinition")
	existingMetadata := nestedMap(t, existingDefinition, "metadata")
	if existingDefinition["kind"] != "HyperConverged" || existingMetadata["name"] != "kubevirt-hyperconverged" || existingMetadata["namespace"] != "openshift-cnv" {
		t.Fatalf("unexpected existing HyperConverged identity: %#v", existingDefinition)
	}
	if _, found, err := unstructured.NestedMap(existingDefinition, "spec"); err != nil || found {
		t.Fatalf("existing HyperConverged must remain create-only; expected identity-only policy object, got spec %#v (error %v)", existingDefinition["spec"], err)
	}

	for _, tc := range []struct {
		name   string
		slices []map[string]interface{}
	}{
		{name: "no webhook endpoints"},
		{name: "unready webhook endpoint", slices: []map[string]interface{}{endpointSlice("hco-webhook-service", false)}},
		{name: "ready endpoint for another service", slices: []map[string]interface{}{endpointSlice("other-service", true)}},
		{name: "unlabeled endpoint slice", slices: []map[string]interface{}{{"metadata": map[string]interface{}{}, "endpoints": []interface{}{}}}},
		{name: "endpoint without readiness condition", slices: []map[string]interface{}{{
			"metadata":  map[string]interface{}{"labels": map[string]interface{}{"kubernetes.io/service-name": "hco-webhook-service"}},
			"endpoints": []interface{}{map[string]interface{}{}},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if objects := renderHyperConvergedTemplate(t, rawTemplates, false, tc.slices, addonConfiguration); len(objects) != 0 {
				t.Fatalf("HyperConverged must not be created before the webhook has a ready endpoint, got %#v", objects)
			}
		})
	}

	readySlices := []map[string]interface{}{
		endpointSlice("hco-webhook-service", true),
	}
	if objects := renderHyperConvergedTemplate(t, rawTemplates, false, readySlices, nil); len(objects) != 0 {
		t.Fatalf("missing addon configuration must not create HyperConverged, got %#v", objects)
	}
	newTemplates := renderHyperConvergedTemplate(t, rawTemplates, false, readySlices, addonConfiguration)
	if len(newTemplates) != 1 {
		t.Fatalf("expected full HyperConverged template when webhook is ready, got %d", len(newTemplates))
	}
	newObject := newTemplates[0]
	hyperConverged := nestedMap(t, newObject, "objectDefinition")
	hyperConvergedMetadata := nestedMap(t, hyperConverged, "metadata")
	if hyperConverged["kind"] != "HyperConverged" || hyperConvergedMetadata["name"] != "kubevirt-hyperconverged" || hyperConvergedMetadata["namespace"] != "openshift-cnv" {
		t.Fatalf("unexpected new HyperConverged identity: %#v", hyperConverged)
	}
	configuredObject := nestedMap(t, nestedSlice(t, addonConfiguration, "spec", "object-templates")[0].(map[string]interface{}), "objectDefinition")
	if !reflect.DeepEqual(hyperConverged, configuredObject) {
		t.Fatalf("hub Policy must use the AddOnTemplate ConfigurationPolicy object; got %#v, want %#v", hyperConverged, configuredObject)
	}
	customConfiguration := addonConfigurationPolicy(t, loadManifest(t, "addons/cnv-addon/cnv-addon-template.yaml", nil))
	customTemplate := customConfiguration["spec"].(map[string]interface{})["object-templates"].([]interface{})[0].(map[string]interface{})
	customObject := customTemplate["objectDefinition"].(map[string]interface{})
	customObject["spec"].(map[string]interface{})["virtualization"].(map[string]interface{})["vmiCPUAllocationRatio"] = float64(12)
	customRendered := renderHyperConvergedTemplate(t, rawTemplates, false, readySlices, customConfiguration)
	if len(customRendered) != 1 || !reflect.DeepEqual(customRendered[0]["objectDefinition"], customObject) {
		t.Fatalf("customized addon ConfigurationPolicy was not used: %#v", customRendered)
	}

	wantFeatureGates := map[string]string{
		"decentralizedLiveMigration": "Enabled",
		"downwardMetrics":            "Disabled",
		"disableMDevConfiguration":   "Disabled",
		"deployKubeSecondaryDNS":     "Disabled",
		"alignCPUs":                  "Disabled",
		"persistentReservation":      "Disabled",
	}
	featureGates := nestedSlice(t, hyperConverged, "spec", "featureGates")
	if len(featureGates) != len(wantFeatureGates) {
		t.Fatalf("expected %d HyperConverged feature gates, got %d", len(wantFeatureGates), len(featureGates))
	}
	for _, rawFeatureGate := range featureGates {
		featureGate, ok := rawFeatureGate.(map[string]interface{})
		if !ok {
			t.Fatalf("feature gate has unexpected type %T", rawFeatureGate)
		}
		name, _ := featureGate["name"].(string)
		state, _ := featureGate["state"].(string)
		if wantFeatureGates[name] != state {
			t.Errorf("unexpected HyperConverged feature gate %q state %q", name, state)
		}
		delete(wantFeatureGates, name)
	}
	if len(wantFeatureGates) > 0 {
		t.Errorf("HyperConverged Policy is missing feature gates: %#v", wantFeatureGates)
	}
}

func endpointSlice(serviceName string, ready bool) map[string]interface{} {
	return map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{"kubernetes.io/service-name": serviceName},
		},
		"endpoints": []interface{}{
			map[string]interface{}{"conditions": map[string]interface{}{"ready": ready}},
		},
	}
}

func addonConfigurationPolicy(t *testing.T, addon map[string]interface{}) map[string]interface{} {
	t.Helper()
	for _, rawManifest := range nestedSlice(t, addon, "spec", "agentSpec", "workload", "manifests") {
		manifest := rawManifest.(map[string]interface{})
		if manifest["kind"] == "ConfigurationPolicy" {
			return manifest
		}
	}
	t.Fatal("AddOnTemplate is missing the customizable ConfigurationPolicy")
	return nil
}

func renderHyperConvergedTemplate(t *testing.T, rawTemplates string, existing bool, slices []map[string]interface{}, addonConfiguration map[string]interface{}) []map[string]interface{} {
	t.Helper()

	lookup := func(apiVersion, kind, namespace, name string) interface{} {
		switch kind {
		case "HyperConverged":
			if apiVersion != "hco.kubevirt.io/v1" || namespace != "openshift-cnv" || name != "kubevirt-hyperconverged" {
				t.Errorf("unexpected HyperConverged lookup: %s %s %s/%s", apiVersion, kind, namespace, name)
			}
			if existing {
				return map[string]interface{}{"kind": "HyperConverged"}
			}
			return ""
		case "EndpointSlice":
			if apiVersion != "discovery.k8s.io/v1" || namespace != "openshift-cnv" || name != "" {
				t.Errorf("unexpected EndpointSlice lookup: %s %s %s/%s", apiVersion, kind, namespace, name)
			}
			return map[string]interface{}{"items": slices}
		case "ConfigurationPolicy":
			if apiVersion != "policy.open-cluster-management.io/v1" || namespace != operatorPolicyNamespace || name != "kubevirt-hyperconverged-config" {
				t.Errorf("unexpected addon ConfigurationPolicy lookup: %s %s %s/%s", apiVersion, kind, namespace, name)
			}
			if addonConfiguration == nil {
				return ""
			}
			return addonConfiguration
		default:
			t.Errorf("unexpected lookup: %s %s %s/%s", apiVersion, kind, namespace, name)
			return ""
		}
	}

	toJSON := func(value interface{}) (string, error) {
		data, err := json.Marshal(value)
		return string(data), err
	}
	parsed, err := template.New("hyperconverged").Funcs(template.FuncMap{"lookup": lookup, "toJson": toJSON}).Parse(rawTemplates)
	if err != nil {
		t.Fatalf("parse HyperConverged policy template: %v", err)
	}

	var rendered bytes.Buffer
	if err := parsed.Execute(&rendered, nil); err != nil {
		t.Fatalf("render HyperConverged policy template: %v", err)
	}

	var objects []map[string]interface{}
	if err := yaml.Unmarshal(rendered.Bytes(), &objects); err != nil {
		t.Fatalf("parse rendered HyperConverged policy objects: %v\n%s", err, rendered.String())
	}
	return objects
}

func assertSelectiveOrphanRule(t *testing.T, object map[string]interface{}, fields ...string) {
	t.Helper()

	deleteOption := nestedMap(t, object, fields...)
	if deleteOption["propagationPolicy"] != "SelectivelyOrphan" {
		t.Fatalf("expected SelectivelyOrphan deletion, got %#v", deleteOption["propagationPolicy"])
	}
	rules := nestedSlice(t, deleteOption, "selectivelyOrphans", "orphaningRules")
	if len(rules) != 1 {
		t.Fatalf("expected one selectively orphaned resource, got %d", len(rules))
	}
	rule, ok := rules[0].(map[string]interface{})
	if !ok || rule["group"] != "hco.kubevirt.io" || rule["resource"] != "hyperconvergeds" || rule["namespace"] != "openshift-cnv" || rule["name"] != "kubevirt-hyperconverged" {
		t.Fatalf("unexpected selectively orphaned resource: %#v", rules[0])
	}
}

func TestCNVHubPolicyBindingTargetsAddonPlacement(t *testing.T) {
	staticBindingPath := "addons/cnv-addon/cnv-hyperconverged-policy-binding.yaml"
	chartBindingPath := "charts/templates/cnv-hyperconverged-policy-binding.yaml"
	staticBinding := loadManifest(t, staticBindingPath, nil)
	chartBinding := loadManifest(t, chartBindingPath, map[string]string{
		"{{ .Values.global.namespace }}": hubPolicyNamespace,
	})

	if !reflect.DeepEqual(staticBinding, chartBinding) {
		t.Fatal("the standalone and Helm CNV hub Policy bindings have diverged")
	}
	spec := nestedMap(t, staticBinding, "spec")
	placementRef := nestedMap(t, spec, "placementRef")
	if placementRef["name"] != cnvPlacementName || placementRef["kind"] != "Placement" || placementRef["apiGroup"] != "cluster.open-cluster-management.io" {
		t.Fatalf("unexpected placement reference: %#v", placementRef)
	}
	subjects, ok := spec["subjects"].([]interface{})
	if !ok || len(subjects) != 1 {
		t.Fatalf("expected one hub Policy subject, got %v", spec["subjects"])
	}
	subject, ok := subjects[0].(map[string]interface{})
	if !ok || subject["kind"] != "Policy" || subject["name"] != hubPolicyName {
		t.Fatalf("unexpected binding subject: %#v", subjects[0])
	}
}

func TestCleanupScriptUsesReadOnlyHyperConvergedHandoff(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(repoRoot(), "update_cleanup.sh"))
	if err != nil {
		t.Fatalf("read update_cleanup.sh: %v", err)
	}
	scriptText := string(script)
	if !strings.Contains(scriptText, "cnv-hyperconverged-policy.yaml") || !strings.Contains(scriptText, "cnv-hyperconverged-policy-binding.yaml") {
		t.Fatal("update_cleanup.sh must install the dependent hub Policy and PlacementBinding")
	}

	workStart := strings.Index(scriptText, "apiVersion: work.open-cluster-management.io/v1\nkind: ManifestWork")
	if workStart < 0 {
		t.Fatal("could not find the per-cluster ManifestWork in update_cleanup.sh")
	}
	workEnd := strings.Index(scriptText[workStart:], "\nEOF")
	if workEnd < 0 {
		t.Fatal("could not find the end of the per-cluster ManifestWork in update_cleanup.sh")
	}
	work := make(map[string]interface{})
	if err := yaml.Unmarshal([]byte(scriptText[workStart:workStart+workEnd]), &work); err != nil {
		t.Fatalf("parse update_cleanup.sh ManifestWork: %v", err)
	}
	assertSelectiveOrphanRule(t, work, "spec", "deleteOption")

	manifestConfigs := nestedSlice(t, work, "spec", "manifestConfigs")
	readOnlyHCO := false
	for _, rawConfig := range manifestConfigs {
		config, ok := rawConfig.(map[string]interface{})
		if !ok {
			t.Fatalf("ManifestWork config has unexpected type %T", rawConfig)
		}
		identifier := nestedMap(t, config, "resourceIdentifier")
		strategy := nestedMap(t, config, "updateStrategy")
		if identifier["group"] == "hco.kubevirt.io" {
			readOnlyHCO = identifier["resource"] == "hyperconvergeds" && identifier["name"] == "kubevirt-hyperconverged" && identifier["namespace"] == "openshift-cnv" && strategy["type"] == "ReadOnly"
		}
	}
	if !readOnlyHCO {
		t.Fatalf("expected update_cleanup.sh to retain a ReadOnly HyperConverged handoff, got %#v", manifestConfigs)
	}

	manifests := nestedSlice(t, work, "spec", "workload", "manifests")
	hyperConvergedFound := false
	configurationFound := false
	addonConfiguration := addonConfigurationPolicy(t, loadManifest(t, "addons/cnv-addon/cnv-addon-template.yaml", nil))
	for _, rawManifest := range manifests {
		manifest, ok := rawManifest.(map[string]interface{})
		if !ok {
			t.Fatalf("ManifestWork manifest has unexpected type %T", rawManifest)
		}
		if manifest["kind"] == "HyperConverged" {
			hyperConvergedFound = true
			metadata := nestedMap(t, manifest, "metadata")
			if metadata["name"] != "kubevirt-hyperconverged" || metadata["namespace"] != "openshift-cnv" {
				t.Fatalf("unexpected HyperConverged handoff identity: %#v", manifest)
			}
			if _, found, err := unstructured.NestedMap(manifest, "spec"); err != nil || found {
				t.Fatalf("HyperConverged handoff manifest must not configure the CR, got spec %#v (error %v)", manifest["spec"], err)
			}
		}
		if manifest["kind"] == "ConfigurationPolicy" {
			configurationFound = true
			if !reflect.DeepEqual(manifest, addonConfiguration) {
				t.Fatalf("cleanup script ConfigurationPolicy differs from AddOnTemplate: %#v", manifest)
			}
		}
	}
	if !hyperConvergedFound {
		t.Fatal("update_cleanup.sh must retain the identity-only HyperConverged handoff manifest")
	}
	if !configurationFound {
		t.Fatal("update_cleanup.sh must include the customizable addon ConfigurationPolicy")
	}
}
