#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
command -v yq >/dev/null || { echo "yq v4 is required" >&2; exit 1; }

template_file="${SCRIPT_DIR}/addons/cnv-addon/cnv-addon-template.yaml"

# Read the HCO definition from either the older raw HCO manifest or the
# ConfigurationPolicy used by newer AddOnTemplates. Fail if a live template
# exists but its desired HCO configuration cannot be identified.
extract_hco_definition() {
  local addon_json="$1" policy_json raw_hco definition
  policy_json="$(jq -c '.spec.agentSpec.workload.manifests[]? |
    select(.kind == "ConfigurationPolicy" and .metadata.name == "kubevirt-hyperconverged-config")' <<<"$addon_json")"
  if [[ -n "$policy_json" ]]; then
    raw_hco="$(jq -r '.spec["object-templates-raw"] // empty' <<<"$policy_json")"
    if [[ -n "$raw_hco" ]]; then
      # Remove the policy's Go control lines to parse its full HCO definition.
      definition="$(printf '%s\n' "$raw_hco" |
        jq -Rs -r 'split("\n") | map(select((test("^[[:space:]]*\\{\\{.*\\}\\}[[:space:]]*$") or
          test("^[[:space:]]*\\[\\][[:space:]]*$")) | not)) | join("\n")' |
        yq -p=yaml -o=json '.' - |
        jq -c -e '[.[] | select(.objectDefinition.spec != null) | .objectDefinition] |
          if length == 1 then .[0] else error("expected one full HCO definition") end')" || return 1
    else
      definition="$(jq -c '.spec["object-templates"][0].objectDefinition // empty' <<<"$policy_json")"
    fi
  else
    definition="$(jq -c '.spec.agentSpec.workload.manifests[]? |
      select(.kind == "HyperConverged" and .metadata.name == "kubevirt-hyperconverged" and .spec)' <<<"$addon_json")"
  fi
  [[ -n "$definition" ]] || return 1
  jq -c -e 'select(.apiVersion == "hco.kubevirt.io/v1" and .kind == "HyperConverged" and
    .metadata.name == "kubevirt-hyperconverged" and .metadata.namespace == "openshift-cnv" and
    (.spec | type == "object"))' <<<"$definition"
}

# The checked-in template supplies the new readiness gate. Keep that gate,
# but replace its fresh-install HCO definition with the merged live values.
customize_hco_template() {
  local raw_template="$1" hco_json="$2" hco_yaml
  hco_yaml="$(printf '%s\n' "$hco_json" | yq -p=json -P '.' | sed 's/^/    /')"
  jq -nr --arg raw "$raw_template" --arg hco "$hco_yaml" '
    ($raw | split("- complianceType: musthave\n")) as $item |
    if ($item | length) != 2 then error("unexpected HCO object template") else . end |
    ($item[1] | split("{{HCO_TEMPLATE_START}} if not $existingHCO }}\n")) as $identity |
    if ($identity | length) != 2 then error("missing existing-HCO gate") else . end |
    ($identity[1] | split("{{HCO_TEMPLATE_START}} else }}\n[]\n")) as $tail |
    if ($tail | length) != 2 then error("missing operator readiness fallback") else . end |
    $item[0] + "{{HCO_TEMPLATE_START}} if $existingHCO }}\n" +
    "- complianceType: musthave\n" + $identity[0] +
    "{{HCO_TEMPLATE_START}} else }}\n" +
    "- complianceType: musthave\n  objectDefinition:\n" + $hco + "\n" +
    "{{HCO_TEMPLATE_START}} end }}\n" +
    "{{HCO_TEMPLATE_START}} else }}\n[]\n" + $tail[1]'
}

desired_template="$(yq -o=json '.' "$template_file")"
default_hco="$(extract_hco_definition "$desired_template")" || {
  echo "Could not read the checked-in HCO definition" >&2; exit 1;
}
live_template="$(oc get addontemplate kubevirt-hyperconverged --ignore-not-found -o json)"
if [[ -n "$live_template" ]]; then
  live_hco="$(extract_hco_definition "$live_template")" || {
    echo "Could not safely read HCO settings from the live AddOnTemplate; no changes were made." >&2
    exit 1
  }
  # Keep customer values while supplying fields introduced by the new template.
  # Do this before any cluster mutation so an unreadable template fails safely.
  merged_hco="$(jq -cn --argjson base "$default_hco" --argjson live "$live_hco" '$base * $live')"
  if ! jq -en --argjson base "$default_hco" --argjson merged "$merged_hco" \
    '$base == $merged' >/dev/null; then
    default_raw="$(jq -r '.spec.agentSpec.workload.manifests[] |
      select(.kind == "ConfigurationPolicy" and .metadata.name == "kubevirt-hyperconverged-config") |
      .spec["object-templates-raw"]' <<<"$desired_template")"
    custom_raw="$(customize_hco_template "$default_raw" "$merged_hco")" || {
      echo "Could not preserve live HCO settings; no changes were made." >&2; exit 1;
    }
    desired_template="$(jq --arg raw "$custom_raw" '
      .spec.agentSpec.workload.manifests |= map(
        if .kind == "ConfigurationPolicy" and .metadata.name == "kubevirt-hyperconverged-config"
        then .spec["object-templates-raw"] = $raw else . end)' <<<"$desired_template")"
  fi
