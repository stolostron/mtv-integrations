# Manual hub: proxy-cclm CA exchange

Use **OpenShift Users** (not ServiceAccounts) for Matrix A. ServiceAccounts do not get `UserPermission` projections on many hubs; htpasswd users with **MulticlusterRoleAssignment** do.

## 1. Deploy advisor

```bash
oc apply -f advisor-manual.yaml
# Plus hub lease Role/RoleBinding from config/rbac/proxy_cclm_ca_exchange/
```

## 2. Htpasswd users (dev hub only)

```bash
htpasswd -c -B -b /tmp/ca-exchange.htpasswd ca-exchange-u2 '<password>'
htpasswd -B -b /tmp/ca-exchange.htpasswd ca-exchange-u3 '<password>'
htpasswd -B -b /tmp/ca-exchange.htpasswd ca-exchange-u4 '<password>'
oc create secret generic ca-exchange-htpasswd -n openshift-config \
  --from-file=htpasswd=/tmp/ca-exchange.htpasswd --dry-run=client -o yaml | oc apply -f -
oc apply -f oauth-htpasswd-idp.yaml
```

## 3. MRAs + UserPermission GET (ACM 2.17)

On **ACM 2.17**, fleet roles lack **get/watch** on `userpermissions`; apply the workaround role, then test users:

```bash
oc apply -f ../../../config/rbac/proxy_cclm_ca_exchange/caller_userpermissions.yaml
oc apply -f users-matrix-a.yaml
```

On **ACM 5.1+**, skip `caller_userpermissions.yaml` when fleet MRAs already allow GET.

Wait for MRA reconciliation, then as each user:

```bash
oc login <api-url> -u ca-exchange-u2 -p '<password>'
oc get userpermission
```

**Required for exchange:** **`kubevirt.io:admin`** on both spokes (user2/u4) and hub **`get`** on `userpermissions` (CRB on 2.17; fleet MRA on 5.1+ when `oc auth can-i get userpermissions` is yes). **Not required:** fleet MRA — user4 has kubevirt only and succeeds. Matrix A still assigns **`acm-vm-fleet:admin`** on user2/u3 for fleet/list UX, not for the API gate.

## 3b. Temporary hub Policy (legacy caller-bearer only)

**Obsolete** when the manager uses **MTV provider tokens** for spoke ConfigMap access (ACM-50186 default). Only needed on older images that proxied with the **caller's** hub bearer and lacked spoke `kubevirt.io:admin` ConfigMap rules.

## 4. Call exchange

```bash
TOKEN="$(oc whoami -t)"
ADVISOR=http://mtv-advisor-manual.open-cluster-management.svc:8082
curl -sS -X POST "$ADVISOR/api/v1/proxy-cclm/ca-exchange" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"clusterA":"cnv-1","clusterB":"cnv-2","clearKubevirtCA":false}'
```

Run `clearKubevirtCA: true` as a **separate** POST (user2 only).

## Hub validation results (manual)

**Hub:** `full-size-azure-ocp5-m6n8w` (ACM dev) · **Spokes:** `cnv-1`, `cnv-2`  
**Advisor:** `mtv-advisor-manual` · **Image:** `mtv-integrations-acm-50186:latest` (build **7**, `sha256:c5f077f3…`, Nov 2026)  
**Transport:** MTV `mtv-integrations/<cluster>-mtv` token via cluster-proxy user-server; hub auth = caller `kubevirt.io:admin` `UserPermission`.

### Auth matrix (`POST`, `clearKubevirtCA: false`)

**API enforces:** `kubevirt.io:admin` `UserPermission` on **both** spokes (via `GET userpermissions/kubevirt.io:admin` as the caller). **Fleet** `UserPermission` is not checked by the exchange.

**Hub operational need (ACM 2.17):** callers must be able to **`get`** `userpermissions` in clusterview.

**CRB** (ClusterRoleBinding on the **hub**, not MRA): e.g. `ca-exchange-u4-clusterview-get` binds the test User to ClusterRole `mtv-integrations:proxy-cclm-ca-exchange-userpermissions` (`config/rbac/proxy_cclm_ca_exchange/caller_userpermissions.yaml`). That grants **`get`** on clusterview `userpermissions` so the exchange can `GET …/userpermissions/kubevirt.io:admin` as the caller. Fleet MRAs on 2.17 only **`list`** that API — skip the CRB on 5.1+ when fleet roles already allow **`get`**.

