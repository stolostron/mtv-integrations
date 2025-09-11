# MTV Integrations for Open Cluster Management

## Overview

This repository provides comprehensive integration capabilities for the Migration Toolkit for Virtualization (MTV) within Advanced Cluster Management (ACM) environments. It includes both a controller for MTV provider management and a webhook for plan access controller. There are also addons for virtualization capabilities.

## Core Components

### Provider Manager Controller

The **Provider Manager Controller** (implemented as the `ManagedClusterReconciler`) integrates ACM managed clusters as MTV providers. Its main responsibilities include:

- Managing MTV provider lifecycle on ACM managed clusters
- Ensuring proper authentication and authorization for MTV operations
- Coordinating with the MTV plan webhook for access control

The **MTV plan webhook** is a validating admission webhook for the `Plan` resource (from the Forklift/MTV API). Its purpose is to enforce security and access control when users create or update migration plans.

## Addons

This repository also contains two addons for OCM that enable container native virtualization and migration toolkit for virtualization capabilities:

### MTV (Migration Toolkit for Virtualization) Addon

**Quick summary:**
- **MTV Addon:** Installs the Migration Toolkit for Virtualization operator in the `openshift-mtv` namespace on the hub, enabling VM migration features. It uses the `release-v2.10` channel and enables UI plugin, validation, and volume populator features.
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

### How to Run the Controller on OpenShift

#### Deploy via CI and Quay.io

1. **Open a Pull Request:**  
   Submit a PR to this repository. Once your PR is approved, merge it into the `main` branch.

2. **Login to Your OpenShift Cluster:**  
   Use `oc login` to authenticate to your target OpenShift cluster. This ensures your local kubeconfig points to the correct cluster.

3. **Get the Image Name:**  
   Go to the Actions tab in GitHub. Find the workflow "Push image to quay.io registry" and run it. After the CI job completes, retrieve your controller image name from `quay.io/stolostron-vm`.

4. **Deploy the Controller:**  
   Replace `QUAY_IMG` with your actual image name and deploy using:
   ```bash
   kubectl apply -f config/default/webhook.yaml
   IMG=quay.io/stolostron/mtv-integrations:latest make deploy
   ```

#### Run Locally in VS Code (without webhook)

You can run the controller locally for development (without the webhook) using VS Code's debugger.  
Add the following configuration to your `.vscode/launch.json`:

```json
{
    "version": "0.2.0",
    "configurations": [
        {
            "name": "Launch Controller (No Webhook)",
            "type": "go",
            "request": "launch",
            "mode": "auto",
            "program": "${workspaceFolder}/cmd/main.go",
            "args": [
                "--webhook-cert-path=${workspaceFolder}",
                "--enable-webhook=false"
            ]
        }
    ]
}
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
