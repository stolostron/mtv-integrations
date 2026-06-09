# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

MTV Integrations is a Go controller-runtime operator that integrates the Migration Toolkit for Virtualization (MTV/Forklift) with Advanced Cluster Management (ACM). It has three runtime components:

1. **ManagedCluster Reconciler** — watches ManagedCluster resources labeled `mtv.konveyor.io/provider: "true"` and creates the ManagedServiceAccount, ClusterPermission, provider Secret, and Forklift Provider CR needed to onboard each cluster as an MTV provider.
2. **Plan Validation Webhook** — a validating admission webhook at `/validate-plan` (port 9443) that impersonates the requesting user and checks `UserPermission` resources (`managedcluster:admin` / `kubevirt.io:admin`) to authorize Plan CREATE/UPDATE.
3. **Migration Advisor API** — a plain-HTTP server (default `:8082`) at `/api/v1/migration-targets` that scores candidate clusters for a given source VM using Thanos metrics and the ACM Search API.

The repo also ships OCM addon manifests under `addons/` (CNV addon and MTV addon) — these are pure YAML, not Go code.

## Build and test commands

```bash
make build                # fmt + vet + compile bin/manager
make test                 # unit tests (envtest, excludes e2e); installs tools on first run
make lint                 # golangci-lint (config: .golangci.yml)
make lint-fix             # lint with auto-fix
```

### Running a single test

```bash
# Single package
go test -v ./controllers/migrationadvisor/...

# Single test by name (regex)
go test -v ./webhook/... -run TestValidateWebhook
```

### E2E tests (require kind cluster)

E2E tests use Ginkgo with label filters. Each suite needs a kind cluster set up first:

```bash
make prepare-e2e-test              # kind + cert-manager + CRDs + deploy controller
make run-e2e-test                  # core e2e (excludes webhook, provider-crd, advisor)
make run-webhook-test              # webhook e2e (needs prepare-webhook-test instead)
make run-provider-crd-test         # provider CRD e2e
make run-advisor-test              # migration advisor e2e (starts fake Thanos + Search servers)
make delete-cluster                # tear down kind
```

### Running locally (no webhook)

```bash
make run                           # or use VS Code launch config with --enable-webhook=false
```

## Architecture

For detailed architecture, see [architecture/README.md](architecture/README.md).

### Key source layout

- `cmd/main.go` — entrypoint; wires up the manager, webhook server, and advisor HTTP server; auto-discovers ACM Search and Thanos Route URLs at startup.
- `controllers/managedcluster_controller.go` — the `ManagedClusterReconciler`; manages Provider lifecycle via dynamic client and unstructured objects (no generated CRD types for Provider).
- `controllers/payloads.go` — GVR constants and payload constructors for Provider, ClusterPermission, ManagedServiceAccount, and Secret resources.
- `controllers/migrationadvisor/` — migration advisor: `handler.go` (HTTP handler + cluster data cache), `scorer.go` (scoring algorithm), `vm_fetcher.go` (VM lookup via Search API), `observability_client.go` (Thanos queries), `search_client.go` (ACM Search GraphQL).
- `webhook/plan_webhook.go` — admission handler; checks `UserPermission` resources to authorize Plan operations.
- `api/types.go` — shared request/response types for the migration advisor API.
- `addons/` — OCM AddOnTemplate YAML for CNV and MTV operators (not Go code).

### Design patterns

- **No generated CRD types for Provider**: the controller uses `dynamic.Interface` with `unstructured.Unstructured` objects and hand-built payloads (`payloads.go`) instead of generated typed clients.
- **Finalizer-based cleanup**: `ManagedClusterFinalizer` on ManagedCluster resources ensures Provider, Secret, ClusterPermission, and ManagedServiceAccount are cleaned up on deletion or label removal.
- **Resources follow a naming convention**: managed resources are named `<cluster-name>-mtv` (e.g., provider, secret, service account).
- **UserPermission names are configurable**: the webhook reads `MTV_USERPERMISSION_NAMES` env var for e2e/kind testing because standard Kubernetes rejects `:` in resource names.
- **Advisor caching**: cluster-wide data (node metrics, Ceph metrics, StorageClasses) is cached with a configurable TTL (default 30s) and uses `singleflight` to deduplicate concurrent cache rebuilds.

## Testing patterns

- Unit tests use controller-runtime's `envtest` (real etcd + API server, no mocking).
- E2E tests use Ginkgo/Gomega with label filters (`webhook`, `managedcluster_provider_crd`, `migration_advisor`).
- Advisor e2e tests start fake Thanos and Search servers (`test/utils/fake-thanos-server/`, `test/utils/fake-search-server/`).
- Test helpers are in `test/utils/`.

## CI

GitHub Actions runs unit tests, webhook tests, provider-CRD tests, e2e tests, and advisor tests as separate jobs, then aggregates coverage for SonarCloud.

## Personal configuration

Read personal config at the start of any task that needs an assignee, email, or project key.
Use the tool-aware fallback chain: ~/.config/opencode/user.local.md (OpenCode),
.claude/user.local.md (Claude Code), or .cursor/rules/user.local.mdc (Cursor, already in context).
If none exist, fall back to agent memory (`user-config`), then placeholders.
Run `make personalize` to generate all three files (if this repo uses Fleet Engineering tooling).

## Fleet Engineering Skills

All skills are available as slash commands via the installed Fleet Engineering plugin. See the [Fleet Engineering skills catalog](https://github.com/OpenShift-Fleet/agentic-sdlc/blob/main/skills/README.md) for the full list with when-to-use guidance.
