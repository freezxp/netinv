-- +goose Up
-- Flow collection had no staleness rule, so it could stop and nothing said so.
-- It did, twice, on the pilot:
--
--   exporter at remote site A   last sample 2026-08-15 16:57:55Z
--   exporter at remote site B   last sample 2026-08-15 16:58:55Z
--   exporter on the local LAN   last sample 2026-08-17 09:50:55Z
--
-- The first two stopped within a minute of each other and went unnoticed for 42
-- hours, because the third kept exporting and every fleet-wide flow query stayed
-- non-empty. **Partial loss is the case worth alerting on**, not just total loss:
-- a rule asking only "is any flow arriving" stays green through those 42 hours.
--
-- ar_flow_exporter_stale is therefore the primary rule. It enumerates exporters
-- seen in a long window and subtracts those seen in a short one, so it reports
-- one series per stale exporter, carrying the `exporter` label — which
-- absent_over_time cannot do, since it reports the absence of a whole selector
-- and returns no labels. Two properties come free and are worth naming:
--
--   * it covers total loss as well as partial. When every exporter stops, every
--     exporter is stale, and the rule fires once per exporter (verified against
--     the pilot mid-outage: one alert for each of the three above).
--   * it self-gates. A deployment that has never received flow has no exporters
--     in the long window, so the rule yields nothing and stays silent. Flow is
--     optional (ADR-020), and most deployments run without it.
--
-- ar_flow_absent extends coverage past the primary rule's 7d window, for the
-- case where every exporter has been silent longer than that. It must be gated:
-- bare `absent_over_time(netinv_flow_bytes[20m])` returns 1 when the metric has
-- *never* existed, so shipping it ungated would fire on every install that never
-- configured flow — the same mistake FR-ALR-08 rejects for interfaces that were
-- never in service. The `and on() count(last_over_time(...[30d]))` clause makes
-- it fire only where flow has actually been seen.
--
-- The 7d and 30d windows are ceilings, and honest ones: past them the alert
-- resolves and the fleet goes quiet about a fault that is still there. That is
-- the defect FR-ALR-08 rejects lookback windows for, accepted here only because
-- the alternative needs a metric NetInv does not publish yet — an
-- inventory-derived `netinv_flow_exporter_expected` built from
-- devices.attrs->'flow_exporters', which would make the expected set a stored
-- fact like inventory.interfaces.ever_up rather than a window. Doc 34 §5.1
-- records it as the follow-up.
--
-- Neither expression uses timestamp(last_over_time(...)). That form does not
-- advance reliably under -search.latencyOffset plus result caching and reads as
-- stale while collection is healthy; presence via last_over_time does not have
-- the problem. Cost measured against the pilot's 32 588 flow series, per 30 s
-- evaluation cycle: 52 ms for the stale rule, 57 ms for the absent rule.

INSERT INTO alerting.alert_rules
  (id, tenant_id, name, kind, severity, expr, condition, is_builtin, annotations)
VALUES
  ('ar_flow_exporter_stale', 't_default', 'Flow exporter stopped exporting',
   'threshold', 'warning',
   'count by (exporter) (last_over_time(netinv_flow_bytes[7d])) unless count by (exporter) (last_over_time(netinv_flow_bytes[20m]))',
   '{"operator":"==","value":0,"for_s":1200,"quiet_window":"20m","seen_window":"7d"}',
   true,
   '{"summary":"Flow exporter {{exporter}} has sent nothing for 20m"}'),
  ('ar_flow_absent', 't_default', 'No flow received from any exporter',
   'threshold', 'warning',
   'absent_over_time(netinv_flow_bytes[20m]) and on() count(last_over_time(netinv_flow_bytes[30d]))',
   '{"operator":"==","value":1,"for_s":1200,"quiet_window":"20m","seen_window":"30d"}',
   true,
   '{"summary":"No NetFlow/IPFIX received from any exporter for 20m — if netinv-flow logs nothing at all, the exporters stopped sending rather than the collector failing"}')
-- Added after this migration first shipped. alert_rules carries
-- UNIQUE (tenant_id, name), so on a deployment where an operator had already
-- created a rule with either of these names, this INSERT aborted the migration
-- — and an aborted migration exits the api at startup, on every start, leaving
-- the deployment stuck several versions behind with nothing but a crashloop to
-- explain it. Seeding fixed rows has to tolerate a database that is not a
-- fresh install.
--
-- Editing an applied migration is safe here specifically: goose keys on the
-- version number rather than the file's contents, so a database that already
-- ran this one is untouched, and only deployments that have not yet reached it
-- see the change. The conflict target is omitted deliberately — the constraint
-- that bites is the name, not the primary key.
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM alerting.alert_rules
  WHERE id IN ('ar_flow_exporter_stale','ar_flow_absent') AND is_builtin;
