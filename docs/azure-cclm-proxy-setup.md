# Cross-cluster live migration (CCLM) — Proxy datapath on Azure

This guide is for **operators and customers** running **Migration Toolkit for Virtualization (MTV)** with **live** migration between two **Azure** OpenShift clusters (for example ACM managed clusters). It uses KubeVirt **decentralized live migration** with the **Proxy** datapath and an OpenShift **Route** on the pod network. You do **not** need a shared L2 or Multus network between clusters.

Background: [Decentralized live migration — KubeVirt user guide](https://kubevirt.io/user-guide/compute/decentralized_live_migration/).

## Overview

| Traffic | Path |
|--------|------|
| **Control** | Source **virt-synchronization-controller** → peer cluster **Route hostname:443** (TLS passthrough) → target sync controller |
| **Data** (Proxy mode) | Source virt-handler → local sync controller → gRPC tunnel → peer sync → target virt-handler |

On **each spoke** you configure:

1. **KubeVirt** — feature gate **`CrossClusterMigrationProxy`** and **`decentralizedLiveMigrationDatapath: Proxy`**
2. **Service + Route** in **`openshift-cnv`** (labeled for endpoint discovery)
3. **CA trust** — exchange peer **`kubevirt-ca`** material via **`kubevirt-external-ca`**
4. **Source VM** — root disk StorageClass suitable for your live migration setup, explicit **CPU model** (not host-model)
5. **Hub** — MTV **Plan** (live), **StorageMap** with **`accessMode`**, providers for both clusters

Repeat steps 1–3 on **every** cluster that participates in CCLM.

## Prerequisites

- OpenShift Virtualization (**CNV**) **4.99+** on both spokes, **HyperConverged** **Available**
- Pod **`virt-synchronization-controller`** running in **`openshift-cnv`**
- **MTV 2.10+** on the **hub** (MTV 2.9 and earlier treat OpenShift Virtualization migrations as **cold** even when the Plan is live)
- On the **hub**, confirm the Forklift controller enables live migration for OpenShift Virtualization (for example **`feature_ocp_live_migration: "true"`** on **`ForkliftController`**) before running a live migration Plan
- MTV **Provider** CRs for source and destination clusters
- TCP **443** reachable from each spoke to the **other** spoke’s sync Route (Azure load balancer / firewall)
- **`cluster-admin`** on each spoke

On some **CNV 4.99** builds, HyperConverged does not yet expose Proxy datapath in the UI. You configure the **KubeVirt** CR directly (below). The **Route and Service are not created by HCO**; you apply them yourself.

---

## Step 1 — Configure KubeVirt for Proxy (each spoke)

Log in to the cluster (`oc login` …). The KubeVirt object name is usually **`kubevirt-kubevirt-hyperconverged`** in namespace **`openshift-cnv`**.

### 1a. Optional: pause HCO reconciliation (CNV 4.99)

If your changes to **KubeVirt** are reverted immediately, scale down reconcilers **temporarily** while you test:

```bash
oc scale deployment/hco-operator deployment/virt-platform-autopilot -n openshift-cnv --replicas=0
```

When you are done testing, scale them back to **1**. Restoring HCO may revert manual **KubeVirt** fields on some builds.

### 1b. Patch the KubeVirt CR

Add the **`CrossClusterMigrationProxy`** feature gate (if missing) and set the migration datapath to **`Proxy`**.

Inspect current spec:

```bash
oc get kubevirt kubevirt-kubevirt-hyperconverged -n openshift-cnv \
  -o jsonpath='{.spec.configuration.developerConfiguration.featureGates}{"\n"}{.spec.configuration.migrations}{"\n"}'
```

Merge patch example (adjust if your gate list already contains **`CrossClusterMigrationProxy`**):

If **`spec.configuration.migrations.crossClusterNetwork`** or **`spec.configuration.migrations.network`** is already set, traffic is bound to that dedicated migration network instead of the pod network — the OpenShift **Route** in Step 2 will not reach the sync listener. **Do not** use this Proxy + Route flow until both fields are unset (remove them from **HyperConverged** / **KubeVirt** if a prior administrator configured a Multus migration network).

```bash
KV=kubevirt-kubevirt-hyperconverged
NS=openshift-cnv

EXISTING_CCN=$(oc get kubevirt "$KV" -n "$NS" -o json | \
  jq -r '.spec.configuration.migrations.crossClusterNetwork // empty')
EXISTING_NET=$(oc get kubevirt "$KV" -n "$NS" -o json | \
  jq -r '.spec.configuration.migrations.network // empty')
if [[ -n "$EXISTING_CCN" ]] || [[ -n "$EXISTING_NET" ]]; then
  echo "ERROR: crossClusterNetwork or migrations.network is set; unset both before Proxy + Route setup." >&2
  echo "  crossClusterNetwork=${EXISTING_CCN:-<empty>}"
  echo "  migrations.network=${EXISTING_NET:-<empty>}"
  exit 1
fi

GATES=$(oc get kubevirt "$KV" -n "$NS" -o json | \
  jq '[.spec.configuration.developerConfiguration.featureGates[]?, "CrossClusterMigrationProxy"] | unique')

MIGRATIONS=$(oc get kubevirt "$KV" -n "$NS" -o json | \
  jq '.spec.configuration.migrations // {} | . + {"decentralizedLiveMigrationDatapath": "Proxy"}')

oc patch kubevirt "$KV" -n "$NS" --type=merge -p "$(jq -n \
  --argjson gates "$GATES" \
  --argjson migrations "$MIGRATIONS" \
  '{spec: {configuration: {developerConfiguration: {featureGates: $gates}, migrations: $migrations}}}')"
```

Equivalent intent in YAML terms:

```yaml
# kubevirt.io/v1 — fragment of spec.configuration
developerConfiguration:
  featureGates:
    - DecentralizedLiveMigration          # usually already present
    - CrossClusterMigrationProxy          # required for Proxy datapath
migrations:
  decentralizedLiveMigrationDatapath: Proxy
```

### 1c. Verify KubeVirt

After **virt-operator** reconciles (up to a few minutes):

```bash
oc get kubevirt kubevirt-kubevirt-hyperconverged -n openshift-cnv \
  -o jsonpath='datapath={.spec.configuration.migrations.decentralizedLiveMigrationDatapath}{"\n"}sync={.status.synchronizationAddresses}{"\n"}endpoint={.status.conditions[?(@.type=="SynchronizationEndpoint")].reason}{"\n"}'
```

**Expected result:**

```text
datapath=Proxy
sync=
endpoint=
```

`datapath` must be **`Proxy`**. Empty `sync` / `endpoint` is normal until Step 2. If `datapath` is empty or reverts away from `Proxy`, HCO likely overwrote the KubeVirt CR — see Step 1a.

---

## Step 2 — Service and Route (each spoke)

On **each** cluster, apply the Service and Route below in **`openshift-cnv`**. HyperConverged does **not** create them for you.

**virt-operator** looks for objects labeled **`kubevirt.io/synchronization-endpoint=true`** (it uses **at most one** Route, Ingress, or Service). When it finds one, it writes that host:port into the **KubeVirt** CR field **`.status.synchronizationAddresses`** (on `kubevirt-kubevirt-hyperconverged`). That address is how peers reach this cluster’s sync controller.

**Important:** On the **Route**, **`spec.port.targetPort`** must be the **Service port name** (`grpc`), not the container port name (`sync`). Wrong targetPort causes TLS handshake **EOF** and **`DecentralizedLiveMigrationFailure`**.

```yaml
apiVersion: v1
kind: Service
metadata:
  name: virt-synchronization-controller
  namespace: openshift-cnv
  labels:
    kubevirt.io/synchronization-endpoint: "true"
spec:
  selector:
    kubevirt.io: virt-synchronization-controller
  ports:
    - name: grpc
      port: 9185
      protocol: TCP
      targetPort: sync
  type: ClusterIP
---
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: virt-synchronization-endpoint
  namespace: openshift-cnv
  labels:
    kubevirt.io/synchronization-endpoint: "true"
spec:
  to:
    kind: Service
    name: virt-synchronization-controller
  port:
    targetPort: grpc
  tls:
    termination: passthrough
    insecureEdgeTerminationPolicy: None
```

Save as a file and apply:

From the **repository root** (paths below assume that working directory):

```bash
oc apply -f docs/examples/cross-cluster-sync-proxy.yaml
```

Verify the Route:

```bash
oc get route virt-synchronization-endpoint -n openshift-cnv \
  -o jsonpath='host={.spec.host} targetPort={.spec.port.targetPort}{"\n"}'
```

**Expected result:** `host=virt-synchronization-endpoint-openshift-cnv.apps.<cluster>.<domain>` and `targetPort=grpc`.

Confirm virt-operator discovered the endpoint (may take a few minutes):

```bash
oc get kubevirt kubevirt-kubevirt-hyperconverged -n openshift-cnv \
  -o jsonpath='datapath={.spec.configuration.migrations.decentralizedLiveMigrationDatapath}{"\n"}sync={.status.synchronizationAddresses}{"\n"}endpoint={.status.conditions[?(@.type=="SynchronizationEndpoint")].reason}{"\n"}'
```

**Expected result:**

```text
datapath=Proxy
sync=[virt-synchronization-endpoint-openshift-cnv.apps.<cluster>.<domain>:443]
endpoint=SynchronizationEndpointFound
```

If `endpoint` is not `SynchronizationEndpointFound`, check Route/Service labels (`kubevirt.io/synchronization-endpoint=true`) and Route `targetPort` (`grpc`).

From a **pod on the peer spoke** (not your workstation), confirm TLS reaches the peer sync Route (certificate validation should show the sync service identity). **`oc login`** only configures your kubeconfig; it does **not** run the check on the cluster. On the **peer** cluster, run a short-lived pod with `curl`, for example:

```bash
PEER_ROUTE_HOST=virt-synchronization-endpoint-openshift-cnv.apps.<peer-cluster>

oc run tls-check --rm -i --restart=Never --image=registry.redhat.io/ubi9/ubi-minimal:latest -n default -- \
  curl -vk "https://${PEER_ROUTE_HOST}" 2>&1 | head -20
```

Replace **`<peer-cluster>`** and adjust **`-n default`** if your namespace policy differs. For **`openssl s_client`**, use an image that includes OpenSSL instead of `ubi-minimal`.

---

## Step 3 — Exchange KubeVirt CA (between spokes)

Each cluster has its own KubeVirt CA in ConfigMap **`kubevirt-ca`**. Cross-cluster mTLS requires each cluster to trust the peer’s CA.

Official flow ([user guide](https://github.com/kubevirt/user-guide/blob/main/docs/compute/decentralized_live_migration.md#configuring-kubevirt-ca-with-cross-cluster-live-migration)):

1. On **cluster B**, copy **`data.ca-bundle`** from **`kubevirt-ca`** in **`openshift-cnv`**.
2. On **cluster A**, paste that bundle into ConfigMap **`kubevirt-external-ca`** (same namespace), key **`ca-bundle`**.
3. Repeat **A → B** symmetrically.

On **cluster B**, extract the CA bundle to a file:

```bash
NS=openshift-cnv

oc get configmap kubevirt-ca -n "$NS" -o jsonpath='{.data.ca-bundle}' > peer-ca.pem
```

On **cluster A**, create (or update) **`kubevirt-external-ca`** with that file:

```bash
NS=openshift-cnv

oc create configmap kubevirt-external-ca -n "$NS" \
  --from-file=ca-bundle=peer-ca.pem \
  --dry-run=client -o yaml | oc apply -f -
```

If the ConfigMap already exists:

```bash
oc patch configmap kubevirt-external-ca -n openshift-cnv --type=merge \
  -p "$(jq -n --rawfile b peer-ca.pem '{data: {"ca-bundle": $b}}')"
```

Wait until **`kubevirt-external-ca`** **`ca-bundle`** is empty again and **`kubevirt-ca`** on both clusters contains both issuers. Re-exchange when CAs rotate (often every ~2 days by default).

**Scale limit:** every peer CA is merged into **`kubevirt-ca`**. A ConfigMap is capped at **1 MiB**. Customers who mesh many spokes (for example ~100 CCLM peers per cluster), especially with CA rotation retaining older certs, can hit updates that fail with size / “too large” errors. Prefer small peer sets (pairs or limited hubs), not a full mesh of every cluster into every `kubevirt-ca`.

---

## Step 4 — Azure storage and source VM

### Live migration disk

Pick a StorageClass that fits your migration mode and platform (default SC can be anything).

If you use **`azurefile-csi`**, do **not** create a **blank** DataVolume on it (CDI fails with `chmod /data/disk.img: operation not permitted`). **Clone** from a **Bound** RHEL (or other) image PVC instead.

### CPU model

On the source **VirtualMachine** CR, set an explicit CPU model both clusters can migrate (avoid implicit **host-model** when Azure SKUs differ):

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
spec:
  template:
    spec:
      domain:
        cpu:
          cores: 2
          model: Broadwell
```

---

## Step 5 — MTV on the hub

### Providers

Create **Provider** CRs in **`openshift-mtv`** for source and destination OpenShift Virtualization clusters (for example **`cnv-1`**, **`cnv-2`**).

### StorageMap

On Azure ACM spokes you typically map **`azurefile-csi`** (RWX live-migration disks) and **`managed-csi`** (standard RWO disks) **one-to-one** between source and destination providers.

#### Simple map (storage class only)

This is the usual starting point — only **`source.name`** and **`destination.storageClass`**. Provider references can be set in the MTV UI (full **`apiVersion` / `kind` / `uid`**) or with short **`name` + `namespace`** as below.

```yaml
apiVersion: forklift.konveyor.io/v1beta1
kind: StorageMap
metadata:
  name: cclm-azurefile
  namespace: openshift-mtv
spec:
  map:
    - source:
        name: azurefile-csi
      destination:
        storageClass: azurefile-csi
    - source:
        name: managed-csi
      destination:
        storageClass: managed-csi
  provider:
    source:
      name: cnv-1
      namespace: openshift-mtv
    destination:
      name: cnv-2
      namespace: openshift-mtv
```

When the plan is created from the console, **`spec.provider`** may also show resolved references (for example **`uid`** on each provider). That is equivalent to the short form above.

```bash
oc apply -f storagemap.yaml
```

#### Extended map (add `accessMode` if PrepareTarget fails)

For some **live** plans with **RWX** root disks, MTV **PrepareTarget** fails unless the destination entry includes **`accessMode`** (and often **`volumeMode`**). If you see *at least 1 access mode is required*, patch or replace the map with:

```yaml
  map:
    - source:
        name: azurefile-csi
      destination:
        storageClass: azurefile-csi
        accessMode: ReadWriteMany
        volumeMode: Filesystem
    - source:
        name: managed-csi
      destination:
        storageClass: managed-csi
        accessMode: ReadWriteOnce
        volumeMode: Filesystem
```

(Full manifest: **`docs/examples/mtv-storagemap-cclm.yaml`**. Simple manifest: **`docs/examples/mtv-storagemap-cclm-simple.yaml`**.)

Adjust **`metadata.name`** and provider **`cnv-1` / `cnv-2`** to match your **Provider** CR names.

### Plan

Create a **Plan** with:

- **`spec.type: live`**
- **`spec.migrateSharedDisks: true`**
- Source provider → destination provider
- **`spec.map.storage`** → StorageMap above
- **`spec.vms`** → VM **`cclm-rwx-test`** in **`default`** on the source cluster

Run the plan. During migration:

| Cluster | Command |
|---------|---------|
| Source | `oc get vmim,vmi -n default -w` |
| Target | `oc get vmim,vmi,pods -n default -w` |

If the hub reports **WaitForStateTransfer** or *check VMIM status*, run **`oc describe vmim <name> -n default`** on **both** spokes. MTV creates VMIM names like **`forklift-xxxxx`**, not the VM name.

---

## Step 6 — After a failed migration

1. On the **hub**, delete or reset the failed **Migration** CR before re-running the plan.
2. On **both spokes**, delete stale **`VirtualMachineInstanceMigration`** objects. If a VMIM is stuck **Terminating**, remove finalizer **`kubevirt.io/migrationJobFinalize`** only after the migration is truly dead.
3. On the **target** cluster, remove failed target VM/VMI/PVC if **`deleteVmOnFailMigration`** did not finish cleanup.
4. Keep the **source** **`cclm-rwx-test`** on the source cluster unless you intend to delete it.

---

## Troubleshooting

| Symptom | Likely cause | What to check |
|--------|----------------|---------------|
| `authentication handshake failed: EOF` | Route **`targetPort`** not **`grpc`** | Route **`spec.port.targetPort`**, Service port name **`grpc`** |
| `unable to locate VMI for migrationID` | Target VMI removed or never scheduled | Target events, **`NoSuitableNodesForHostModelMigration`**, VMIM messages |
| `NoSuitableNodesForHostModelMigration` | **host-model** or CPU feature mismatch | VM **`cpu.model: Broadwell`**, restart VMI |
| `destination volume is smaller than the source` | RWO live copy / size mismatch | Use **RWX** CCLM VM; increase target size if doing disk copy |
| PrepareTarget / accessModes | Simple StorageMap missing **`accessMode`** | Add **`destination.accessMode`** on **`azurefile-csi`** → **ReadWriteMany** (extended map) |
| CPU cores vs instance type | **`spec.instancetype`** + **`domain.cpu.cores`** | Remove instance type or use instance type only for sizing |
| CDI chmod on azurefile | Blank DV on Azure Files | Clone from Bound image PVC on **`managed-csi`** |
| Proxy settings reverted | HCO reconciled **KubeVirt** | Re-apply Step 1; consider pausing HCO during tests on 4.99 |

---

## Reference manifests in this repository

Copy-paste starting points (edit names for your environment):

| File | Content |
|------|---------|
| [`docs/examples/cross-cluster-sync-proxy.yaml`](examples/cross-cluster-sync-proxy.yaml) | Service + Route (Step 2) |
| [`docs/examples/cclm-vm-rwx-azurefile.yaml`](examples/cclm-vm-rwx-azurefile.yaml) | DataVolume + VirtualMachine (Step 4) |
| [`docs/examples/mtv-storagemap-cclm-simple.yaml`](examples/mtv-storagemap-cclm-simple.yaml) | StorageMap — **azurefile-csi** + **managed-csi** (class only) |
| [`docs/examples/mtv-storagemap-cclm.yaml`](examples/mtv-storagemap-cclm.yaml) | StorageMap with **`accessMode`** / **`volumeMode`** if PrepareTarget requires it |

## External links

- [Decentralized live migration](https://kubevirt.io/user-guide/compute/decentralized_live_migration/) (KubeVirt user guide)
- [Decentralized live migration — KubeVirt CA](https://github.com/kubevirt/user-guide/blob/main/docs/compute/decentralized_live_migration.md#configuring-kubevirt-ca-with-cross-cluster-live-migration)
