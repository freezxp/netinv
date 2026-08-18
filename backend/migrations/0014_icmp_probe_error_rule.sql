-- +goose Up
-- Availability alerting could stop working silently.
--
-- ar_device_down is `max_over_time(netinv_icmp_up[3m]) == 0`, which needs the
-- series to exist. When the probe itself failed — the socket could not be
-- opened, which is what an unprivileged ping does inside an LXC whose
-- net.ipv4.ping_group_range excludes the poller's gid — the poller wrote no
-- sample at all rather than a zero. An empty lookbehind produces no series, so
-- the rule matched nothing and stayed quiet. "We cannot ask" was
-- indistinguishable from "nothing to report", and the one alert an operator
-- most relies on was the one that went blind.
--
-- The poller now writes netinv_icmp_probe_error: 1 when the probe could not
-- run, 0 on every successful probe. It deliberately does not report the device
-- as down instead — the poller does not know whether it is, and paging someone
-- to look at healthy equipment because we failed to ask is the worse error.
--
-- This rule is the alarm for that state. It is a poller/host fault rather than
-- a device fault, so it is a warning rather than critical: nothing is known to
-- be broken in the network, but the thing that would have told you is.
--
-- ON CONFLICT DO NOTHING because 0012 taught the lesson the hard way: a
-- migration inserting fixed rows into a table with UNIQUE (tenant_id, name)
-- aborts if a deployment already holds that name, and an aborted migration
-- crashloops the api on every start. Seeding must tolerate a database that is
-- not a fresh install.
--
-- The conflict target is deliberately omitted rather than written as
-- ON CONFLICT (id): the constraint that would actually be hit on an existing
-- deployment is UNIQUE (tenant_id, name), not the primary key. Naming the
-- wrong one is the same bug with the appearance of a fix.
INSERT INTO alerting.alert_rules
  (id, tenant_id, name, kind, severity, expr, condition, is_builtin, annotations)
VALUES
  ('ar_icmp_probe_error', 't_default', 'Availability probe cannot run',
   'threshold', 'warning',
   'max_over_time(netinv_icmp_probe_error[5m]) == 1',
   '{"operator":"==","value":1,"for_s":300}',
   true,
   '{"summary":"The poller cannot send ICMP for {{device}} — availability is unknown, not good. Check net.ipv4.ping_group_range in the poller container, or set NETINV_ICMP_PRIVILEGED=1 with CAP_NET_RAW."}')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM alerting.alert_rules WHERE id = 'ar_icmp_probe_error';
