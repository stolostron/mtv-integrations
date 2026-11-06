#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

oc delete clustermanagementaddon kubevirt-hyperconverged-operator --ignore-not-found
oc delete addontemplate kubevirt-hyperconverged-operator --ignore-not-found

INSTALLER_NAMESPACE="$(oc get clustermanagementaddon kubevirt-hyperconverged \
  -o jsonpath='{.metadata.labels.installer\.namespace}')"
if [[ -z "$INSTALLER_NAMESPACE" ]]; then
  echo "Could not find installer.namespace on ClusterManagementAddOn kubevirt-hyperconverged" >&2
  exit 1
fi

CLUSTERS="$(oc get managedclusters -l 'acm/cnv-operator-install=true' \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')"

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
oc apply -f "${SCRIPT_DIR}/addons/cnv-addon/cnv-addon-template.yaml"

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
