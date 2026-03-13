#!/bin/bash
set -euo pipefail

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

echo "=== Step 3: Applying ClusterManagementAddOn with correct namespace ==="
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

echo "=== Step 4: Reordering ManifestWork resources per cluster ==="
CLUSTERS=$(oc get managedcluster -l 'acm/cnv-operator-install=true' \
  -o jsonpath='{.items[*].metadata.name}')

if [ -z "${CLUSTERS}" ]; then
  echo "WARNING: No managed clusters found with label acm/cnv-operator-install=true"
  exit 0
fi

for cluster in ${CLUSTERS}; do
  echo "--- Processing cluster: ${cluster} ---"

  # Re-apply the ManifestWork with RBAC placed before OperatorPolicy so that
  # klusterlet-work-sa has the required permissions when OperatorPolicy is applied.
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
      group: hco.kubevirt.io
      name: kubevirt-hyperconverged
      namespace: openshift-cnv
      resource: hyperconvergeds
    updateStrategy:
      type: CreateOnly
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
    - apiVersion: hco.kubevirt.io/v1beta1
      kind: HyperConverged
      metadata:
        name: kubevirt-hyperconverged
        annotations:
          deployOVS: "false"
        namespace: openshift-cnv
      spec:
        virtualMachineOptions:
          disableFreePageReporting: false
          disableSerialConsoleLog: true
        higherWorkloadDensity:
          memoryOvercommitPercentage: 100
        liveMigrationConfig:
          allowAutoConverge: false
          allowPostCopy: false
          completionTimeoutPerGiB: 800
          parallelMigrationsPerCluster: 5
          parallelOutboundMigrationsPerNode: 2
          progressTimeout: 150
        certConfig:
          ca:
            duration: 48h0m0s
            renewBefore: 24h0m0s
          server:
            duration: 24h0m0s
            renewBefore: 12h0m0s
        applicationAwareConfig:
          allowApplicationAwareClusterResourceQuota: false
          vmiCalcConfigName: DedicatedVirtualResources
        featureGates:
          decentralizedLiveMigration: true
          deployTektonTaskResources: false
          enableCommonBootImageImport: true
          withHostPassthroughCPU: false
          downwardMetrics: false
          disableMDevConfiguration: false
          enableApplicationAwareQuota: false
          deployKubeSecondaryDNS: false
          nonRoot: true
          alignCPUs: false
          enableManagedTenantQuota: false
          primaryUserDefinedNetworkBinding: false
          deployVmConsoleProxy: false
          persistentReservation: false
          autoResourceLimits: false
          deployKubevirtIpamController: false
        workloadUpdateStrategy:
          batchEvictionInterval: 1m0s
          batchEvictionSize: 10
          workloadUpdateMethods:
          - LiveMigrate
        uninstallStrategy: BlockUninstallIfWorkloadsExist
        resourceRequirements:
          vmiCPUAllocationRatio: 10
EOF
  echo "Applied ManifestWork for cluster: ${cluster}"

done

echo "=== Done ==="
