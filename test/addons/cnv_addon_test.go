package addons_test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

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

func TestCNVAddonTemplatesDoNotApplyHyperConvergedDirectly(t *testing.T) {
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
			for _, rawManifest := range manifests {
				manifest, ok := rawManifest.(map[string]interface{})
				if !ok {
					t.Fatalf("manifest has unexpected type %T", rawManifest)
				}
				if manifest["kind"] == "HyperConverged" {
					t.Fatal("HyperConverged must be created by the dependent hub Policy, not the addon ManifestWork")
				}
				if manifest["kind"] == "OperatorPolicy" {
					operatorPolicies++
				}
			}
			if operatorPolicies != 1 {
				t.Fatalf("expected exactly one OperatorPolicy in the addon ManifestWork, got %d", operatorPolicies)
			}

			manifestConfigs := nestedSlice(t, addon, "spec", "agentSpec", "manifestConfigs")
			for _, rawConfig := range manifestConfigs {
				config, ok := rawConfig.(map[string]interface{})
				if !ok {
					t.Fatalf("manifest config has unexpected type %T", rawConfig)
				}
				identifier := nestedMap(t, config, "resourceIdentifier")
				if identifier["group"] == "hco.kubevirt.io" {
					t.Fatal("the addon ManifestWork must not track HyperConverged")
				}
			}
		})
	}
}

func TestCNVHubPolicyWaitsForOperatorPolicyAndEnforcesHyperConverged(t *testing.T) {
	staticPolicyPath := "addons/cnv-addon/cnv-hyperconverged-policy.yaml"
	chartPolicyPath := "charts/templates/cnv-hyperconverged-policy.yaml"
	staticPolicy := loadManifest(t, staticPolicyPath, nil)
	chartPolicy := loadManifest(t, chartPolicyPath, map[string]string{
		"{{ .Values.global.namespace }}": hubPolicyNamespace,
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
	objects := nestedSlice(t, definition, "spec", "object-templates")
	if len(objects) != 1 {
		t.Fatalf("expected one managed object, got %d", len(objects))
	}
	objectTemplate, ok := objects[0].(map[string]interface{})
	if !ok {
		t.Fatalf("object template has unexpected type %T", objects[0])
	}
	hyperConverged := nestedMap(t, objectTemplate, "objectDefinition")
	hyperConvergedMetadata := nestedMap(t, hyperConverged, "metadata")
	if hyperConverged["kind"] != "HyperConverged" || hyperConvergedMetadata["name"] != "kubevirt-hyperconverged" || hyperConvergedMetadata["namespace"] != "openshift-cnv" {
		t.Fatalf("unexpected managed object identity: %#v", hyperConverged)
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

func TestCleanupScriptDoesNotReapplyHyperConverged(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(repoRoot(), "update_cleanup.sh"))
	if err != nil {
		t.Fatalf("read update_cleanup.sh: %v", err)
	}
	scriptText := string(script)
	if strings.Contains(scriptText, "kind: HyperConverged") || strings.Contains(scriptText, "resource: hyperconvergeds") {
		t.Fatal("update_cleanup.sh must not put HyperConverged back in the addon ManifestWork")
	}
	if !strings.Contains(scriptText, "cnv-hyperconverged-policy.yaml") || !strings.Contains(scriptText, "cnv-hyperconverged-policy-binding.yaml") {
		t.Fatal("update_cleanup.sh must install the dependent hub Policy and PlacementBinding")
	}
}
