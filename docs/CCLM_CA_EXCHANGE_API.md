# Proxy CCLM — automatic KubeVirt CA exchange API

Jira: [ACM-50186](https://redhat.atlassian.net/browse/ACM-50186)

Related: [Architecture — CA exchange](ARCHITECTURE.md#proxy-cclm-ca-exchange-api), [Azure proxy setup — Step 3](azure-cclm-proxy-setup.md#step-3--exchange-kubevirt-ca-between-spokes), [proxy-cclm addon](../addons/README.md#proxy-cclm-addon).

## Endpoint

`POST /api/v1/proxy-cclm/ca-exchange` on the **migration advisor HTTP server** (default port **`8082`**, same listener as `/api/v1/migration-targets`). Override with manager flag `--advisor-addr`.

### `GET /api/v1/proxy-cclm/health`

Unauthenticated readiness check for the CA exchange feature on the same listener (not the exchange POST itself). Verifies the hub **`mtv-integrations`** namespace exists and the manager can **get** `coordination.k8s.io/leases` there (proxy-cclm lease RBAC). Does not call spokes or Thanos.

- **`200`** / body `ok` when checks pass
- **`503`** when the namespace is missing or lease API/RBAC fails
- **`405`** for non-GET methods

`GET /health` on the advisor still checks **Thanos** for migration targets only.

The advisor HTTP server uses a **90s** write timeout so verification polling (up to **30s**) and spoke/hub I/O can complete. Callers should use a similar client timeout for exchange requests that perform writes.

## Prerequisites

Complete these **before** calling the API (checks run in this order after auth):

| Requirement | Notes |
|-------------|--------|
| **`acm/proxy-cclm=true`** on **both** hub `ManagedCluster` objects | Proxy CCLM addon placement ([ACM-50184](https://redhat.atlassian.net/browse/ACM-50184)). Deploys enforcing **ConfigurationPolicy** that merges **kubevirt-ca** / **kubevirt-external-ca** rules into **`kubevirt.io:admin`** (CNV operator **ClusterRole** on the spoke). If **either** cluster lacks the label (or the value is not exactly `true`), the API returns **HTTP 400** / **`PROXY_CCLM_NOT_ENABLED`** — see [Missing `acm/proxy-cclm`](#missing-acmproxy-cclm-label). |
| **MulticlusterRoleAssignment on both `ManagedCluster`s** | **Required** before the exchange can succeed. On the hub, create MRA so the caller gets **`kubevirt.io:admin`** on **each** spoke named in the request (`clusterA`, `clusterB`): **`targetNamespaces: [openshift-cnv]`** and **Placement** resolving to those clusters. Without it, the managed-cluster API proxy returns **403** for ConfigMap access. Details: [MulticlusterRoleAssignment on managed clusters](#multiclusterroleassignment-on-managed-clusters). |
| Hub **`mtv-integrations/<cluster>-mtv`** or **`ManagedCluster` client URL** | Used only for spoke **API URL** and **CA** (`url`, `cacert`); the MTV provider **token in the Secret is not used** for ConfigMap access. |
| KubeVirt on each spoke | ConfigMaps **`kubevirt-ca`** (non-empty `ca-bundle`) and pre-existing **`kubevirt-external-ca`** in **`openshift-cnv`** or the `kubeVirtNamespace` you pass on the request. The API does **not** create `kubevirt-external-ca`. Trust is between KubeVirt peers, not MTV/Forklift. |
| **ACM RBAC for the API caller (hub preflight)** | See [Authorization](#authorization). Hub **`UserPermission`** **`acm-vm-fleet:admin`** and **`kubevirt.io:admin`** (bindings for **both** clusters) — **403** if missing. Spoke API may still return **403** if cluster RBAC is out of sync with UserPermission. |

Label both clusters on the hub:

```bash
oc label managedcluster cnv-1 acm/proxy-cclm=true --overwrite
oc label managedcluster cnv-2 acm/proxy-cclm=true --overwrite
```

## Authorization

Every request needs **`Authorization: Bearer <token>`** (OpenShift user or service account token on the hub). The controller checks the **caller's** identity (not the MTV controller ServiceAccount).

The migration advisor validates **hub** **`UserPermission`** (**GET** as the caller, `clusterview.open-cluster-management.io/v1alpha1`). **Both** must pass before preflight or spoke calls:

### Hub — `acm-vm-fleet:admin`

**`status.bindings`** must include **`local-cluster`** with namespace scope **`*`**.

```bash
oc get userpermission acm-vm-fleet:admin -o jsonpath='{.status.bindings}' && echo
```

### Spokes (source and destination) — `kubevirt.io:admin`

**`status.bindings`** must include **both** `clusterA` and `clusterB` from the request (each binding’s `cluster` field matches the ManagedCluster name). Namespace scope is typically **`openshift-cnv`** or **`*`** from MRA.

```bash
oc get userpermission kubevirt.io:admin -o yaml
```

If either check fails: **HTTP 403** — `forbidden: requires hub acm-vm-fleet:admin (local-cluster with namespaces *) and kubevirt.io:admin on both clusters`.

### Spoke API (after hub preflight)

Spoke **`get` / `watch` / `patch` / `update`** use the **same bearer token** through the **managed-cluster proxy**. Missing **effective** RBAC on the cluster still returns **403** (`FORBIDDEN` with remediation text) or **502**.

On each **`acm/proxy-cclm=true`** spoke, the **proxy-cclm** addon’s enforcing **ConfigurationPolicy** merges the CA exchange ConfigMap rules into **`kubevirt.io:admin`**. That **ClusterRole** is **created by the CNV operator** on the spoke (not by mtv-integrations). Grant callers **`kubevirt.io:admin`** in **`openshift-cnv`** via MRA (below).

### MulticlusterRoleAssignment on managed clusters

You need **[MulticlusterRoleAssignment](https://github.com/stolostron/multicluster-role-assignment)** on the **hub** that propagates RBAC to **both** `ManagedCluster` resources in the exchange request:

1. **Hub MRA** (or existing binding) — **`acm-vm-fleet:admin`** for the caller on **`local-cluster`** so they may call the API (hub `UserPermission` check).
2. **Spoke MRA** — **`kubevirt.io:admin`** with **`targetNamespaces: [openshift-cnv]`** (or your `kubeVirtNamespace`) and **Placement** that selects **every** `ManagedCluster` you pass as **`clusterA`** and **`clusterB`**. The fine-grained RBAC addon materializes namespace-scoped bindings **on each selected managed cluster**; that is what the cluster proxy enforces.

Confirm placements cover both cluster names:

```bash
oc get placementdecisions -A -o json | jq -r '.items[] | select(.status.decisions[]? .clusterName=="cnv-1" or .status.decisions[]? .clusterName=="cnv-2") | .metadata.namespace + "/" + .metadata.name'
```

Replace `cnv-1` / `cnv-2` with your **`ManagedCluster` metadata.name** values.

### MulticlusterRoleAssignment (`kubevirt.io:admin`)

Fleet operators grant **`acm-vm-fleet:admin`** (hub) and spoke **`kubevirt.io:admin`** through **[MulticlusterRoleAssignment](https://github.com/stolostron/multicluster-role-assignment)** in **two places**:

| Where | Purpose | Placement / scope |
|-------|---------|-------------------|
| **Hub (`local-cluster`)** | Caller may invoke the exchange API | MRA with **`clusterRole: acm-vm-fleet:admin`** and a placement that includes the **hub**; binding must surface as **`UserPermission`** on **`local-cluster`** with **`namespaces: *`** |
| **Each `ManagedCluster` in the exchange** (`clusterA`, `clusterB`) | Caller may read/patch ConfigMaps on that spoke via the proxy | **Required** spoke MRA: **`clusterRole: kubevirt.io:admin`**, **`targetNamespaces: [openshift-cnv]`**, **`clusterSelection.placements`** resolving to **both** managed cluster names |

On **`acm/proxy-cclm=true`** clusters, the proxy-cclm addon **ConfigurationPolicy** ensures **`kubevirt.io:admin`** includes these **named ConfigMap** rules (CNV installs the ClusterRole; the policy **merges** rules):

| ConfigMap | Verbs |
|-----------|-------|
| `kubevirt-ca` | get, list, watch |
| `kubevirt-external-ca` | get, list, watch, update, patch |

If a spoke is missing from the placement, lacks **`acm/proxy-cclm=true`** (no CA rules on **`kubevirt.io:admin`**), or the subject lacks **`kubevirt.io:admin`** in **`openshift-cnv`**, the hub accepts the request (fleet `UserPermission` OK) but the call fails on that spoke with **403/502**.

Use a **Placement** (and **PlacementDecision**) that selects **all CNV/proxy-cclm spokes** you will exchange, or separate placements covering **clusterA** and **clusterB**. The placement must resolve to **every** `ManagedCluster` name you pass in the JSON body.

### Spoke namespace: `openshift-cnv`

KubeVirt stores **`kubevirt-ca`** and **`kubevirt-external-ca`** in **`openshift-cnv`** by default (override on the API with `kubeVirtNamespace` if your cluster uses a different namespace). The spoke MRA above must use **`targetNamespaces`** listing that same namespace (for example `[openshift-cnv]`), not a cluster-wide `ClusterRoleBinding`.

Example **`MulticlusterRoleAssignment`** on the hub (spokes — adjust `subject` and placement names):

```yaml
apiVersion: rbac.open-cluster-management.io/v1beta1
kind: MulticlusterRoleAssignment
metadata:
  name: kubevirt-admin-cnv-proxy-cclm
  namespace: open-cluster-management-global-set
spec:
  subject:
    kind: Group   # use User for a single OpenShift user
    name: <your-group-or-user>
    apiGroup: rbac.authorization.k8s.io
  roleAssignments:
    - name: kubevirt-admin-openshift-cnv-on-exchange-spokes
      clusterRole: kubevirt.io:admin
      targetNamespaces:
        - openshift-cnv
      clusterSelection:
        type: placements
        placements:
          # Placement must select BOTH clusterA and clusterB (and any other spokes you exchange).
          - name: <placement-for-source-and-target-clusters>
            namespace: open-cluster-management-global-set
```

Add a **separate** hub MRA (or existing fleet-admin binding) so the same subject has **`UserPermission`** **`acm-vm-fleet:admin`** on **`local-cluster`** with **`namespaces: *`**.

## Request

```json
{
  "clusterA": "cnv-1",
  "clusterB": "cnv-2",
  "kubeVirtNamespace": "openshift-cnv"
}
```

Verification polling uses a **fixed 30s** timeout (not configurable on the request). Hub exchange **leases** use a **120s** TTL so the lock outlives verification under normal operation.

`clusterA` and `clusterB` are hub `ManagedCluster` **metadata.name** values (not MTV provider suffixes).

### Example call

```bash
ADVISOR="${ADVISOR:-http://mtv-advisor-service.open-cluster-management.svc:8082}"
# Token for a user or SA with acm-vm-fleet:admin (local-cluster, namespaces *); spoke ConfigMap RBAC via MRA on clusters
TOKEN="$(oc whoami -t)"

curl -sS -X POST "${ADVISOR}/api/v1/proxy-cclm/ca-exchange" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"clusterA":"cnv-1","clusterB":"cnv-2"}'
```

## Processing flow

1. Validate JSON and Bearer token.
2. Authorize caller for both clusters.
3. **Preflight:** GET each `ManagedCluster`; require `acm/proxy-cclm=true`.
4. Build spoke Kubernetes clients with the **caller's bearer token** (API URL/CA from hub Secret or ManagedCluster).
5. Read `kubevirt-ca`, decide bidirectional trust, acquire hub Leases, write `kubevirt-external-ca` when needed, poll until `kubevirt-ca` reflects trust (or return timeout).

## Success (already trusted)

```json
{
  "status": "success",
  "code": "CA_ALREADY_TRUSTED",
  "message": "Both clusters already trust each other's kubevirt-ca bundles",
  "clusterA": { "cluster": "cnv-1", "caVerified": true, "certificateCount": 3 },
  "clusterB": { "cluster": "cnv-2", "caVerified": true, "certificateCount": 3 }
}
```

## Success (exchange performed)

```json
{
  "status": "success",
  "clusterA": { "cluster": "cnv-1", "caVerified": true, "certificateCount": 4, "writePerformed": true },
  "clusterB": { "cluster": "cnv-2", "caVerified": true, "certificateCount": 4, "writePerformed": true },
  "warnings": []
}
```

## Missing `acm/proxy-cclm` label {#missing-acmproxy-cclm-label}

After auth (HTTP **403** if the caller lacks fleet RBAC), preflight loads each hub **`ManagedCluster`** in request order: **`clusterA`**, then **`clusterB`**. The **first** cluster that fails the label check ends the request; the other cluster is not evaluated in that response.

A cluster fails when **`metadata.labels["acm/proxy-cclm"]`** is missing, empty, or **not** exactly **`true`** (for example `false`, `True`, or no label after `oc label … acm/proxy-cclm-`).

| Situation | HTTP | JSON `code` | JSON `status` |
|-----------|------|-------------|---------------|
| **`clusterA`** not labeled (but exists) | **400** | **`PROXY_CCLM_NOT_ENABLED`** | **`failed`** |
| **`clusterB`** not labeled ( **`clusterA`** OK) | **400** | **`PROXY_CCLM_NOT_ENABLED`** | **`failed`** |
| Either name not a `ManagedCluster` | **400** | **`INVALID_REQUEST`** | **`failed`** |
| Hub cannot GET `ManagedCluster` | **502** | **`CLUSTER_LOOKUP_FAILED`** | **`failed`** |

Response body is always JSON (`Content-Type: application/json`) with `status`, `code`, and `message`. No spoke API calls and no ConfigMap changes occur on **`PROXY_CCLM_NOT_ENABLED`**.

### Example — label missing on `clusterB` only

Request: `{ "clusterA": "cnv-1", "clusterB": "cnv-2" }` where **`cnv-1`** has **`acm/proxy-cclm=true`** and **`cnv-2`** does not.

```http
HTTP/1.1 400 Bad Request
Content-Type: application/json
```

```json
{
  "status": "failed",
  "code": "PROXY_CCLM_NOT_ENABLED",
  "message": "cluster \"cnv-2\" must be labeled acm/proxy-cclm=true before kubevirt-ca exchange (deploy the proxy-cclm addon)"
}
```

### Example — label missing on `clusterA` only

Same shape; **`message`** names **`cnv-1`** instead:

```json
{
  "status": "failed",
  "code": "PROXY_CCLM_NOT_ENABLED",
  "message": "cluster \"cnv-1\" must be labeled acm/proxy-cclm=true before kubevirt-ca exchange (deploy the proxy-cclm addon)"
}
```

### Example — both clusters unlabeled

Only **`clusterA`** appears in the response (first failure in the loop):

```json
{
  "status": "failed",
  "code": "PROXY_CCLM_NOT_ENABLED",
  "message": "cluster \"cnv-1\" must be labeled acm/proxy-cclm=true before kubevirt-ca exchange (deploy the proxy-cclm addon)"
}
```

Fix: label **both** managed clusters on the hub, then retry:

```bash
oc label managedcluster cnv-1 acm/proxy-cclm=true --overwrite
oc label managedcluster cnv-2 acm/proxy-cclm=true --overwrite
```

## Conflict — exchange in progress

HTTP `409`

```json
{
  "status": "failed",
  "code": "CA_EXCHANGE_IN_PROGRESS",
  "message": "The kubevirt-external-ca ConfigMap is currently in use or contains pending CA data. Please wait and try again."
}
```

## Near-limit warning (still success)

```json
{
  "status": "success",
  "warnings": [
    {
      "cluster": "cnv-1",
      "code": "KUBEVIRT_CA_NEAR_LIMIT",
      "message": "The kubevirt-ca ConfigMap contains 46 certificates and is approaching the KubeVirt CA bundle limit. Review the bundle and follow the supported KubeVirt cleanup procedure."
    }
  ]
}
```

## Spoke access

The controller calls each spoke’s **Kubernetes API as the API caller** (`Authorization: Bearer` from the POST). Connection target comes from **`ManagedCluster.spec.managedClusterClientConfigs`** and/or **`mtv-integrations/<cluster>-mtv`** (`url`, `cacert` only). **The provider Secret token is not used** for `kubevirt-ca` / `kubevirt-external-ca` operations.

The caller must have spoke RBAC to the ConfigMaps. Spoke **403** returns **`FORBIDDEN`** with remediation text (kubevirt.io:admin MRA, UserPermission checks); other spoke API failures return **`CLUSTER_UNREACHABLE`** / 502. **Forklift `Provider` CRs** are unrelated.

Spoke API URLs must use **`https`**. On the spoke the implementation **GET**s `kubevirt-ca` and **PATCH**es existing `kubevirt-external-ca` only (no create):

- `ConfigMap/kubevirt-ca` (read)
- `ConfigMap/kubevirt-external-ca` (patch `ca-bundle`)

Default namespace is `openshift-cnv` (override with `kubeVirtNamespace` or controller env `MTV_KUBEVIRT_NAMESPACE`).

## Concurrency

Hub `coordination.k8s.io/Lease` objects in **`mtv-integrations`** (`proxy-cclm-ca-exchange-<cluster>`) serialize exchanges per cluster across API replicas. The manager ServiceAccount needs a **namespaced Role** on leases in `mtv-integrations` (not cluster-wide). ConfigMap updates use Kubernetes resourceVersion optimistic concurrency.

## Error codes

| Code | HTTP | Meaning |
|------|------|---------|
| `UNAUTHORIZED` | 401 | Missing or invalid `Authorization: Bearer` header (`status: failed`, `Content-Type: application/json`) |
| `FORBIDDEN` | 403 | **Hub:** caller lacks `acm-vm-fleet:admin` and/or `kubevirt.io:admin` `UserPermission` for both clusters. **Spoke:** managed-cluster API denied ConfigMap access (remediation text references MRA, `acm/proxy-cclm=true`, and UserPermission checks) |
| `INVALID_REQUEST` | 400 | Missing or invalid cluster names, or ManagedCluster not found |
| `PROXY_CCLM_NOT_ENABLED` | 400 | ManagedCluster missing `acm/proxy-cclm=true` |
| `KUBEVIRT_EXTERNAL_CA_NOT_FOUND` | 400 | `kubevirt-external-ca` ConfigMap missing on a spoke that needs a write |
| `CLUSTER_LOOKUP_FAILED` | 502 | Hub could not read ManagedCluster |
| `CA_ALREADY_TRUSTED` | 200 | No write required |
| `CA_EXCHANGE_IN_PROGRESS` | 409 | Lock held or `kubevirt-external-ca` pending |
| `CLUSTER_UNREACHABLE` | 502 | Secret, API, or ConfigMap failure (non-RBAC; does not include spoke **403** — see `FORBIDDEN`) |
| `VERIFY_TIMEOUT` | 504 | Trust not observed in `kubevirt-ca` in time |
| `VERIFY_PARTIAL` | 504 | One cluster verified, the other did not |
| `CA_ROTATED_DURING_EXCHANGE` | 409 | Source `kubevirt-ca` changed during operation |
| `KUBEVIRT_CA_NEAR_LIMIT` | 200 (warning) | More than 45 certs in `kubevirt-ca` |

KubeVirt operator owns merge, expiry, and cleanup of `kubevirt-ca`. ACM only writes `kubevirt-external-ca`.

### Spoke RBAC denied (`FORBIDDEN` / 403)

When the caller’s bearer token cannot **get**, **patch**, or **update** `kubevirt-ca` / `kubevirt-external-ca` on a spoke, the JSON `message` includes the failing **`ManagedCluster`** name and asks you to:

1. Assign **`kubevirt.io:admin`** via **MulticlusterRoleAssignment** in **`openshift-cnv`** on **both** `clusterA` and `clusterB`.
2. Label both clusters **`acm/proxy-cclm=true`** so proxy-cclm merges CA ConfigMap rules into **`kubevirt.io:admin`** (CNV ClusterRole).
3. Confirm hub **`UserPermission`** for the same user: **`acm-vm-fleet:admin`** on **`local-cluster`** (`namespaces: *`), and **`kubevirt.io:admin`** `status.bindings` listing both cluster names.

```bash
oc get userpermission acm-vm-fleet:admin -o jsonpath='{.status.bindings}' && echo
oc get userpermission kubevirt.io:admin -o yaml
```

### Verification timeouts (`504`) — spoke / operator, not ACM

After a successful **write** to `kubevirt-external-ca`, the API polls **`kubevirt-ca`** for up to **30s**. **`VERIFY_TIMEOUT`** or **`VERIFY_PARTIAL`** means the expected certs did not appear in `kubevirt-ca` in that window.

ACM has already done its part (ConfigMap write on the spoke). It does **not** retry automatically, trim bundles, or repair the KubeVirt operator. Typical causes on the **spoke** include:

- KubeVirt **virt-operator** slow or failing reconcile
- `kubevirt-external-ca` stuck **`pending`** or conflicting edits
- **`kubevirt-ca`** near size limits or unsupported bundle growth
- CNV / cluster conditions unrelated to hub MTV

**Operator action:** inspect `virt-operator`, `KubeVirt` conditions, and ConfigMaps on the affected cluster(s); follow KubeVirt CCLM CA guidance; retry the API when the operator is healthy. Extending hub wait time or “fixing” merge logic in ACM is out of scope for this API.

## E2E tests

Ginkgo specs live in `test/e2e/cclm_ca_exchange_test.go` (label `cclm_ca_exchange`). They run with the migration advisor harness:

```bash
make prepare-e2e-test
make run-advisor-test
```

On a single kind hub, both managed clusters use the same API server; the happy path asserts `CA_ALREADY_TRUSTED` after a successful spoke ConfigMap read, and a case strips `acm/proxy-cclm` to assert `PROXY_CCLM_NOT_ENABLED`.