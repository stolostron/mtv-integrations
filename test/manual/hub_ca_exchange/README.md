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

Callers need hub **`acm-vm-fleet:view`** or **`acm-vm-fleet:admin`** (e.g. user2/u3 fleet MRAs) **and** **`kubevirt.io:admin`** on both spokes (user2/u4). The API enforces kubevirt only; verify both with `oc get userpermission`.

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
**Advisor:** `mtv-advisor-manual` · **Image:** `mtv-integrations-acm-50186:latest` (build **6**, Nov 2026)  
**Transport:** MTV `mtv-integrations/<cluster>-mtv` token via cluster-proxy user-server; hub auth = caller `kubevirt.io:admin` `UserPermission`.

### Auth matrix (`POST`, `clearKubevirtCA: false`)

| Caller | Fleet UP | Kubevirt UP | Clusterview GET workaround | HTTP | Code / notes |
|--------|----------|-------------|----------------------------|------|----------------|
| (no token) | — | — | — | **401** | `UNAUTHORIZED` |
| **ca-exchange-u2** | yes | yes (cnv-1, cnv-2) | yes | **200** | Success |
| **ca-exchange-u3** | yes | no | yes | **403** | Missing kubevirt UP |
| **ca-exchange-u3** | yes | no | **removed** (`ca-exchange-u3-clusterview-get`) | **403** | `can-i get userpermissions` → no; same message |
| **ca-exchange-u4** | no | yes (cnv-1, cnv-2) | yes | **200** | Kubevirt-only sufficient; GET `kubevirt.io:admin` → 200 |

`GET /api/v1/proxy-cclm/health` → **200** `ok` (unauthenticated).

### Functional (ca-exchange-u2, build 6)

| `clearKubevirtCA` | HTTP | Result |
|-------------------|------|--------|
| **false** | **200** | `caVerified: true` on both clusters |
| **true** | **200** | First call after rotation-check fix; no spurious `CA_ROTATED_DURING_EXCHANGE` |

Build 5 (before verify fix): `clearKubevirtCA: true` could return **409** `CA_ROTATED_DURING_EXCHANGE` once; immediate retry → **200**.

### Not required for this path

- Addon **ConfigurationPolicy** merge of `kubevirt.io:admin` ConfigMap rules (removed; spoke uses MTV MSA token).
- Temporary hub Policy `proxy-cclm-ca-rbac-manual` (only needed for caller-bearer spoke experiments).

## Rollback

- Delete MRAs, test Users, htpasswd secret, restore `oauth/cluster` `spec.identityProviders: []`, remove `ca-exchange-manual-test` if created for SAs.