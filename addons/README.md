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
- Use the `release-v2.10` channel for operator updates
- Deploy in the `openshift-mtv` namespace
- Enable UI plugin, validation, and volume populator features

## CNV Addon

### Purpose
The CNV Addon deploys the KubeVirt Hyperconverged operator, which provides virtualization capabilities on OpenShift clusters.

### Features
- Deploys the KubeVirt Hyperconverged operator in the `openshift-cnv` namespace
- Configures HyperConverged custom resource with optimized settings
- Sets up HostPathProvisioner for storage
- Uses OperatorPolicy to manage the operator lifecycle
- Automatically upgrades the operator (Automatic approval)

### Requirements
- ACM installed
- Policy addon installed (for OperatorPolicy) on the managed cluster
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
