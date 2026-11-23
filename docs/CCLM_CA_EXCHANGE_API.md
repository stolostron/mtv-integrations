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

The advisor HTTP server uses a **150s** write timeout (up to **30s** clear per spoke when `clearKubevirtCA` is true, **30s** verify, plus I/O). Use a similar client timeout for exchange requests that perform writes or optional clear.

## Prerequisites

Complete these **before** calling the API (checks run in this order after auth):

| Requirement | Notes |
|-------------|--------|
| **`acm/proxy-cclm=true`** on **both** hub `ManagedCluster` objects | Proxy CCLM addon placement ([ACM-50184](https://redhat.atlassian.net/browse/ACM-50184)). Deploys KubeVirt Proxy datapath **ConfigurationPolicy** on the spoke. If **either** cluster lacks the label (or the value is not exactly `true`), the API returns **HTTP 400** / **`PROXY_CCLM_NOT_ENABLED`** — see [Missing `acm/proxy-cclm`](#missing-acmproxy-cclm-label). |
| **MulticlusterRoleAssignment on both `ManagedCluster`s** | **Required** for hub authorization. On the hub, create MRA so the caller gets **`kubevirt.io:admin`** **`UserPermission`** on **each** spoke named in the request (`clusterA`, `clusterB`): **`targetNamespaces: [openshift-cnv]`** and **Placement** resolving to those clusters. Without it, hub preflight returns **403**. Details: [MulticlusterRoleAssignment on managed clusters](#multiclusterroleassignment-on-managed-clusters). |
| **MTV onboarding** on both clusters | Hub Secret **`mtv-integrations/<cluster>-mtv`** (ManagedServiceAccount token) must exist for each exchange cluster. Spoke ConfigMap **`get` / `patch` / `update`** use that token via **cluster-proxy** user-server (`{user-server}/{cluster}/api/v1/...`). Override base URL with **`MTV_CLUSTER_PROXY_USER_SERVER`** when needed. **`MTV_CCLM_CLUSTER_PROXY_MODE=direct`** is for kind/e2e only. |
| KubeVirt on each spoke | ConfigMaps **`kubevirt-ca`** (non-empty `ca-bundle`) and pre-existing **`kubevirt-external-ca`** in **`openshift-cnv`** or the `kubeVirtNamespace` you pass on the request. The API does **not** create `kubevirt-external-ca`. Trust is between KubeVirt peers, not MTV/Forklift. |
| **Hub authorization for the caller** | See [Authorization](#authorization). **Required:** **`kubevirt.io:admin`** **`UserPermission`** on **both** exchange clusters (MRA) and hub RBAC to **`get`** `clusterview.open-cluster-management.io/userpermissions` (ACM **5.1+** fleet VM roles, or **`config/rbac/proxy_cclm_ca_exchange/caller_userpermissions.yaml`** on **2.17**). **`acm-vm-fleet:view`** / **`acm-vm-fleet:admin`** are **not** enforced by the API (optional for fleet console / `oc get userpermission`). |

Label both clusters on the hub:

```bash
oc label managedcluster cnv-1 acm/proxy-cclm=true --overwrite
oc label managedcluster cnv-2 acm/proxy-cclm=true --overwrite
```

## Authorization

Every request needs **`Authorization: Bearer <token>`** (OpenShift user or service account token on the hub). The controller checks the **caller's** identity (not the MTV controller ServiceAccount).

### What operators must grant callers

| Item | Required for exchange? | Notes |
|------|------------------------|--------|
| **`kubevirt.io:admin`** MRA | **Yes** | **Both** `clusterA` and `clusterB` in **`openshift-cnv`** (or your `kubeVirtNamespace`). **Enforced** via `GET userpermissions/kubevirt.io:admin` as the caller. |
| Hub **`get`** on **`userpermissions`** | **Yes** | Separate from fleet MRA on **ACM 2.17** — see [UserPermission GET](#acm-217-userpermission-get-on-fleet-roles). |
| **`acm-vm-fleet:admin`** (or **`view`**) on **`local-cluster`** | **No** | Optional: ACM fleet UI and `oc get userpermission` **list**. Neither fleet role is checked by the exchange handler. kubevirt-only callers can succeed (**no** fleet MRA). |

Grant **kubevirt** via **[MulticlusterRoleAssignment](https://github.com/stolostron/multicluster-role-assignment)** (OpenShift **User** or **Group** subjects; fine-grained RBAC on the hub).

### What the API enforces (hub preflight)

The migration advisor validates **`kubevirt.io:admin`** **`UserPermission`** (**GET** as the caller, `clusterview.open-cluster-management.io/v1alpha1`) before preflight or spoke calls.

**`status.bindings`** must include **both** `clusterA` and `clusterB` from the request (each binding’s `cluster` field matches the ManagedCluster name). Namespace scope is typically **`openshift-cnv`** or **`*`** from MRA.

```bash
oc auth can-i get userpermissions.clusterview.open-cluster-management.io
oc get userpermission kubevirt.io:admin -o jsonpath='{.status.bindings}' && echo
```

If the kubevirt check fails: **HTTP 403** — `forbidden: requires kubevirt.io:admin UserPermission on both clusters` (same message when **`GET`** is **Forbidden** or **`kubevirt.io:admin`** is **NotFound** for the caller).

### ACM 2.17: `UserPermission` GET on fleet roles {#acm-217-userpermission-get-on-fleet-roles}

On **ACM 2.17**, shipped **`acm-vm-fleet:view`** and **`acm-vm-fleet:admin`** include **`list`** on **`userpermissions`** but **not** **`get`** or **`watch`**. The exchange calls **`GET …/userpermissions/kubevirt.io:admin`** as the caller (not list). **Fleet MRA alone does not satisfy that** — bind **`mtv-integrations:proxy-cclm-ca-exchange-userpermissions`** to exchange users/groups, or use **ACM 5.1+** when fleet VM roles include **`get`** on **`userpermissions`**.

**ACM 5.1** adds **`get`** and **`watch`** on **`userpermissions`** to the fleet VM ClusterRoles. On **2.17**, bind each exchange caller to the workaround ClusterRole (one binding per User or Group):

```bash
oc apply -f config/rbac/proxy_cclm_ca_exchange/caller_userpermissions.yaml
oc create clusterrolebinding ca-exchange-<subject> \
  --clusterrole=mtv-integrations:proxy-cclm-ca-exchange-userpermissions \
  --user=<openshift-user>
# or: --group=<group>
```

On **5.1+**, skip the extra ClusterRoleBinding when **`oc auth can-i get userpermissions`** is **yes** for the caller (often true after fleet MRA; kubevirt-only identities may still need the binding).

### Spoke API (after hub preflight)

Spoke **`get` / `watch` / `patch` / `update`** use the **MTV ManagedServiceAccount token** from **`mtv-integrations/<cluster>-mtv`** through **cluster-proxy**. Missing provider secret, token, or spoke RBAC for the MTV service account still returns **403** (`FORBIDDEN` with remediation text) or **502**. Callers still need **`kubevirt.io:admin`** **`UserPermission`** on the hub (MRA below) — that gate is separate from spoke API credentials.

### MulticlusterRoleAssignment on managed clusters

**Required:** one MRA (per subject) for **`kubevirt.io:admin`** with **`targetNamespaces: [openshift-cnv]`** (or your `kubeVirtNamespace`) and **Placement** that selects **every** `ManagedCluster` you pass as **`clusterA`** and **`clusterB`**.

**Optional:** hub **`acm-vm-fleet:admin`** (or **`view`**) on **`local-cluster`** for fleet console workflows — not enforced by the exchange API.

The fine-grained RBAC addon materializes bindings on the hub and spokes; the exchange **enforces** kubevirt on both cluster names via **`UserPermission`**.

Confirm placements cover both cluster names:

```bash
oc get placementdecisions -A -o json | jq -r '.items[] | select(.status.decisions[]? .clusterName=="cnv-1" or .status.decisions[]? .clusterName=="cnv-2") | .metadata.namespace + "/" + .metadata.name'
```

Replace `cnv-1` / `cnv-2` with your **`ManagedCluster` metadata.name** values.

### MulticlusterRoleAssignment examples

**Hub fleet (optional)** — adjust `subject` and placement; prefer **`acm-vm-fleet:admin`** for operators who manage fleet on the hub:

```yaml
apiVersion: rbac.open-cluster-management.io/v1beta1
kind: MulticlusterRoleAssignment
metadata:
  name: ca-exchange-caller-fleet
  namespace: open-cluster-management-global-set
spec:
  subject:
    kind: User
    name: <your-user>
    apiGroup: rbac.authorization.k8s.io
  roleAssignments:
    - name: fleet-view-or-admin-hub
      clusterRole: acm-vm-fleet:admin
      clusterSelection:
        type: placements
        placements:
          - name: openshift-cnv
            namespace: open-cluster-management
```

**Spokes (`kubevirt.io:admin`)** — placement must include **`clusterA`** and **`clusterB`**:

If a spoke is missing from the placement or the subject lacks **`kubevirt.io:admin`** **`UserPermission`** for that cluster name, hub preflight fails with **403**. If **`mtv-integrations/<cluster>-mtv`** is missing, spoke client construction fails with **502** / **`CLUSTER_UNREACHABLE`**.

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

## Request

```json
{
  "clusterA": "cnv-1",
  "clusterB": "cnv-2",
  "kubeVirtNamespace": "openshift-cnv",
  "clearKubevirtCA": false
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `clusterA`, `clusterB` | yes | Hub `ManagedCluster` names |
| `kubeVirtNamespace` | no | Default `openshift-cnv` for both clusters when per-cluster overrides are omitted |
| `clusterAKubeVirtNamespace` | no | KubeVirt ConfigMap namespace for **`clusterA`** only (overrides `kubeVirtNamespace`) |
| `clusterBKubeVirtNamespace` | no | KubeVirt ConfigMap namespace for **`clusterB`** only (overrides `kubeVirtNamespace`) |
| `clearKubevirtCA` | no | Default **`false`**. When **`true`** and a spoke needs a write, clear `kubevirt-ca` `ca-bundle` once and wait for CNV to republish a **single** certificate before writing `kubevirt-external-ca`. **Destructive** on that spoke (drops peer CAs until the operator reconciles); use when bundles are large or stale, not for routine pairwise exchange on shared meshes. |

Verification polling uses a **fixed 30s** timeout (not configurable on the request). Hub exchange **leases** use a **120s** TTL so the lock outlives verification under normal operation. **`clearKubevirtCA`** can use up to **30s per spoke** that needs a write (same window).

`clusterA` and `clusterB` are hub `ManagedCluster` **metadata.name** values (not MTV provider suffixes).

### Example call

```bash
ADVISOR="${ADVISOR:-http://mtv-advisor-service.open-cluster-management.svc:8082}"
# Token for a caller with kubevirt.io:admin UserPermission on both clusters (MRA on spokes)
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
4. Build spoke Kubernetes clients with the **MTV provider token** from **`mtv-integrations/<cluster>-mtv`** (direct mode: API URL/CA from the same secret or ManagedCluster).
5. Read `kubevirt-ca`, decide bidirectional trust. If **`clearKubevirtCA`** is **`true`** and a spoke needs a write: with **more than one** certificate in `ca-bundle`, **clear `kubevirt-ca`** once, then poll until CNV leaves **exactly one** certificate (up to 30s per spoke). Omitted or **`false`**: skip clear (full peer `kubevirt-ca` may be copied to `kubevirt-external-ca` as today).
6. Acquire hub Leases, write peer material to `kubevirt-external-ca` when needed, poll until `kubevirt-ca` reflects trust (or return timeout).

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

After hub authorization (HTTP **403** if the caller cannot **`GET userpermissions/kubevirt.io:admin`** for both clusters), preflight loads each hub **`ManagedCluster`** in request order: **`clusterA`**, then **`clusterB`**. The **first** cluster that fails the label check ends the request; the other cluster is not evaluated in that response.

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

The controller authorizes the **API caller** on the hub (`Authorization: Bearer` from the POST, **`kubevirt.io:admin`** **`UserPermission`**). Spoke ConfigMap calls use the **ManagedServiceAccount token** in **`mtv-integrations/<cluster>-mtv`** through **cluster-proxy** (production) or the secret’s **`url`** / **`cacert`** in direct e2e mode.

Spoke **403** returns **`FORBIDDEN`** with remediation text (provider secret, MTV MSA RBAC, caller UserPermission); other spoke API failures return **`CLUSTER_UNREACHABLE`** / 502. **Forklift `Provider` CRs** are unrelated to transport but reflect the same MTV onboarding that creates the provider secret.

Spoke API URLs must use **`https`**. On the spoke the implementation **GET**s `kubevirt-ca`, optionally **PATCH**es it when **`clearKubevirtCA`** is true, and **PATCH**es existing `kubevirt-external-ca` only (no create):

- `ConfigMap/kubevirt-ca` (read; optional clear when `clearKubevirtCA` is true and more than one cert)
- `ConfigMap/kubevirt-external-ca` (patch `ca-bundle`)

Default namespace is `openshift-cnv` (override with `kubeVirtNamespace` or controller env `MTV_KUBEVIRT_NAMESPACE`).

## Concurrency

Hub `coordination.k8s.io/Lease` objects in **`mtv-integrations`** (`proxy-cclm-ca-exchange-<cluster>`) serialize exchanges per cluster across API replicas. The manager ServiceAccount needs a **namespaced Role** on leases in `mtv-integrations` (not cluster-wide). ConfigMap updates use Kubernetes resourceVersion optimistic concurrency.

## Error codes

| Code | HTTP | Meaning |
|------|------|---------|
| `UNAUTHORIZED` | 401 | Missing or invalid `Authorization: Bearer` header (`status: failed`, `Content-Type: application/json`) |
| `FORBIDDEN` | 403 | **Hub:** caller lacks `kubevirt.io:admin` `UserPermission` for both clusters. **Spoke:** MTV provider token denied ConfigMap access (remediation text references provider secret, MSA RBAC, and caller UserPermission) |
| `INVALID_REQUEST` | 400 | Missing or invalid cluster names, or ManagedCluster not found |
| `PROXY_CCLM_NOT_ENABLED` | 400 | ManagedCluster missing `acm/proxy-cclm=true` |
| `KUBEVIRT_EXTERNAL_CA_NOT_FOUND` | 400 | `kubevirt-external-ca` ConfigMap missing on a spoke that needs a write |
| `CLUSTER_LOOKUP_FAILED` | 502 | Hub could not read ManagedCluster |
| `CA_ALREADY_TRUSTED` | 200 | No write required |
| `CA_EXCHANGE_IN_PROGRESS` | 409 | Lock held or `kubevirt-external-ca` pending |
| `CLUSTER_UNREACHABLE` | 502 | Secret, API, or ConfigMap failure (non-RBAC; does not include spoke **403** — see `FORBIDDEN`) |
| `KUBEVIRT_CA_CLEANUP_TIMEOUT` | 504 | CNV operator did not refresh `kubevirt-ca` to a single certificate after clear |
| `VERIFY_TIMEOUT` | 504 | Trust not observed in `kubevirt-ca` in time |
| `VERIFY_PARTIAL` | 504 | One cluster verified, the other did not |
| `CA_ROTATED_DURING_EXCHANGE` | 409 | `kubevirt-ca` changed during verify **before** trust polls succeeded (not used when `clearKubevirtCA: true`; trust polls are the success criterion) |
| `KUBEVIRT_CA_NEAR_LIMIT` | 200 (warning) | More than 45 certs in `kubevirt-ca` |

KubeVirt/CNV operator owns the authoritative CA in `kubevirt-ca`. Optional request flag **`clearKubevirtCA`** (default **false**) triggers one-time `kubevirt-ca` clear and operator refresh before `kubevirt-external-ca` writes; otherwise ACM only patches `kubevirt-external-ca`.

### Spoke RBAC denied (`FORBIDDEN` / 403)

When the MTV provider token cannot **get**, **patch**, or **update** `kubevirt-ca` / `kubevirt-external-ca` on a spoke, the JSON `message` includes the failing **`ManagedCluster`** name and asks you to:

1. Confirm hub Secret **`mtv-integrations/<cluster>-mtv`** exists and the ManagedServiceAccount token is current (MTV cluster onboarding).
2. Assign **`kubevirt.io:admin`** via **MulticlusterRoleAssignment** in **`openshift-cnv`** on **both** `clusterA` and `clusterB` (hub authorization gate).
3. Confirm the caller can **`get`** `userpermissions` and **`kubevirt.io:admin`** shows both cluster names in `status.bindings` (see [ACM 2.17 GET workaround](#acm-217-userpermission-get-on-fleet-roles)).
4. Label both clusters **`acm/proxy-cclm=true`** (proxy datapath addon).

```bash
oc get userpermission
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