fi

oc delete clustermanagementaddon kubevirt-hyperconverged-operator --ignore-not-found
oc delete addontemplate kubevirt-hyperconverged-operator --ignore-not-found

INSTALLER_NAMESPACE="$(oc get clustermanagementaddon kubevirt-hyperconverged \
  -o jsonpath='{.metadata.labels.installer\.namespace}')"
if [[ -z "$INSTALLER_NAMESPACE" ]]; then
  echo "Could not find installer.namespace on ClusterManagementAddOn kubevirt-hyperconverged" >&2
  exit 1
fi

# Skip clusters that install CNV manually (same rule as openshift-cnv Placement).
CLUSTERS="$(oc get managedclusters -l 'acm/cnv-operator-install=true' -o json | jq -r '
  .items[] | select(.metadata.labels["acm/cnv-operator-manual-install"] != "true") | .metadata.name')"

# Add the delimiter variable before updating the template. Without it, the
# AddOnTemplate renderer would remove the managed policy's Go templates.
oc apply -f - <<'EOF_TEMPLATE_VALUES'
apiVersion: addon.open-cluster-management.io/v1alpha1
kind: AddOnDeploymentConfig
metadata:
  name: kubevirt-hyperconverged-template-values
  namespace: open-cluster-management-global-set
spec:
  customizedVariables:
    - name: HCO_TEMPLATE_START
      value: "{{"
EOF_TEMPLATE_VALUES
oc apply -f - <<EOF_CMA
apiVersion: addon.open-cluster-management.io/v1beta1
kind: ClusterManagementAddOn
metadata:
  name: kubevirt-hyperconverged
  annotations:
    addon.open-cluster-management.io/lifecycle: "addon-manager"
spec:
  addOnMeta:
    description: Kubevirt Hyperconverged
    displayName: Kubevirt Hyperconverged
  defaultConfigs:
    - group: addon.open-cluster-management.io
      resource: addontemplates
      name: kubevirt-hyperconverged
    - group: addon.open-cluster-management.io
      resource: addondeploymentconfigs
      name: kubevirt-hyperconverged-template-values
      namespace: open-cluster-management-global-set
  installStrategy:
    type: Placements
    placements:
      - name: openshift-cnv
        namespace: ${INSTALLER_NAMESPACE}
EOF_CMA

for cluster in $CLUSTERS; do
  ready=false
  for attempt in {1..60}; do
    addon_status="$(oc -n "$cluster" get managedclusteraddon kubevirt-hyperconverged \
      -o json 2>/dev/null || true)"
    config_ref="$(jq -c '.status.configReferences[]? |
      select(.resource == "addondeploymentconfigs") | .desiredConfig // empty' <<<"$addon_status")"
    if [[ -n "$config_ref" ]]; then
      config_name="$(jq -r '.name' <<<"$config_ref")"
      config_namespace="$(jq -r '.namespace' <<<"$config_ref")"
      if oc -n "$config_namespace" get addondeploymentconfig "$config_name" -o json |
        jq -e '.spec.customizedVariables | any(.name == "HCO_TEMPLATE_START" and
          .value == "{{")' >/dev/null; then
        ready=true
        break
      fi
    fi
    if (( attempt < 60 )); then
      sleep 5
    fi
  done
  if [[ "$ready" != true ]]; then
    echo "The CNV addon on $cluster needs HCO_TEMPLATE_START={{ in its effective AddOnDeploymentConfig." >&2
    exit 1
  fi
done

# The ConfigurationPolicy in the final AddOnTemplate is the sole HCO writer.
oc delete placementbinding binding-kubevirt-hyperconverged \
  -n open-cluster-management-global-set --ignore-not-found
oc delete policy kubevirt-hyperconverged \
  -n open-cluster-management-global-set --ignore-not-found
printf '%s\n' "$desired_template" | oc apply -f -

# Repair per-cluster ManifestWorks as well. Render the same manifests as the addon
# template controller: replace the cluster name and insert the policy's Go
# template delimiter in one pass.
addon_template="$(oc get addontemplate kubevirt-hyperconverged -o json)"
for cluster in $CLUSTERS; do
  jq -n --argjson addon "$addon_template" --arg cluster "$cluster" '
    def render:
      walk(if type == "string" then
        (split("{{CLUSTER_NAME}}") | join($cluster) |
         split("{{HCO_TEMPLATE_START}}") | join("{{"))
      else . end);
    {apiVersion: "work.open-cluster-management.io/v1",
     kind: "ManifestWork",
     metadata: {name: "addon-kubevirt-hyperconverged-deploy-0", namespace: $cluster},
     spec: $addon.spec.agentSpec | {manifestConfigs, workload, deleteOption}}
    | render' | oc apply -f -
  echo "Applied CNV ManifestWork for $cluster"
done
