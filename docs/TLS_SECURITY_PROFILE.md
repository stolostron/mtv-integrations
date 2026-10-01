# TLS security profile (ACM-47297)

## Assessment

The mtv-integrations binary runs on the ACM hub and reads the cluster `APIServer` (`config.openshift.io/v1`, name `cluster`) **TLSSecurityProfile** via `github.com/openshift/controller-runtime-common/pkg/tls`.

### TLS-terminating endpoints (groups apply)

| Component | Port / path | Role |
|-----------|-------------|------|
| Plan validation webhook | `:9443` `/validate-plan` | **TLS server** — `webhook.NewServer` with hub profile `TLSOpts` |
| Metrics server | bind address (HTTPS when `--metrics-secure`) | **TLS server** — controller-runtime metrics `TLSOpts` |

Both use the same `tlsProfileFunc` derived from `FetchAPIServerTLSProfile` in `cmd/main.go`.

### TLS clients (groups apply to outbound handshakes)

The migration advisor uses HTTPS clients built in `controllers/migrationadvisor/httpclient.go`. When calling in-cluster Search API or external OpenShift Routes (Thanos), the client `tls.Config` receives the same profile function (curve/group filter + min version + ciphers where applicable).

### Not terminated by this operator

| Component | Notes |
|-----------|--------|
| Migration Advisor API | Plain HTTP on `--advisor-addr` (default `:8082`); no TLS server in-process |
| Health probes | HTTP on `:8081` |
| OpenShift Route to advisor | TLS terminates at the cluster ingress/router, not in mtv-integrations |

No code changes are required for those paths beyond documenting that TLS group preferences are out of scope for non-TLS listeners.

## Group preferences behavior

- Profile resolution and `tls.Config` construction use `tlspkg.NewTLSConfigFromProfile`, which maps `TLSProfileSpec.Groups` to `tls.Config.CurvePreferences` via `library-go` (`TLSGroupsToCurveIDs`).
- When **`groups` is omitted** on the effective profile spec, **CurvePreferences are not set** and Go uses its default group selection (unchanged from pre-groups behavior for custom profiles without groups).
- When **`groups` is set** (including named profiles Old/Intermediate/Modern, which include default groups in `openshift/api`), the operator **filters** allowed groups per OpenShift semantics; Go may still reorder curves internally.
- Unsupported group names are logged and skipped; see startup logs in `getInitialTLSProfile`.

## Reload on profile change

`SecurityProfileWatcher` compares the full `TLSProfileSpec` (including `Groups`) and triggers a graceful process restart when the hub profile changes, so servers and advisor clients pick up new settings.
