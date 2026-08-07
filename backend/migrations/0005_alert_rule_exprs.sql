-- +goose Up
-- Builtin rules become MetricsQL-evaluated now that the ICMP/traffic metric
-- families exist (Sprint 9). Rules whose event sources don't exist yet
-- (components S17, reboot/poll-auth events S10+) stay disabled until then.

UPDATE alerting.alert_rules SET kind = 'threshold',
  expr = 'max_over_time(netinv_icmp_up[3m]) == 0',
  condition = '{"operator":"==","value":0,"for_s":180}'
WHERE id = 'ar_device_down' AND is_builtin;

UPDATE alerting.alert_rules SET kind = 'threshold',
  expr = '(netinv_if_oper_status == 2) * (netinv_if_admin_status == 1) == 2',
  condition = '{"operator":"==","value":2}'
WHERE id = 'ar_if_down' AND is_builtin;

UPDATE alerting.alert_rules SET enabled = false
WHERE id IN ('ar_psu_fan','ar_reboot','ar_poll_auth') AND is_builtin;

-- +goose Down
UPDATE alerting.alert_rules SET kind = 'state', expr = NULL,
  condition = '{"event":"device_down","for_cycles":3}'
WHERE id = 'ar_device_down' AND is_builtin;
UPDATE alerting.alert_rules SET kind = 'state', expr = NULL,
  condition = '{"event":"if_oper_down_admin_up"}'
WHERE id = 'ar_if_down' AND is_builtin;
UPDATE alerting.alert_rules SET enabled = true
WHERE id IN ('ar_psu_fan','ar_reboot','ar_poll_auth') AND is_builtin;
