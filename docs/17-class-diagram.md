# 17 — Class Diagram

**Status:** draft · **Depends on:** 10, 13, 16

Key types and interfaces per context — the *shape* of the code, not every field. Go has no classes; boxes are structs/interfaces, `<<interface>>` = Go interface, composition arrows = embedding.

## 1. Connector SDK & poller runtime (the plugin seam)

```mermaid
classDiagram
    class Connector {
        <<interface>>
        +Info() Info
        +Match(SysInfo) MatchScore
        +Collectors() Capabilities
    }
    class InventoryCollector { <<interface>> +CollectInventory(ctx, Session) InventorySnapshot }
    class InterfaceCollector { <<interface>> +CollectInterfaces(ctx, Session) []Sample }
    class HealthCollector    { <<interface>> +CollectHealth(ctx, Session) []Sample }
    class TopologyCollector  { <<interface>> +CollectTopology(ctx, Session) []Adjacency }
    class Session {
        <<interface>>
        +Get(ctx, oids) []Var
        +Walk(ctx, root) []Var
        +Target() TargetMeta
    }
    class GenericBase {
        +CollectInventory()
        +CollectInterfaces()
        +CollectHealth()  best-effort
        +CollectTopology()  LLDP
    }
    class CiscoIOS { +CollectHealth() vendor MIBs }
    class JuniperJunos { +CollectHealth() }
    class HuaweiVRP { +CollectHealth() }
    class Registry {
        +Register(Connector)
        +ByID(id) Connector
        +MatchAll(SysInfo) []Scored
    }
    class PollerRuntime {
        -workers WorkerPool
        -buffer DiskBuffer
        -batcher Batcher
        +HandleJob(PollJob)
    }
    class SNMPSession { gosnmp impl }

    Connector <|.. GenericBase
    GenericBase <|-- CiscoIOS : embeds
    GenericBase <|-- JuniperJunos : embeds
    GenericBase <|-- HuaweiVRP : embeds
    InventoryCollector <|.. GenericBase
    InterfaceCollector <|.. GenericBase
    HealthCollector <|.. GenericBase
    TopologyCollector <|.. GenericBase
    Session <|.. SNMPSession
    Registry o-- Connector
    PollerRuntime --> Registry : resolve connector_id
    PollerRuntime --> Session : creates per device
```

## 2. Inventory context (aggregate + sync)

```mermaid
classDiagram
    class Device {
        +ID DeviceID
        +SiteID · ConnectorID · CredentialID · ProfileID
        +Status DeviceStatus
        +Identity SysIdentity
        +ApplyOperatorEdit(cmd) error
        +ApplyNetworkFacts(snapshot) []Change
        +Retire() error
    }
    class Interface {
        +IfIndex int
        +Name · Alias · SpeedBps
        +State PresenceState
        +MarkMissing() · Restore()
    }
    class SyncDiffer {
        <<domain service>>
        +Diff(current Device, snap InventorySnapshot) ChangeSet
        -resolveInterfaceIdentity()
    }
    class DeviceRepository {
        <<interface>>
        +Get(ctx, id) Device
        +Search(ctx, Filter, Page) []Device
        +Save(ctx, Device, []Change) error
    }
    class CredentialVault {
        <<interface>>
        +Store(ctx, PlainSecret) CredentialID
        +Decrypt(ctx, id) PlainSecret  poller-path only
    }
    class SyncService {
        +HandleResult(SyncResult)
    }
    Device *-- Interface : owns
    SyncService --> SyncDiffer
    SyncService --> DeviceRepository
    SyncService --> EventPublisher
    DeviceRepository <|.. PostgresDeviceRepo
    CredentialVault <|.. EnvelopeVault
```

## 3. Application layer pattern (CQRS-lite, doc 05 §1) — one example each

```mermaid
classDiagram
    class CommandHandler~C~ {
        <<pattern>>
        +Handle(ctx, C) (Result, error)
        validate → authorize → tx → audit → events
    }
    class CreateDeviceCommand { +Name +MgmtIP +SiteID +CredentialID +ConnectorID +ProfileID }
    class CreateDeviceHandler {
        -repo DeviceRepository
        -creds CredentialVault
        -events EventPublisher
        -audit AuditWriter
    }
    class QueryHandler~Q~ {
        <<pattern>>
        +Handle(ctx, Q) (Result, error)
        authorize → cache? → read
    }
    class DashboardSummaryQuery { }
    class DashboardSummaryHandler {
        -cache Cache
        -pg ReadDB
        -vm MetricsReader
    }
    CommandHandler <|.. CreateDeviceHandler
    QueryHandler <|.. DashboardSummaryHandler
    CreateDeviceHandler ..> CreateDeviceCommand
    DashboardSummaryHandler ..> DashboardSummaryQuery
```

## 4. Alerting & notification

```mermaid
classDiagram
    class AlertRule {
        +Kind threshold|state|inventory
        +Expr MetricsQL
        +Scope
        +Fingerprint(labels) string
    }
    class AlertInstance {
        +State firing|acked|resolved|flapping
        +Ack(user, comment) error
        +Resolve(at) error
        +RegisterFlap() bool
    }
    class RuleEvaluator {
        <<domain service>>
        +Evaluate(rule, results, now) []Transition
    }
    class MetricsReader { <<interface>> +Query(ctx, expr, at) }
    class AlertRepository { <<interface>> +UpsertLive() +Transition(cas) }
    class RoutingService { +Route(alert, []Policy) []ChannelDispatch }
    class Sender { <<interface>> +Send(ctx, Rendered) error }
    class SMTPSender ; class SlackSender ; class WebhookSender

    RuleEvaluator --> MetricsReader
    RuleEvaluator --> AlertRepository
    AlertRule "1" --> "*" AlertInstance
    RoutingService --> Sender
    Sender <|.. SMTPSender
    Sender <|.. SlackSender
    Sender <|.. WebhookSender
```

## 5. Platform kernel interfaces (shared, doc 13)

```mermaid
classDiagram
    class TokenVerifier { <<interface>> +Verify(raw) Claims }
    class LocalJWTVerifier ; class OIDCVerifier { future Keycloak }
    class KeyProvider { <<interface>> +WrapDEK() +UnwrapDEK() }
    class EnvMasterKey ; class VaultProvider { future }
    class EventPublisher { <<interface>> +Publish(ctx, Event) }
    class Cache { <<interface>> +GetJSON +SetJSON +Invalidate }
    class Lock { <<interface>> +Acquire(key, ttl) Lease }
    class Clock { <<interface>> +Now() }
    TokenVerifier <|.. LocalJWTVerifier
    TokenVerifier <|.. OIDCVerifier
    KeyProvider <|.. EnvMasterKey
    KeyProvider <|.. VaultProvider
```

The two `future` implementations (OIDCVerifier, VaultProvider) are the concrete seams promised by ADR-010/011 — swap by configuration, no call-site changes.
