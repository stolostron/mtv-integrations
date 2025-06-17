# MTV and CNV Addons for Open Cluster Management

## Overview

This repository contains two addons for Open Cluster Management (OCM) that enable virtualization and migration capabilities:

1. **MTV (Migration Toolkit for Virtualization) Addon**
2. **CNV (Container-native Virtualization) Addon**

Both addons require the OperatorPolicy to be deployed on the target cluster, which is part of the full policy addon.

## MTV Addon

### Purpose
The MTV Addon deploys the Migration Toolkit for Virtualization operator, which enables live migration of virtual machines between OpenShift clusters.

### Features
- Deploys the MTV operator in the `openshift-mtv` namespace
- Configures the ForkliftController with UI plugin, validation, and volume populator features
- Uses OperatorPolicy to manage the operator lifecycle
- Automatically upgrades the operator (Automatic approval)

### Requirements
- Open Cluster Management (OCM) installed
- Full policy addon installed (for OperatorPolicy)
- Target cluster must be labeled with `local-cluster: "true"`

### Configuration
The addon is configured to:
- Use the `release-v2.8` channel for operator updates
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
- Open Cluster Management (OCM) installed
- Full policy addon installed (for OperatorPolicy)
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

## Uninstallation

### Important Note
The addons do NOT automatically remove the operators when uninstalled. Manual cleanup is required.

### Uninstallation Steps

1. Remove the addon from the hub cluster:
   ```bash
   # For MTV Addon
   oc delete clustermanagementaddon mtv-operator -n open-cluster-management
   
   # For CNV Addon
   oc delete clustermanagementaddon kubevirt-hyperconverged-operator -n open-cluster-management
   ```

2. Manually remove the operators from the target clusters:
   ```bash
   # For MTV Operator
   oc delete subscription mtv-operator -n openshift-mtv
   oc delete operatorgroup openshift-mtv -n openshift-mtv
   
   # For CNV Operator
   oc delete subscription kubevirt-hyperconverged -n openshift-cnv
   oc delete operatorgroup openshift-cnv -n openshift-cnv
   ```

3. Remove the namespaces (optional, only if you want to completely clean up):
   ```bash
   oc delete namespace openshift-mtv
   oc delete namespace openshift-cnv
   ```

## Development

### Building
```bash
make build
```

### Running Locally
```bash
make run
```

### Deploying to Cluster
```bash
make deploy
```

### Building Container Image
```bash
# Set your registry
export REGISTRY_BASE=quay.io/your-org
make docker-build
make docker-push
```
