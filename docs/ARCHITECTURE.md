# Architecture

## System overview

MTV Integrations runs as a single Deployment in the `open-cluster-management` namespace on the ACM hub cluster. It exposes four independent runtime components behind one binary (`cmd/main.go`):

```
┌──────────────────────────────────────────────────────────────┐
│  mtv-integrations-controller  (single binary)                │
│                                                              │
│  ┌────────────────────────┐  ┌─────────────────────────────┐ │
│  │  ManagedCluster         │  │  Plan Reconciler (CCLM)     │ │
│  │  Reconciler             │  │  OwnerRef on NetworkMap/    │ │
│  │  (controller-runtime)   │  │  StorageMap for cclm Plans  │ │
│  └──────────┬─────────────┘  └──────────┬──────────────────┘ │
│             │                           │                    │
│  ┌──────────┴─────────────┐  ┌──────────┴──────────────────┐ │
│  │  Plan Validation        │  │  Migration Advisor API      │ │
│  │  Webhook (:9443/TLS)    │  │  (:8082/HTTP)               │ │
│  │  /validate-plan         │  │  /api/v1/migration-targets  │ │
│  └─────────────────────────┘  └─────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
        │              │              │                │
        ▼              ▼              ▼                ▼
   ManagedCluster  Forklift CRDs  K8s Admission   Thanos / ACM
   resources       (Plans, Maps)  API server      Search API
```

## Component details

### ManagedCluster Reconciler

**Trigger:** ManagedCluster labeled `acm/cnv-operator-install: "true"`.

**What it creates (all named `<cluster>-mtv`):**

1. **ManagedServiceAccount** — in the cluster's namespace on the hub; enables token rotation for secure hub-to-spoke communication.
2. **ClusterPermission** — grants `cluster-admin` ClusterRoleBinding to the service account on the managed cluster.
3. **Provider Secret** — in the `mtv-integrations` namespace; contains kubeconfig token, CA cert, and a `cacert` key for MTV compatibility.
4. **Forklift Provider CR** — registers the cluster as an OpenShift-type provider in the `mtv-integrations` namespace.

**Cleanup:** finalizer (`mtv-integrations.open-cluster-management.io/resource-cleanup`) ensures all four resources are removed when the label is removed or the cluster is deleted.

**Key design choice:** Provider and ClusterPermission resources use `dynamic.Interface` with hand-built `unstructured.Unstructured` payloads (see `controllers/payloads.go`) rather than generated typed clients, because these CRDs are external and may not be installed.

**Sequencing:** the Provider CR is only created after the Secret is ready (contains a valid token), ensuring authentication details are in place before MTV tries to use the provider.

### Plan Reconciler (CCLM ownership)

**Trigger:** Forklift Plan resources labeled `app.kubernetes.io/created-by: cclm`.

**Purpose:** when the CCLM (Cluster Lifecycle Manager) creates a Plan with its associated NetworkMap and StorageMap, this controller stamps an `OwnerReference` on the NetworkMap and StorageMap pointing back to the Plan. This ensures Kubernetes garbage-collects the maps when the Plan is deleted.

**Soft failure:** if Forklift CRDs are not installed on the hub, the controller logs a warning and starts a background watcher that retries when the CRDs become available, rather than failing fatally.

### Plan Validation Webhook

**Endpoint:** `/validate-plan` on the webhook server (port 9443, TLS).

**Operations:** intercepts `CREATE` and `UPDATE` of `forklift.konveyor.io/v1beta1` Plan resources.

**Authorization flow:**
1. Extract destination provider name (must end with `-mtv`) → derive managed cluster name.
2. Read `spec.targetNamespace` from the Plan.
3. Impersonate the requesting user via a dynamic client.
4. GET cluster-scoped `UserPermission` resources — by default checks `managedcluster:admin`, `kubevirt.io:admin`, and `kubevirt.io:edit` (from `clusterview.open-cluster-management.io/v1alpha1`).
5. Allow if **any** permission has a `status.bindings` entry for that cluster whose `namespaces` list includes `*` or the target namespace.
6. Deny with a descriptive error otherwise.

