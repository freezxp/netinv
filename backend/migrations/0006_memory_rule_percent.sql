-- +goose Up
-- Vendors report memory as % (Juniper buffer, Huawei hwEntityMemUsage) or
-- used/total bytes (Cisco pools). Connectors normalize to
-- netinv_device_memory_percent; the builtin rule follows (Sprint 17).
UPDATE alerting.alert_rules
SET expr = 'netinv_device_memory_percent > 90'
WHERE id = 'ar_memory' AND is_builtin;

-- +goose Down
UPDATE alerting.alert_rules
SET expr = '(netinv_device_memory_used_bytes / netinv_device_memory_total_bytes) * 100 > 90'
WHERE id = 'ar_memory' AND is_builtin;
