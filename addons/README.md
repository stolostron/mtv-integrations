# MTV and CNV Addons for Red Hat Advanced Cluster Management

## Overview

This folder contains two addons for Advanced Cluster Management (ACM) that enable virtualization and migration capabilities:

1. **MTV (Migration Toolkit for Virtualization) Addon**
2. **CNV (Container-native Virtualization) Addon**

Both addons require the OperatorPolicy to be deployed on the target cluster, which is part of the ACM policy addon.

## MTV Addon

### Purpose
The MTV Addon deploys the Migration Toolkit for Virtualization operator, which enables live migration of virtual machines between OpenShift clusters.

### Features
- Deploys the MTV operator in the `openshift-mtv` namespace
- Configures the ForkliftController with UI plugin (disabled), validation, and volume populator features
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
- Keeps the editable HyperConverged configuration in the AddOnTemplate and creates the CR through a gated hub Policy
- Sets up HostPathProvisioner for storage
- Uses OperatorPolicy to manage the operator lifecycle
- Automatically upgrades the operator (Automatic approval)

### Requirements
- ACM installed
- Policy addon installed on the hub and managed clusters
- Target cluster must be labeled with `acm/cnv-operator-install: "true"`

### Configuration
The addon is configured with:
- Stable channel for operator updates
- Optimized HyperConverged settings including:
  - Memory overcommit percentage: 100%
  - Live migration configuration
  - Resource requirements
  - Feature gates for enhanced functionality
- HostPathProvisioner with 50Gi storage pool

## Installation

To install the addons on your ACM hub cluster, use the following commands:

```bash
# Deploy CNV Addon
oc apply -f ./cnv-addon

# Deploy MTV Addon
oc apply -f ./mtv-addon

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

### HyperConverged readiness gate and recovery

The CNV AddOnTemplate installs the HCO OperatorPolicy and an `inform` ConfigurationPolicy named `kubevirt-hyperconverged-config`. Edit that ConfigurationPolicy's `spec.object-templates[0].objectDefinition` in the AddOnTemplate to customize the HyperConverged resource before a fresh install. The ConfigurationPolicy uses `CreateOnly` in the addon ManifestWork, so subsequent edits to the ConfigurationPolicy on the managed cluster are preserved; later AddOnTemplate edits do not update it. The `inform` policy only reports whether its desired configuration is present; it does not create or update the CR. On an existing CR with custom settings, it may report `NonCompliant` even though the hub Policy intentionally leaves that CR unchanged.

A hub Policy targets the same placement and waits for the OperatorPolicy to report `Compliant`. It also checks for a ready endpoint behind `hco-webhook-service`, then reads the addon ConfigurationPolicy and creates a missing HyperConverged CR from its object definition. It keeps an existing CR identity-only, preserving the previous `CreateOnly` behavior. The addon ManifestWork also keeps an identity-only `ReadOnly` HyperConverged entry. `ReadOnly` does not remove an AppliedManifestWork owner reference left by an older Work; that owner reference needs a separate migration before deleting the Work.

For clusters that failed during an earlier install, see [Recovering a failed CNV HyperConverged install](../docs/CNV_HYPERCONVERGED_RECOVERY.md). The runbook explains how to remove a failed HyperConverged resource, recover a failed HCO CSV if needed, and let the dependent Policy recreate HyperConverged after the operator is healthy.


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