**Testing note:** standard Kubernetes rejects `:` in resource names, so e2e/kind tests set the `MTV_USERPERMISSION_NAMES` env var to override resource names with DNS-safe alternatives.

### Migration Advisor API

**Endpoint:** `/api/v1/migration-targets` (port 8082, plain HTTP — no TLS required).

**Purpose:** given a source VM (cluster, namespace, name), score all candidate ACM-managed clusters and recommend the best migration target.

**Data flow:**
1. **VM Fetcher** (`vm_fetcher.go`) — queries ACM Search API (GraphQL) to get the source VM's CPU, memory, and volumes.
2. **Observability Client** (`observability_client.go`) — queries Thanos for per-node CPU/memory allocatable and Ceph cluster metrics across all managed clusters.
3. **Search Client** (`search_client.go`) — queries ACM Search for StorageClass provisioners on clusters labeled `acm/cnv-operator-install=true`.
4. **Scorer** (`scorer.go`) — filters clusters that can't fit the VM, then scores remaining candidates on CPU availability, memory availability, and storage compatibility.
5. **Handler** (`handler.go`) — orchestrates the above, returns ranked candidates with a top recommendation.

**Caching:** cluster-wide data (node metrics, Ceph metrics, StorageClasses) is cached with a configurable TTL (default 30s) using `singleflight` to deduplicate concurrent refreshes.

**HTTP client:** `httpclient.go` builds an `*http.Client` that authenticates with the bearer token from the rest config and trusts both the cluster API server CA and the OpenShift service CA (from a mounted ConfigMap at `/var/run/secrets/service-ca/service-ca.crt`). This is required for in-cluster HTTPS services like `search-search-api` that are signed by the OpenShift Service CA.

**Endpoint discovery:** at startup, `cmd/main.go` auto-discovers ACM Search API and Thanos Query Frontend via OpenShift Routes (`search-api` in `open-cluster-management`, `rbac-query-proxy` in `open-cluster-management-observability`). Flags `--search-api-endpoint` and `--thanos-host` override this for local development.

## Addons (YAML-only)

Under `addons/`, two OCM AddOnTemplate manifests:

- **CNV Addon** (`cnv-addon/`) — installs KubeVirt HyperConverged operator on clusters labeled `acm/cnv-operator-install: "true"`. Uses OperatorPolicy for lifecycle management.
- **MTV Addon** (`mtv-addon/`) — installs the MTV operator in `openshift-mtv` on the hub. Enables UI plugin, validation, and volume populator features.

Both require ACM and the Policy addon. They are applied directly with `oc apply -f`, not managed by the Go controller.

## Key data flows

### Provider onboarding (ManagedCluster reconciler)

```
ManagedCluster (acm/cnv-operator-install=true label added)
  → Reconciler creates ManagedServiceAccount
  → MSAA controller creates ServiceAccount + token on spoke
  → Token appears in Secret on hub
  → Reconciler creates ClusterPermission (cluster-admin on spoke)
  → Reconciler creates Provider Secret (token + CA in mtv-integrations namespace)
  → Reconciler creates Forklift Provider CR
  → MTV can now use the cluster as a migration source/target
```

### CCLM Plan ownership (Plan reconciler)

```
CCLM creates Plan + NetworkMap + StorageMap (all labeled created-by=cclm)
  → Plan reconciler detects the Plan
  → Reads spec.map.network and spec.map.storage refs
  → Sets OwnerReference on NetworkMap and StorageMap → Plan
  → Kubernetes GC deletes maps when Plan is deleted
```

### Plan authorization (webhook)

```
User submits Plan CREATE/UPDATE
  → API server calls /validate-plan
  → Webhook extracts destination provider + target namespace
  → Webhook impersonates user, GETs UserPermission resources
  → Checks bindings for cluster + namespace match
  → Allow or Deny
```

### Migration target scoring (advisor)

```
GET /api/v1/migration-targets?cluster=X&namespace=Y&name=Z
  → Fetch source VM info from ACM Search API
  → Fetch node metrics from Thanos (all clusters)
  → Fetch Ceph metrics from Thanos
  → Fetch StorageClasses from ACM Search
  → Filter clusters that can't fit the VM
  → Score remaining clusters (CPU + memory + storage)
  → Return ranked candidates + recommendation
```
