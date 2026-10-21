-- +goose Up
-- SNMP trap intake (ADR-025, doc 36). netinv-events writes one row per trap it
-- accepts, from a known device or not, and one netinv_snmp_trap sample per trap
-- from a known device. The row is what an operator reads; the sample is what a
-- rule alerts on.
--
-- device_id carries no foreign key on purpose. The listener resolves addresses
-- from a periodically refreshed index, so a device deleted between refreshes
-- would otherwise abort the whole COPY batch — losing every other trap in it
-- to protect a reference the read query already treats as optional.
--
-- The community string is not stored. In a v1/v2c trap it is the sender's
-- shared secret, and an event log readable under devices:read is not where a
-- secret belongs.
CREATE SCHEMA IF NOT EXISTS events;

CREATE TABLE events.snmp_traps (
  id          bigserial PRIMARY KEY,
  received_at timestamptz NOT NULL,
  source      inet NOT NULL,
  agent_addr  inet,
  device_id   text,
  version     text NOT NULL CHECK (version IN ('1', '2c')),
  inform      boolean NOT NULL DEFAULT false,
  trap_oid    text NOT NULL,
  trap_name   text,
  if_index    integer,
  varbinds    jsonb NOT NULL DEFAULT '[]'
);

CREATE INDEX snmp_traps_device_idx ON events.snmp_traps (device_id, id DESC);
CREATE INDEX snmp_traps_received_idx ON events.snmp_traps (received_at);
CREATE INDEX snmp_traps_unknown_idx ON events.snmp_traps (id DESC) WHERE device_id IS NULL;

-- Two built-in rules, and deliberately not more. Polling already alerts on the
-- standard traps' subjects — ar_if_down on oper status, ar_reboot on sysUpTime
-- reset — so a trap rule for linkDown or coldStart would put a second alert on
-- every such event. What polling cannot see is what these cover:
--
--   * authenticationFailure: something is talking to the device with the wrong
--     credentials. ar_poll_auth covers NetInv's own polls failing; this is the
--     device reporting a request from anyone, which is the security signal.
--   * everything else: vendor notifications (PSU, fan, HA failover, config
--     change, BGP) have no polled counterpart, and their names are not known
--     here without the vendor's MIB, so they alert as one rule labelled by
--     trap. Info severity, because a trap's importance is the vendor's call and
--     cannot be read off the PDU; an operator who knows a trap matters writes
--     a rule for it at the severity it deserves.
--
-- max_over_time over 10m makes each alert fire on arrival and resolve after ten
-- quiet minutes: a trap is an event, and carries no "cleared" state to resolve
-- on. The `> 0` is in the expression because the evaluator fires on every
-- series a rule returns.
INSERT INTO alerting.alert_rules
  (id, tenant_id, name, kind, severity, expr, condition, is_builtin, annotations)
VALUES
  ('ar_trap_auth_failure', 't_default', 'Device reported an SNMP authentication failure',
   'threshold', 'warning',
   'max_over_time(netinv_snmp_trap{trap="authenticationFailure"}[10m]) > 0',
   '{"operator":">","value":0,"window":"10m"}',
   true,
   '{"summary":"{{device}} rejected an SNMP request with the wrong credentials"}'),
  ('ar_trap_received', 't_default', 'SNMP trap received',
   'threshold', 'info',
   'max_over_time(netinv_snmp_trap{trap!~"linkUp|linkDown|coldStart|warmStart|authenticationFailure"}[10m]) > 0',
   '{"operator":">","value":0,"window":"10m"}',
   true,
   '{"summary":"{{device}} sent SNMP trap {{trap}}"}');

-- +goose Down
DELETE FROM alerting.alert_instances WHERE rule_id IN ('ar_trap_auth_failure', 'ar_trap_received');
DELETE FROM alerting.alert_rules WHERE id IN ('ar_trap_auth_failure', 'ar_trap_received');
DROP TABLE events.snmp_traps;
DROP SCHEMA IF EXISTS events;
