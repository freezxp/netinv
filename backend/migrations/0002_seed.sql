-- +goose Up
-- Seed data per doc 08 §notes: default tenant, builtin roles (doc 20 §5),
-- default polling profile, builtin alert rule pack (FR-ALR-07), settings.
-- The initial admin user is created by the API at first boot (it needs Argon2id
-- hashing and a forced-password-change flow — not expressible in SQL).
-- Connector catalog rows are seeded by the app from the compiled registry.

INSERT INTO platform.tenants (id, name) VALUES ('t_default', 'Default');

INSERT INTO iam.roles (id, name, description, permissions, is_builtin) VALUES
  ('role_admin', 'admin', 'Full administrative access', '["*"]', true),
  ('role_operator', 'operator', 'Operate: ack alerts, edit maps, manage devices',
   '["devices:read","devices:write","metrics:read","maps:read","maps:write",
     "alerts:read","alerts:ack","alerts:admin","platform:read","exports:run"]', true),
  ('role_readonly', 'readonly', 'View everything except credentials and audit',
   '["devices:read","metrics:read","maps:read","alerts:read","platform:read",
     "exports:run"]', true),
  ('role_auditor', 'auditor', 'View-only including audit logs',
   '["devices:read","metrics:read","maps:read","alerts:read","platform:read",
     "audit:read","exports:run"]', true);

INSERT INTO platform.polling_profiles (id, tenant_id, name, is_default)
VALUES ('pp_default', 't_default', 'Default', true);

INSERT INTO platform.sites (id, tenant_id, name, location)
VALUES ('s_default', 't_default', 'Default Site', 'unset');

INSERT INTO alerting.alert_rules
  (id, tenant_id, name, kind, severity, expr, condition, is_builtin, annotations)
VALUES
  ('ar_device_down', 't_default', 'Device down', 'state', 'critical', NULL,
   '{"event":"device_down","for_cycles":3}', true,
   '{"summary":"Device {{device}} unreachable (ICMP failed 3 cycles)"}'),
  ('ar_if_down', 't_default', 'Interface down', 'state', 'warning', NULL,
   '{"event":"if_oper_down_admin_up"}', true,
   '{"summary":"Interface {{if_name}} on {{device}} oper down while admin up"}'),
  ('ar_util_warn', 't_default', 'Interface utilization > 80%', 'threshold', 'warning',
   'max_over_time(netinv_if_utilization_percent[15m]) > 80',
   '{"operator":">","value":80,"for_s":900}', true,
   '{"summary":"{{if_name}} on {{device}} above 80% for 15m"}'),
  ('ar_util_crit', 't_default', 'Interface utilization > 90%', 'threshold', 'critical',
   'max_over_time(netinv_if_utilization_percent[15m]) > 90',
   '{"operator":">","value":90,"for_s":900}', true,
   '{"summary":"{{if_name}} on {{device}} above 90% for 15m"}'),
  ('ar_cpu', 't_default', 'CPU > 85%', 'threshold', 'warning',
   'avg_over_time(netinv_device_cpu_percent[10m]) > 85',
   '{"operator":">","value":85,"for_s":600}', true,
   '{"summary":"CPU on {{device}} above 85% for 10m"}'),
  ('ar_memory', 't_default', 'Memory > 90%', 'threshold', 'warning',
   '(netinv_device_memory_used_bytes / netinv_device_memory_total_bytes) * 100 > 90',
   '{"operator":">","value":90,"for_s":600}', true,
   '{"summary":"Memory on {{device}} above 90%"}'),
  ('ar_psu_fan', 't_default', 'PSU or fan failure', 'state', 'critical', NULL,
   '{"event":"component_failed","kinds":["psu","fan"]}', true,
   '{"summary":"{{component}} failed on {{device}}"}'),
  ('ar_reboot', 't_default', 'Device rebooted', 'state', 'info', NULL,
   '{"event":"uptime_reset"}', true,
   '{"summary":"{{device}} rebooted (sysUpTime reset)"}'),
  ('ar_poll_auth', 't_default', 'SNMP authentication failing', 'state', 'warning', NULL,
   '{"event":"poll_auth_failure"}', true,
   '{"summary":"SNMP auth failures polling {{device}} — credential rotated?"}');

INSERT INTO config.settings (key, tenant_id, value, value_schema) VALUES
  ('retention', 't_default',
   '{"raw_days":90,"rollup_5m_months":13,"rollup_1h_years":3,"audit_months":12,"asset_history_months":24}',
   'netinv.settings.retention/1'),
  ('instance', 't_default', '{"name":"NetInv"}', 'netinv.settings.instance/1');

-- +goose Down
DELETE FROM config.settings WHERE key IN ('retention','instance');
DELETE FROM alerting.alert_rules WHERE is_builtin;
DELETE FROM platform.sites WHERE id = 's_default';
DELETE FROM platform.polling_profiles WHERE id = 'pp_default';
DELETE FROM iam.roles WHERE is_builtin;
DELETE FROM platform.tenants WHERE id = 't_default';
