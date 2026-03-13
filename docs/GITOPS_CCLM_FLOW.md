# GitOps VM Migration Flow with ACM

This document describes a safe GitOps-driven workflow for migrating VMs across OpenShift clusters managed by ACM (Advanced Cluster Management), using Argo CD ApplicationSets and the MTV (Migration Toolkit for Virtualization) operator.

## Overview

The approach prioritizes safety and control. It prevents Argo CD from automatically deleting or attempting to restart the source VM post-migration, requiring explicit user action for cleanup.

```
┌─────────────────────────────────────────────────────────────────────────┐
│  1. Prepare: Disable auto-sync / set runStrategy: Manual               │
│  2. Migrate: Commit the Plan or start migration from Console           │
│  3. Wait:    Plan status.vms[0].phase == "Completed"                   │
│  4. Verify:  Source VM is stopped                                      │
│  5. PR:      Place target VM yaml in target cluster dir, merge PR      │
│  6. Cleanup: (Optional) Delete the source VM from git                  │
└─────────────────────────────────────────────────────────────────────────┘
```

## Prerequisites

- ACM hub cluster with managed clusters registered
- MTV operator installed on the hub
- Argo CD / OpenShift GitOps managing VM workloads via ApplicationSets
- Git repository with VM manifests organized by cluster:

```
clusters/
├── cluster1/
│   └── vm-web-server.yaml    # kind: VirtualMachine
├── cluster2/
│   └── vm-database.yaml
```

## Step-by-step

### Step 1 -- Prepare Argo CD for Migration

Choose **one** of the following to prevent Argo CD from interfering during migration:

**Option A:** Set the source VM's `spec.runStrategy: Manual` so Argo CD does not restart it after shutdown.

**Option B:** Disable automated sync on the ApplicationSet and add the skip-reconcile annotation:

- `spec.template.spec.syncPolicy.automated: {}`
- `argocd.argoproj.io/skip-reconcile: "true"`

<details>
<summary>Example ApplicationSet</summary>

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: helloworld
  namespace: openshift-gitops
spec:
  generators:
  - clusterDecisionResource:
      configMapRef: acm-placement
      labelSelector:
        matchLabels:
          cluster.open-cluster-management.io/placement: global-argocd
      requeueAfterSeconds: 30
  template:
    metadata:
      annotations:
        apps.open-cluster-management.io/ocm-managed-cluster: '{{name}}'
        argocd.argoproj.io/skip-reconcile: "true"
      labels:
        apps.open-cluster-management.io/pull-to-ocm-managed-cluster: "true"
      name: '{{name}}-helloworld'
    spec:
      destination:
        namespace: helloworld
        server: https://kubernetes.default.svc
      project: default
      source:
        path: applicationset
        repoURL: https://github.com/yiraeChristineKim/argo-test.git
        targetRevision: HEAD
      syncPolicy:
        automated: {}
```

</details>

### Step 2 -- Start the Migration

Commit the MTV `Plan` (Migration) CR to the hub cluster, or start the migration from the ACM/MTV Console.

### Step 3 -- Wait for Completion

Monitor the Plan until the VM migration completes:

```bash
oc get plan <plan-name> -n <namespace> -o jsonpath='{.status.vms[0].phase}'
# Expected: "Completed"
```

### Step 4 -- Verify Source VM is Stopped

Confirm the source VM has been shut down on the original cluster:

```bash
oc get vm <vm-name> -n <namespace> -o jsonpath='{.status.printableStatus}'
# Expected: "Stopped"
```

### Step 5 -- Open a PR for the Target VM

Place the target VM manifest in the target cluster's directory and open a PR:

```
clusters/
├── cluster1/
│   └── vm-web-server.yaml        # source (to be removed)
└── cluster2/
    └── vm-web-server.yaml        # target (added in this PR)
```

If the `vm_migration` checkbox is checked in the PR description, the [VM Migration Check](./example_github_action.yml) GitHub Action automatically verifies that the VM is running on the target cluster before the PR can merge.

### Step 6 -- (Optional) Delete the Source VM

After the PR is merged and the target VM is confirmed running, remove the source VM manifest from git to complete the cutover.

---

## GitHub Action: VM Migration Check

A CI workflow that validates VMs are running on target managed clusters before a PR can merge. See the full workflow file: [example_github_action.yml](./example_github_action.yml).

### How It Works

1. The PR author checks `[x] vm_migration` in the PR description
2. The workflow detects changed `VirtualMachine` YAML files in the PR
3. For each VM file, the **target cluster name** is inferred from the parent directory (e.g., `clusters/cluster2/vm.yaml` -> `cluster2`)
4. A ManagedClusterView is created on the hub to query the `VirtualMachineInstance` on the managed cluster
5. The workflow passes if `status.phase` is `Running`; otherwise it fails

### Required Secrets

Configure these in **Settings > Secrets and variables > Actions**:

| Secret | Description |
|--------|-------------|
| `HUB_API_URL` | Hub cluster API URL (e.g., `https://api.hub.example.com:6443`) |
| `HUB_USERNAME` | Username for hub cluster authentication |
| `HUB_PASSWORD` | Password for hub cluster authentication |

### PR Template

Add the following to `.github/PULL_REQUEST_TEMPLATE.md`:

```markdown
## VM Migration Validation
- [ ] vm_migration
```

### ManagedClusterView Example

The workflow creates a temporary `ManagedClusterView` like the following to check VM status on the managed cluster:

```yaml
apiVersion: view.open-cluster-management.io/v1beta1
kind: ManagedClusterView
metadata:
  name: check-vmi-my-vm
  namespace: cluster2          # target managed cluster name
spec:
  scope:
    apiGroup: kubevirt.io
    version: v1
    resource: virtualmachineinstances
    name: my-vm
    namespace: default
```

The result is read from `.status.result.status.phase` on the hub cluster.
