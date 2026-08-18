-- +goose Up
-- "Device down" meant "ping failed", and those are not the same thing.
--
-- ar_device_down has been `max_over_time(netinv_icmp_up[3m]) == 0` since
-- migration 0005. A device that answers SNMP perfectly — inventory syncing,
-- interface counters advancing, graphs filling — raised a *critical* alert
-- because echo replies stopped, which happens for reasons that have nothing to
-- do with the device being down: a management ACL that permits udp/161 and
-- omits ICMP, control-plane ICMP policing on a router, or the poller's own
-- unprivileged-ping path breaking inside a container. Waking someone for a
-- device that is demonstrably answering is how an alert loses its meaning.
--
-- Split into the two facts:
--
--   ar_device_down (critical)  ICMP is silent AND nothing proves SNMP works.
--   ar_icmp_down   (warning)   ICMP is silent while SNMP demonstrably works.
--
-- The device-down side is written as `unless` against SNMP-is-working rather
-- than `and` against SNMP-is-failing, and the distinction is load-bearing. A
-- device with no SNMP agent at all — the pilot runs a mesh-joined AP on an
-- ICMP-only polling profile — has no netinv_poll_success series whatsoever.
-- With `and`, its side of the join is empty and it could never raise a
-- device-down alert again: the one device whose only signal is ping would be
-- the one device that could not alert on losing it. `unless` treats "no proof
-- SNMP works" as covering both a failing agent and an absent one.
--
-- Windows differ deliberately. ICMP keeps its 3 minutes. The SNMP side looks
-- back 10 minutes because the family cadences differ — traffic every 60s but
-- health every 300s — and a window that cannot span the slowest scheduled
-- family would read a healthy device as having no SNMP and escalate a ping
-- failure to a critical.
--
-- Verified against synthetic series before shipping: both-fail and no-SNMP
-- devices match device-down, an SNMP-healthy device matches icmp-down only,
-- and a healthy device matches neither.
UPDATE alerting.alert_rules SET
  expr = '(max by (device_id) (max_over_time(netinv_icmp_up[3m])) == 0)'
      || ' unless on (device_id) '
      || '(max by (device_id) (max_over_time(netinv_poll_success{family=~"traffic|health|sync"}[10m])) == 1)',
  annotations = '{"summary":"Device {{device}} is unreachable — no ICMP reply for 3m and no successful SNMP poll for 10m"}'
WHERE id = 'ar_device_down' AND is_builtin;

INSERT INTO alerting.alert_rules
  (id, tenant_id, name, kind, severity, expr, condition, is_builtin, annotations)
VALUES
  ('ar_icmp_down', 't_default', 'Device not answering ping (SNMP still working)',
   'threshold', 'warning',
   '(max by (device_id) (max_over_time(netinv_icmp_up[3m])) == 0)'
   || ' and on (device_id) '
   || '(max by (device_id) (max_over_time(netinv_poll_success{family=~"traffic|health|sync"}[10m])) == 1)',
   '{"operator":"==","value":0,"for_s":180}',
   true,
   '{"summary":"Device {{device}} stopped answering ping while SNMP still works — availability data is unreliable for it. Usual causes: a management ACL permitting SNMP but not ICMP, control-plane ICMP policing on the device, or the poller''s own ICMP path (see ar_icmp_probe_error)."}')
ON CONFLICT DO NOTHING;

-- +goose Down
UPDATE alerting.alert_rules SET
  expr = 'max_over_time(netinv_icmp_up[3m]) == 0',
  annotations = '{"summary":"Device {{device}} unreachable (ICMP failed 3 cycles)"}'
WHERE id = 'ar_device_down' AND is_builtin;
DELETE FROM alerting.alert_rules WHERE id = 'ar_icmp_down';
