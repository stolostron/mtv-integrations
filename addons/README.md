# MTV and CNV Addons for Red Hat Advanced Cluster Management

## Overview

This folder contains addons for Advanced Cluster Management (ACM) that enable virtualization and migration capabilities:

1. **MTV (Migration Toolkit for Virtualization) Addon**
2. **CNV (Container-native Virtualization) Addon**
3. **Proxy CCLM Addon** (`proxy-cclm-addon/`)

Both addons require the OperatorPolicy to be deployed on the target cluster, which is part of the ACM policy addon.

## MTV Addon

### Purpose
The MTV Addon deploys the Migration Toolkit for Virtualization operator, which enables live migration of virtual machines between OpenShift clusters.

### Features
- Deploys the MTV operator in the `openshift-mtv` namespace
- Configures the ForkliftController with UI plugin, validation, and volume populator features enabled
- Uses OperatorPolicy to manage the operator lifecycle
- Automatically upgrades the operator (Automatic approval)

### Requirements
- ACM installed
- Policy addon installed (for OperatorPolicy) on the hub cluster (local-cluster)

### Configuration
The addon is configured to:
- Use the `release-v5.0` channel for operator updates
- Deploy in the `openshift-mtv` namespace
- Enable UI plugin, validation, and volume populator features

## CNV Addon

### Purpose
The CNV Addon deploys the KubeVirt Hyperconverged operator, which provides virtualization capabilities on OpenShift clusters.

### Features
- Deploys the KubeVirt Hyperconverged operator in the `openshift-cnv` namespace
- Keeps the editable HyperConverged configuration in the AddOnTemplate's ConfigurationPolicy
- Uses OperatorPolicy to manage the operator lifecycle
- Automatically upgrades the operator (Automatic approval)

### Requirements
- ACM installed
- Policy addon installed on the hub and managed clusters
- Target cluster must be labeled with `acm/cnv-operator-install: "true"` for the addon to deploy CNV via ACM
- To skip ACM CNV install (operator installed manually or by your own policy), set `acm/cnv-operator-manual-install: "true"` on the `ManagedCluster`. Omit the label or set it to `"false"` to allow the addon. MTV provider onboarding may still use `acm/cnv-operator-install` (see ACM-41613). The hub is still placed via `local-cluster: "true"` regardless of manual-install labels.

### Configuration
The addon is configured with:
- Stable channel for operator updates
- HyperConverged settings including:
  - Memory overcommit percentage: 100%
  - Live migration configuration
  - Feature gate states

## Proxy CCLM Addon

### Purpose

When a hub `ManagedCluster` is labeled `acm/proxy-cclm: "true"`, this addon applies the CCLM **Proxy** datapath on that spoke: a managed `ConfigurationPolicy` requires `KubeVirt` feature gates `DecentralizedLiveMigration` and `CrossClusterMigrationProxy` plus `decentralizedLiveMigrationDatapath: Proxy` (additional customer gates are allowed), plus the virt-synchronization `Service` and `Route` from `docs/examples/cross-cluster-sync-proxy.yaml`.

### Requirements

- CNV / `kubevirt-kubevirt-hyperconverged` must already exist on the spoke (this addon does not install CNV). Spokes may use a **manual** CNV install (`acm/cnv-operator-manual-install: "true"`) without the hub `kubevirt-hyperconverged` addon (see CNV placement in ACM-41613).
- Policy addon on hub and spokes.

The AddOnTemplate includes Role `proxy-cclm-policy-manager` and bindings for `klusterlet-work-sa` so it can create the managed `ConfigurationPolicy`. That RBAC is separate from the CNV addon’s `operatorpolicy-manager`, so proxy-cclm works when CNV was installed manually and the CNV addon was never placed on the spoke.

See `docs/azure-cclm-proxy-setup.md` for manual steps this addon automates.

## Installation

To install the addons on your ACM hub cluster, use the following commands:

```bash
# Deploy CNV Addon
oc apply -f ./cnv-addon

# Deploy MTV Addon
oc apply -f ./mtv-addon

# Deploy Proxy CCLM addon (hub Placement + template)
oc apply -f ./proxy-cclm-addon

# Delete CNV Addon
oc delete -f ./cnv-addon

# Delete MTV Addon
oc delete -f ./mtv-addon
```

Note: 
  * Make sure you apply these manifests to the ACM hub
  * To target a cluster for the CNV operator,  label it with `acm/cnv-operator-install: "true"` to initiate a deploy
  * When removing the addon or the label, the operator is NOT uninstalled, you must clean it up manually on the target 
  cluster. This is done by logging into the cluster an removing the Operator from the console or cli.

### HyperConverged creation gate

The CNV AddOnTemplate installs one HCO OperatorPolicy and an enforcing ConfigurationPolicy named `kubevirt-hyperconverged-config`. Edit the HCO object under that ConfigurationPolicy's `spec.object-templates-raw` to customize the resource created on a fresh install. After the OperatorPolicy reports `Compliant`, the ConfigurationPolicy renders the full HCO definition if the CR is missing, or only its identity if it already exists. This preserves existing HCO settings and later edits. The addon ManifestWork includes the ConfigurationPolicy but no direct HCO manifest. There is no separate hub Policy. OperatorPolicy compliance does not explicitly confirm ready endpoints for `hco-webhook-service`.

The AddOnTemplate renderer normally consumes Go template delimiters before a ConfigurationPolicy can evaluate them. The AddOnDeploymentConfig in `cnv-clustermanagementaddon.yaml` supplies the `HCO_TEMPLATE_START` delimiter so the managed policy receives its OperatorPolicy lookup template. Keep that default config in the ClusterManagementAddOn. A per-cluster AddOnDeploymentConfig override must also define `HCO_TEMPLATE_START` with the value `{{`; otherwise the policy template will not render. The ConfigurationPolicy uses `CreateOnly`: new clusters receive the enforcing policy, while an existing managed ConfigurationPolicy keeps its current settings.

## ClusterRole Permissions

### clusterrole.yaml
This file contains ClusterPermission resources that grant KubeVirt admin access to cluster administrators.

#### What it does:
- **kubevirt-admin ClusterPermission**: Binds the `kubevirt.io:admin` ClusterRole to the `system:cluster-admins` group
- Grants full administrative privileges for KubeVirt resources (VMs, DataVolumes, etc.)
- Required for administrators to manage virtualization resources deployed by the CNV addon

#### Configuration:
- Currently configured for `bm14` and `local-cluster` namespaces (examples)
- **Important**: Update the namespace values to match your target cluster names before applying

#### Usage:
1. Edit the file to replace `bm14` and `local-cluster` with your actual cluster names:
   ```yaml
   metadata:
     name: kubevirt-admin
     namespace: <your-cluster-name>
   ```

2. Apply the permissions:
   ```bash
   oc apply -f clusterrole.yaml
   ```

3. Verify the permissions are applied:
   ```bash
   oc get clusterpermissions -A
   ```

## Migration Advisor External Access

Migration Advisor runs in the hub-side `mtv-integrations` controller, not in the spoke addon manifests in this folder.

When deploying the hub controller via Helm chart templates, external access is exposed through:

- Service: `mtv-advisor-service` (port `8082`)
- Route: `mtv-advisor-route`

Endpoints:

- `/api/v1/migration-targets`
- `/health`
