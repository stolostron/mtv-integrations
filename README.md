# MTV Integrations for Open Cluster Management

## Overview

This repository provides comprehensive integration capabilities for the Migration Toolkit for Virtualization (MTV) within Advanced Cluster Management (ACM) environments. It includes both a controller for MTV provider management and a webhook for plan access controler. There are also addons for virtualization capabilities.

## Core Components

### Provider Manager Controller

The **Provider Manager Controller** (implemented as the `ManagedClusterReconciler`) integrates ACM managed clusters as MTV providers. Its main responsibilities include:

The **MTV plan webhook** is a validating admission webhook for the `Plan` resource (from the Forklift/MTV API). Its purpose is to enforce security and access control when users create or update migration plans:

## Addons

This repository also contains two addons for OCM that enable container native virtualization and migration toolkit for virtualization capabilities:

### MTV (Migration Toolkit for Virtualization) Addon

**Quick summary:**
- **MTV Addon:** Installs the Migration Toolkit for Virtualization operator in the `openshift-mtv` namespace on the hub, enabling VM migration features. It uses the `release-v2.8` channel and enables UI plugin, validation, and volume populator features.
- **CNV Addon:** Installs the KubeVirt Hyperconverged operator in the `openshift-cnv` namespace, providing virtualization capabilities. It configures optimized HyperConverged settings and uses OperatorPolicy for lifecycle management.

Both addons require ACM and the Policy addon. The CNV Addon targets clusters labeled with `acm/cnv-operator-install: "true"`.

**See the [addons/README.md](addons/README.md) for full details and usage.**

## Architecture Summary

For a detailed explanation of the controller and webhook architecture, see [architecture/README.md](architecture/README.md).

## Installation

### Core Controller and Webhook

```bash
# Build and deploy the controller
make build
make deploy

# Enable webhook (requires certificates)
# Set ENABLE_WEBHOOK=true and provide certificate paths
make deploy ENABLE_WEBHOOK=true
```

### Addons

```bash
# Deploy CNV Addon
oc apply -f ./addons/cnv-addon

# Deploy MTV Addon
oc apply -f ./addons/mtv-addon
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

### Testing
```bash
# Run unit tests
make test

# Run webhook tests
make run-webhook-test
```

### Building Container Image
```bash
# Set your registry
export REGISTRY_BASE=quay.io/your-org
make docker-build
make docker-push
```

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

## Contributing

Please read our [Contributing Guidelines](CONTRIBUTING.md) for details on our code of conduct and the process for submitting pull requests.

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.