| Caller | `kubevirt.io:admin` UP (cnv-1, cnv-2) | Hub `GET …/userpermissions/kubevirt.io:admin` | HTTP | Notes |
|--------|--------------------------------------|-----------------------------------------------|------|-------|
| (no Bearer) | — | — | **401** | `UNAUTHORIZED` |
| **ca-exchange-u2** | **yes** | allowed | **200** | Also has fleet UP (not required for API) |
| **ca-exchange-u3** | **no** | allowed | **403** | Fleet only; kubevirt UP missing |
| **ca-exchange-u3** | **no** | **denied** (CRB removed) | **403** | Same JSON message |
| **ca-exchange-u4** | **yes** | allowed (CRB present) | **200** | No fleet UP |
| **ca-exchange-u4** | **yes** | **denied** (CRB removed) | **403** | Has kubevirt MRA but cannot `GET`; `can-i get userpermissions` → no |

`GET /api/v1/proxy-cclm/health` → **200** `ok` (unauthenticated).

### Re-run after lease renewal + verify fixes (build 7)

Recorded **2026-11-23** after `mtv-integrations-acm-50186-7` rebuild + `mtv-advisor-manual` rollout (`clearKubevirtCA: false` only):

| Caller | HTTP | Notes |
|--------|------|-------|
| (no Bearer) | **401** | `UNAUTHORIZED` |
| **ca-exchange-u2** | **200** | `caVerified` true both spokes; near-limit warnings (51–52 certs) |
| **ca-exchange-u3** | **403** | fleet only |
| **ca-exchange-u4** | **200** | kubevirt only (no fleet UP) |
| **ca-exchange-u3** (CRB removed) | **403** | same kubevirt message (still lacks kubevirt UP) |
| **ca-exchange-u4** (CRB removed) | **403** | `can-i get userpermissions` → **no** |
| **ca-exchange-u2** (`clearKubevirtCA: true`) | **200** | counts **2**/**2**, no near-limit warnings |

`GET /api/v1/proxy-cclm/health` → **200** `ok`.

### Functional (ca-exchange-u2, build 6)

Recorded on hub **2026-11-20** (`cnv-1` ↔ `cnv-2`), back-to-back POSTs.

**`clearKubevirtCA: false`** — HTTP **200** (bundles already large from prior testing):

```json
{
  "status": "success",
  "clusterA": { "cluster": "cnv-1", "caVerified": true, "certificateCount": 58, "writePerformed": true },
  "clusterB": { "cluster": "cnv-2", "caVerified": true, "certificateCount": 51, "writePerformed": true },
  "warnings": [
    { "cluster": "cnv-1", "code": "KUBEVIRT_CA_NEAR_LIMIT", "message": "…contains 58 certificates…" },
    { "cluster": "cnv-2", "code": "KUBEVIRT_CA_NEAR_LIMIT", "message": "…contains 51 certificates…" }
  ]
}
```

**`clearKubevirtCA: true`** — HTTP **200** (immediate follow-up; operator refresh + exchange):

```json
{
  "status": "success",
  "clusterA": { "cluster": "cnv-1", "caVerified": true, "certificateCount": 2, "writePerformed": true },
  "clusterB": { "cluster": "cnv-2", "caVerified": true, "certificateCount": 2, "writePerformed": true }
}
```

`KUBEVIRT_CA_NEAR_LIMIT` is emitted when `certificateCount` **> 45** after exchange (success still **200**).

Build 5 (before verify fix): `clearKubevirtCA: true` could return **409** `CA_ROTATED_DURING_EXCHANGE` once; immediate retry → **200**.

### Not required for this path

- Addon **ConfigurationPolicy** merge of `kubevirt.io:admin` ConfigMap rules (removed; spoke uses MTV MSA token).
- Temporary hub Policy `proxy-cclm-ca-rbac-manual` (only needed for caller-bearer spoke experiments).

## Rollback

- Delete MRAs, test Users, htpasswd secret, restore `oauth/cluster` `spec.identityProviders: []`, remove `ca-exchange-manual-test` if created for SAs.