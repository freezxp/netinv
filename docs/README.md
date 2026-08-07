# NetInv Design Package — Document Index

30 documents. Numbers are canonical references ("doc 09" = API spec). Read top-to-bottom for the full picture, or follow the role guides below.

| # | Document | One-liner | Status |
|---|---|---|---|
| 01 | [Executive Summary](01-executive-summary.md) | What, why, for whom, in 5 minutes | draft |
| 02 | [Product Requirements (PRD)](02-prd.md) | Personas, problems, features, success metrics, scope cuts | draft |
| 03 | [Functional Requirements (FRS)](03-frs.md) | Numbered FR-xxx testable requirements per module | draft |
| 04 | [Non-Functional Requirements](04-nfr.md) | Performance, scale, availability, capacity triggers | draft |
| 05 | [System Architecture](05-system-architecture.md) | Services, data flow, messaging topology, rationale | draft |
| 06 | [High-Level Architecture Diagram](06-hla-diagram.md) | The one-page Mermaid system picture | draft |
| 07 | [Sequence Diagrams](07-sequence-diagrams.md) | Poll cycle, discovery, alert, auth, weathermap live data | draft |
| 08 | [Database Design + ER Diagram](08-database-design.md) | Every table: PK/FK/indexes, Mermaid ERD | draft |
| 09 | [API Specification](09-api-specification.md) | Every REST endpoint: method, URI, payloads, codes | draft |
| 10 | [Connector Architecture](10-connector-architecture.md) | Plugin framework, interfaces, vendor matrix | draft |
| 11 | [Synchronization Flow](11-sync-flow.md) | Discovery, incremental sync, change/delete detection | draft |
| 12 | [Folder Structure](12-folder-structure.md) | Monorepo layout and ownership rules | draft |
| 13 | [Backend Project Structure](13-backend-structure.md) | Go workspace, Clean Architecture package layout | draft |
| 14 | [Frontend Project Structure](14-frontend-structure.md) | React/TS app layout, state, component conventions | draft |
| 15 | [Entity Relationship Model](15-entity-model.md) | Logical entities and lifecycles (DB-agnostic) | draft |
| 16 | [Domain Model](16-domain-model.md) | Bounded contexts, aggregates, ubiquitous language | draft |
| 17 | [Class Diagram](17-class-diagram.md) | Key interfaces/types per context (Mermaid classDiagram) | draft |
| 18 | [Deployment Diagram](18-deployment-diagram.md) | Core site + remote sites, network paths, ports | draft |
| 19 | [Kubernetes Deployment Design](19-kubernetes-design.md) | Namespaces, workloads, Helm, resources, operators | draft |
| 20 | [Security Design](20-security-design.md) | AuthN/Z, crypto, secrets, RBAC matrix, threat model | draft |
| 21 | [Logging Strategy](21-logging-strategy.md) | Structured logs, levels, correlation, retention | draft |
| 22 | [Monitoring Strategy](22-monitoring-strategy.md) | Self-monitoring: metrics, SLOs, alerting on ourselves | draft |
| 23 | [Error Handling Strategy](23-error-handling.md) | Error taxonomy, retries, circuit breakers, API errors | draft |
| 24 | [Testing Strategy](24-testing-strategy.md) | Pyramid, SNMP simulation, contract tests, coverage bars | draft |
| 25 | [CI/CD Pipeline](25-cicd-pipeline.md) | GitHub Actions workflows, environments, release flow | draft |
| 26 | [Development Roadmap](26-development-roadmap.md) | Phases P0–P5, backend first, milestones | draft |
| 27 | [Sprint Planning](27-sprint-planning.md) | 20 two-week sprints, solo dev + AI | draft |
| 28 | [Risk Assessment](28-risk-assessment.md) | Risk register with likelihood/impact/mitigation | draft |
| 29 | [Future Enhancements](29-future-enhancements.md) | Wireless, flow, multi-tenant SaaS, HA/DR sequencing | draft |
| 30 | [UI Design](30-ui-design.md) | Every page: layout, components, states, dark mode | draft |

## Role guides

- **New AI agent taking over:** `/CLAUDE.md` → `/DECISIONS.md` → 01 → 05 → 26.
- **Backend work:** 05, 08, 09, 10, 11, 13, 16, 17, 23.
- **Frontend work:** 30, 09, 14, 02.
- **Ops/deploy:** 18, 19, 21, 22, 25, 04.
- **Product/management:** 01, 02, 26, 27, 28, 29.

## Conventions

- Diagrams are Mermaid, inline in the doc.
- Requirement IDs: `FR-<module>-<nn>` (doc 03), `NFR-<nn>` (doc 04), risks `R-<nn>` (doc 28).
- Cross-references use doc numbers.
- A doc's `Depends on` header lists which docs constrain it; change those first.
