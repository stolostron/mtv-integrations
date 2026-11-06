#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

echo "=== Step 1: Cleaning up old addon resources ==="
oc delete clustermanagementaddon kubevirt-hyperconverged-operator --ignore-not-found
oc delete addontemplate kubevirt-hyperconverged-operator --ignore-not-found

echo "=== Step 2: Getting installer namespace from ClusterManagementAddOn ==="
INSTALLER_NAMESPACE=$(oc get clustermanagementaddon kubevirt-hyperconverged \
  -o jsonpath='{.metadata.labels.installer\.namespace}' 2>/dev/null || true)

if [ -z "${INSTALLER_NAMESPACE}" ]; then
  echo "ERROR: Could not find installer.namespace label on ClusterManagementAddOn kubevirt-hyperconverged"
  exit 1
fi
echo "Found installer.namespace: ${INSTALLER_NAMESPACE}"

echo "=== Step 3: Applying the gated CNV addon resources ==="
oc apply -f "${SCRIPT_DIR}/addons/cnv-addon/cnv-addon-template.yaml"
oc apply -f "${SCRIPT_DIR}/addons/cnv-addon/cnv-hyperconverged-policy.yaml"
oc apply -f "${SCRIPT_DIR}/addons/cnv-addon/cnv-hyperconverged-policy-binding.yaml"

oc apply -f - <<EOF
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
  installStrategy:
    type: Placements
    placements:
    - name: openshift-cnv
      namespace: ${INSTALLER_NAMESPACE}
EOF

echo "=== Step 4: Updating ManifestWork resources per cluster ==="
CLUSTERS=$(oc get managedcluster -l 'acm/cnv-operator-install=true' \
  -o jsonpath='{.items[*].metadata.name}')

if [ -z "${CLUSTERS}" ]; then
  echo "WARNING: No managed clusters found with label acm/cnv-operator-install=true"
  exit 0
fi

for cluster in ${CLUSTERS}; do
  echo "--- Processing cluster: ${cluster} ---"

  # Keep the HyperConverged object identity-only and ReadOnly during the
  # ownership handoff. The dependent hub Policy creates/configures it after
  # the OperatorPolicy reports Compliant. ReadOnly also removes the old Work
  # owner reference without overwriting existing cluster customization.
  oc apply -f - <<EOF
apiVersion: work.open-cluster-management.io/v1
kind: ManifestWork
metadata:
  name: addon-kubevirt-hyperconverged-deploy-0
  namespace: ${cluster}
spec:
  manifestConfigs:
  - feedbackScrapeType: Poll
    resourceIdentifier:
      group: policy.open-cluster-management.io
      name: kubevirt-hyperconverged-operator
      namespace: open-cluster-management-policies
      resource: operatorpolicies
    updateStrategy:
      type: CreateOnly
  - resourceIdentifier:
      group: hco.kubevirt.io
      name: kubevirt-hyperconverged
      namespace: openshift-cnv
      resource: hyperconvergeds
    updateStrategy:
      type: ReadOnly
  workload:
    manifests:
    - apiVersion: rbac.authorization.k8s.io/v1
      kind: Role
      metadata:
        name: operatorpolicy-manager
        namespace: open-cluster-management-policies
      rules:
      - apiGroups: ["policy.open-cluster-management.io"]
        resources: ["operatorpolicies"]
        verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
    - apiVersion: rbac.authorization.k8s.io/v1
      kind: RoleBinding
      metadata:
        name: operatorpolicy-manager-binding
        namespace: open-cluster-management-policies
      roleRef:
        apiGroup: rbac.authorization.k8s.io
        kind: Role
        name: operatorpolicy-manager
      subjects:
      - kind: ServiceAccount
        name: klusterlet-work-sa
        namespace: open-cluster-management-agent
    - apiVersion: rbac.authorization.k8s.io/v1
      kind: RoleBinding
      metadata:
        name: operatorpolicy-manager-hosted-binding
        namespace: open-cluster-management-policies
      roleRef:
        apiGroup: rbac.authorization.k8s.io
        kind: Role
        name: operatorpolicy-manager
      subjects:
      - kind: ServiceAccount
        name: klusterlet-${cluster}-work-sa
        namespace: open-cluster-management-${cluster}
    - apiVersion: policy.open-cluster-management.io/v1beta1
      kind: OperatorPolicy
      metadata:
        name: kubevirt-hyperconverged-operator
        namespace: open-cluster-management-policies
      spec:
        remediationAction: enforce
        complianceType: musthave
        operatorGroup:
          name: openshift-cnv
          namespace: openshift-cnv
          targetNamespaces:
          - openshift-cnv
        subscription:
          channel: stable
          name: kubevirt-hyperconverged
          namespace: openshift-cnv
        upgradeApproval: Automatic
    - apiVersion: hco.kubevirt.io/v1
      kind: HyperConverged
      metadata:
        name: kubevirt-hyperconverged
        namespace: openshift-cnv
  deleteOption:
    propagationPolicy: SelectivelyOrphan
    selectivelyOrphans:
      orphaningRules:
      - group: hco.kubevirt.io
        resource: hyperconvergeds
        namespace: openshift-cnv
        name: kubevirt-hyperconverged
EOF
  echo "Applied ManifestWork for cluster: ${cluster}"

done

echo "=== Done ==="
