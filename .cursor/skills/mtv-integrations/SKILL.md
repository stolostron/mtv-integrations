---
name: mtv-integrations
description: >-
  Works on the stolostron/mtv-integrations Go repo: ACM MTV provider controller,
  Plan validating webhook (UserPermission + impersonation), Helm charts under
  charts/ (keep in sync with config/), and addons. Use when editing this
  repository, MTV/ACM integration, ManagedCluster providers, forklift Plans,
  validating webhook RBAC, Helm chart templates, golangci-lint, gofmt, or Go
  style fixes.
---

# MTV Integrations (this repository)

## What this repo is

Go module `github.com/stolostron/mtv-integrations`. It ships:

- **Controller** (`ManagedClusterReconciler`): onboarding managed clusters as Forklift/MTV `Provider` resources on the hub when labeled for MTV.
- **Validating webhook** (`/validate-plan`): admission on `forklift.konveyor.io/v1beta1` `Plan` **CREATE** and **UPDATE**.
- **Helm chart** under [`charts/`](charts/) and **kustomize** under [`config/`](config/) (manager deployment, ClusterRole, webhook TLS, ValidatingWebhookConfiguration).
- **OCM addons** under `addons/` (CNV / MTV addon manifests; separate from the core controller).

## Helm charts (`charts/`) — keep in sync with `config/`

Shipped installs use **Helm** (`charts/templates/`). **Kustomize** under `config/` is the alternate layout. When changing deployables, update **both** unless the change is intentionally chart-only or kustomize-only.

| Concern | Chart template | Kustomize counterpart (typical) |
|--------|----------------|----------------------------------|
| Manager ClusterRole | [`charts/templates/mtv-integrations-clusterrole.yaml`](charts/templates/mtv-integrations-clusterrole.yaml) | [`config/rbac/role.yaml`](config/rbac/role.yaml) |
| Deployment (args, mounts, webhook certs) | [`charts/templates/mtv-integrations-deployment.yaml`](charts/templates/mtv-integrations-deployment.yaml) | [`config/manager/manager.yaml`](config/manager/manager.yaml) |
| ValidatingWebhookConfiguration | [`charts/templates/mtv-integrations-validating-config.yaml`](charts/templates/mtv-integrations-validating-config.yaml) | [`config/default/webhook.yaml`](config/default/webhook.yaml), [`config/webhook_test/webhook.yaml`](config/webhook_test/webhook.yaml) |
| Webhook Service (TLS secret) | [`charts/templates/mtv-integration-webhook-service.yaml`](charts/templates/mtv-integration-webhook-service.yaml) | same `webhook.yaml` files above |
| ServiceAccount / binding | [`mtv-integrations-sa.yaml`](charts/templates/mtv-integrations-sa.yaml), [`mtv-integrations-clusterrolebinding.yaml`](charts/templates/mtv-integrations-clusterrolebinding.yaml) | [`config/rbac/role_binding.yaml`](config/rbac/role_binding.yaml) |
| OCM addon / placement (CNV, MTV) | [`cnv-*.yaml`](charts/templates/), [`mtv-*.yaml`](charts/templates/) | N/A (Helm-first for addon wiring) |

**Rule of thumb:** new API groups/verbs on the controller or webhook → mirror **ClusterRole** rules in **both** chart and `config/rbac/role.yaml`. New flags or volume mounts → mirror **Deployment** in both places.

## Plan webhook: authorization model

Do **not** assume `kubevirtprojects` list checks; the webhook validates **UserPermission** resources:

| Constant | Resource name (cluster-scoped GET) |
|----------|-------------------------------------|
| `managedcluster:admin` | `userpermissions/managedcluster:admin` |
| `kubevirt.io:admin` | `userpermissions/kubevirt.io:admin` |

- **API:** `clusterview.open-cluster-management.io/v1alpha1`, resource `userpermissions`.
- **Allow** if **either** `UserPermission` has a `status.bindings` entry where `cluster` matches the destination cluster name and `namespaces` contains `*` or the Plan’s `spec.targetNamespace`.
- **Cluster name:** `strings.TrimSuffix(plan.Spec.Provider.Destination.Name, "-mtv")`. If the destination provider name does **not** end with `-mtv`, validation is **skipped** (allowed).
- **Client:** dynamic client with **`rest.ImpersonationConfig`** from the admission request user (username, groups, UID). Effective access is the **end user’s** RBAC, not only the controller ServiceAccount.

Implementation lives in [`webhook/plan_webhook.go`](webhook/plan_webhook.go).

## RBAC expectations

- **End users** creating/updating Plans must be able to **get** those `UserPermission` objects (hub must allow it; same idea as `oc get userpermission <name>`).
- **Controller ClusterRole** ([`charts/templates/mtv-integrations-clusterrole.yaml`](charts/templates/mtv-integrations-clusterrole.yaml), [`config/rbac/role.yaml`](config/rbac/role.yaml)) includes `userpermissions` **get** for the manager SA; impersonation still drives the webhook’s real checks.

## Code map

| Area | Path |
|------|------|
| Entrypoint, webhook registration | [`cmd/main.go`](cmd/main.go) — `--enable-webhook`, cert paths, `Register("/validate-plan", ...)` |
| Webhook logic + tests | [`webhook/plan_webhook.go`](webhook/plan_webhook.go), [`webhook/plan_webhook_test.go`](webhook/plan_webhook_test.go) |
| Provider / ManagedCluster reconciliation | [`controllers/managedcluster_controller.go`](controllers/managedcluster_controller.go) |
| Deploy / chart | [`charts/templates/`](charts/templates/), [`config/manager/manager.yaml`](config/manager/manager.yaml) |
| Webhook e2e harness (kind) | [`config/webhook_test/`](config/webhook_test/), [`Makefile`](Makefile) `prepare-webhook-test`, `run-webhook-test` |
| E2e Plan fixtures | [`test/resources/webhook/`](test/resources/webhook/) (e.g. `userpermissions.yaml`, `plan_*.yaml`) |

## Testing (quick reference)

- Unit: `go test ./webhook/...` (and other packages as needed).
- Webhook e2e (Ginkgo): `make run-webhook-test` (after `prepare-webhook-test` per Makefile).
- Favor **focused** changes: match existing patterns in [`webhook/plan_webhook_test.go`](webhook/plan_webhook_test.go) (`k8s.io/client-go/dynamic/fake`).

## Lint and format (CI)

CI runs **[golangci-lint](https://golangci-lint.run/)** using [`.golangci.yml`](.golangci.yml). Before pushing, run:

```bash
golangci-lint run
```

**Common fixes:**

| Linter | What to do |
|--------|------------|
| **gofmt** | Run `gofmt -w <files>` on changed `.go` files (struct field alignment, spacing). |
| **goimports** | Fix import grouping/order like `goimports -w <files>` (often fixed by `golangci-lint run --fix` if supported). |
| **lll** | Default **line length 120**. Split long function signatures and calls across lines; wrap long `string` literals. Paths `api/*` and `internal/*` disable `lll` in this repo’s config—**`webhook/` and `controllers/` are not** excluded. |

**Context:** pass `context.Context` through handlers and API calls; avoid `context.TODO()` in production paths where the request already provides `ctx`.

## Docs

- Architecture overview: [`architecture/README.md`](architecture/README.md).
- Repo root [`README.md`](README.md) for install/build/run.

## Conventions

- Jira project for this work is often **ACM** (team context).
- Prefer **complete sentences** in commit messages and PR descriptions; use **conventional** prefixes (e.g. `feat(webhook): …`) when the team agrees.
