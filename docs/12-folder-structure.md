# 12 — Folder Structure (Monorepo)

**Status:** draft · **Depends on:** ADR-015 · detail: 13 (backend), 14 (frontend)

```
netinv/
├── README.md                  # product overview + repo map
├── CLAUDE.md                  # AI onboarding (root); per-dir CLAUDE.md appear with code
├── DECISIONS.md               # ADR log — the "why"
├── LICENSE                    # proprietary (commercial ambitions, doc 29)
├── Makefile                   # dev entrypoints: make dev / test / lint / sim
├── docker-compose.yml         # full local stack incl. SNMP simulator (NFR-73)
├── .github/
│   ├── workflows/             # CI/CD pipelines (doc 25)
│   ├── PULL_REQUEST_TEMPLATE.md   # includes "docs updated?" gate (NFR-70)
│   └── CODEOWNERS
├── docs/                      # this design package, 01–30 + assets/
├── backend/                   # single Go module — all six services (doc 13)
├── connectors/                # vendor plugins + sdk + registry (doc 10)
│   ├── sdk/                   #   interfaces & value types — the plugin contract
│   ├── registry/              #   import-side registration
│   ├── generic/  cisco/  juniper/  huawei/  zte/  ubiquiti/
├── frontend/                  # React + TS SPA (doc 14)
├── deploy/
│   ├── helm/netinv/           # the product chart (doc 19)
│   ├── helm/netinv-poller/    # standalone remote-site poller chart
│   └── compose-poller/        # docker-compose for non-k8s remote sites
├── tools/
│   ├── snmpsim/               # simulator configs + recorded device walks (doc 24)
│   ├── loadgen/               # NFR load harness
│   └── mibs/                  # vendored MIB files used by connectors (reference only)
└── scripts/                   # repo-level dev/ops scripts (backup, seed, walk-recorder)
```

## Ownership & boundary rules

| Path | May import from | Never imports |
|---|---|---|
| `connectors/*` | `connectors/sdk`, stdlib | `backend/**`, other connectors (except `generic` embedding) |
| `connectors/sdk` | stdlib only | everything else |
| `backend/**` | `backend`, `connectors/sdk` + `registry` (poller cmd only) | `frontend` |
| `frontend` | its own packages; talks to backend **only** via `/api/v1` | — |
| `deploy`, `tools`, `scripts` | consume build artifacts | application internals |

Enforced by `make lint` (go-arch-lint rules + eslint import rules) — an AI agent violating a boundary fails CI, not review.

## Placement rules (for future contributors/AI)

- New vendor → `connectors/<vendor>/` only (doc 10 §6).
- New API resource → backend Inventory/relevant context + doc 09 update in same PR.
- New page → `frontend/src/features/<feature>/` + doc 30 update.
- New infra dependency → requires an ADR first.
- Anything temporary/experimental → `scratch/` (gitignored), never committed.
