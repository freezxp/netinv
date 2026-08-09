# 25 — CI/CD Pipeline (GitHub Actions → GHCR → on-prem k8s)

**Status:** draft · **Depends on:** 24, 19, ADR-016

## 1. Principles

Trunk-based: short-lived branches → PR → squash to `main`; `main` is always releasable. Every merge produces deployable images; **deploys are pulled by the cluster** (no GitHub→on-prem credentials — doc 18 firewall matrix). Releases are tags (`v1.4.0`, SemVer). Path-filtered jobs keep the monorepo fast (docs-only PRs run docs checks only).

## 2. Workflows

### `ci.yml` — every PR + push to main
```
changes (paths-filter) ─┬─ backend:  go build ▸ golangci-lint ▸ go-arch-lint (boundary
                        │            rules, docs 12/13) ▸ unit ▸ integration
                        │            (testcontainers: PG/RMQ/VM/Redis) ▸ coverage gates
                        │            ▸ openapi generate+drift check (doc 24 §contract)
                        ├─ connectors: connector-lint (sdk-only imports, fixtures
                        │            present) ▸ tests ▸ zero-core-diff check (NFR-72)
                        ├─ frontend: pnpm install ▸ typecheck (incl. generated API
                        │            types) ▸ eslint (+boundary rules) ▸ vitest
                        │            ▸ build ▸ bundle-size budget (doc 14)
                        ├─ security: gitleaks ▸ govulncheck ▸ npm audit (fail: high+)
                        ├─ helm:     helm lint ▸ kubeconform ▸ chart-testing
                        └─ docs:     lychee link check ▸ mermaid compile check ▸
                                     "docs updated?" PR-template reminder (NFR-70)
```
Target: PR feedback <10 min (unit/lint fast-fail before integration).

### `e2e.yml` — merge to main + nightly
Compose the full stack (all services + snmpsim + MailHog) from freshly built images → Playwright journeys (doc 24 §3) → artifacts: traces/screenshots on failure.

### `build.yml` — merge to main + tags
Multi-arch (amd64/arm64) distroless images per service + frontend-nginx, buildx cache; tags: `sha-<short>`, `main`, and SemVer on release tags. Push GHCR → **trivy scan (gate: critical)** → SBOM (syft, attached) → **cosign sign** (keyless OIDC). Helm charts packaged and pushed as OCI to GHCR with matching version.

### `release.yml` — on tag `v*`
Verify `main` E2E green → generate release notes from conventional commits → GitHub Release with chart/SBOM links → bump `deploy/environments/staging` image digests (GitOps commit).

### `nightly.yml`
Load profile on staging (doc 24 §4) → SLO assertions (fail = issue auto-filed) · staging **backup-restore drill** (restore last night's PG backup + VM snapshot into a scratch namespace, run smoke) — NFR-31's "backups are tested, not hoped".

## 3. CD — on-prem pull model

**Argo CD** (or Flux; chart-agnostic) in the on-prem cluster watches `deploy/environments/{staging,prod}` in this repo (image digests + values per env):
- **staging:** auto-sync on every main merge → smoke suite runs in-cluster (Argo hook Job) → notifies.
- **prod:** sync gated on git tag promotion PR (human approves the environment diff) → Helm upgrade with pre-upgrade migration Job (doc 19 §4) → post-sync smoke → auto-rollback on hook failure (`helm rollback`, safe one version per NFR-51).
Remote-site pollers: same registry, `netinv-poller` chart per site cluster (or compose `docker compose pull && up -d` via cron/watchtower-style for non-k8s sites) — core tolerates ±1 version skew (doc 10 §3).

## 4. Secrets & access

GitHub side holds **no** cluster credentials; GHCR pull secrets in-cluster. Actions permissions: default read-only `GITHUB_TOKEN`, `id-token: write` only in build.yml (cosign), environments `staging`/`prod` with required reviewers on prod. Dependabot (Go, npm, Actions, base images) weekly, auto-merge patch-level on green CI.

## 5. Branch protection & quality gates summary

`main`: PR required · CI + E2E status checks · linear history · no force push. Merge blocks on: tests, coverage bars, lint/arch boundaries, security scans, OpenAPI drift, bundle budget, helm lint. Release blocks additionally on: soak/chaos-lite pass (doc 24 §4), security checklist (doc 20 §12), CHANGELOG + docs status flip for shipped features.

### 5.1 What is actually wired today

§2's diagram is the target shape; the pipeline as built is a subset, and the gap is recorded here rather than left to be discovered. Implemented in `ci.yml`: paths-filter, backend build/vet/test (`-race`, with a PostgreSQL service for integration tests), connector tests, `make connector-lint`, frontend typecheck/lint/build, gitleaks, `make licenses`, govulncheck, `npm audit`, and a Playwright E2E smoke. **Not yet wired:** golangci-lint as a blocking gate (it runs locally when installed), go-arch-lint boundary rules, coverage gates, OpenAPI drift check, bundle-size budget, helm lint/kubeconform, and the docs link/mermaid checks.

Two notes for anyone reading a red or absent build:

- **CI is free on public repositories** but consumes billable minutes on private ones. Between 2026-08-07 and going public, every run failed at the first job with *"recent account payments have failed or your spending limit needs to be increased"* — no code was ever evaluated. A run that fails before any step executes is a billing or permissions problem, not a test failure.
- **gitleaks is configured by `.gitleaks.toml`.** CI scans the working tree (`--no-git`), which walks the filesystem rather than the git index and therefore also sees git-ignored files — locally that means `deploy/compose-app/.env`, which holds real secrets and is correctly never committed. The config allowlists that path so the signal stays meaningful. It also allowlists one historical finding: a placeholder vault key (`0123456789abcdef…`) present in the workflow between commits e079092 and 77c5de9, which never protected anything.

## 6. Developer loop (solo + AI)

`make dev` = compose stack + hot-reload (air for Go, Vite) + snmpsim + seeded data — identical images/config to CI, so "works locally" ≈ "passes CI". `make test` / `make lint` mirror CI exactly (same versions, pinned in `tools.go`/`.tool-versions`). An AI agent's inner loop is: edit → `make lint test` → targeted E2E — everything reproducible from the repo (NFR-73).
