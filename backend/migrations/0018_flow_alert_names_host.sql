-- +goose Up
-- A firing flow alert named an address and nothing else:
--
--   "Flow exporter 192.0.2.7 has sent nothing for 20m"
--
-- Every other alert family names a device, because device/interface metrics
-- carry `device`, `device_id` and `site` labels. Flow series carry none of them
-- — they are keyed by the datagram's source address (doc 34 §3.1) — so the one
-- alert most likely to reach someone who does not have the fleet's addressing
-- memorised was the one that told them least. It also meant a device_id was
-- never set on the instance, so the alert list offered no graph link.
--
-- The alerter now resolves the exporter address to its device before firing
-- (app.ExporterResolver): first through devices.attrs->'flow_exporters', then
-- falling back to the management address, which is what a gateway exports from
-- when the collector sits on its own LAN. `device` is set either way — to the
-- claiming device, or to the raw address when nothing claims it — so this
-- summary reads correctly in both cases, and an unclaimed exporter is visible
-- as an address where a name should be, which is itself the thing to fix.
--
-- Resolution happens after fingerprinting, so claiming an exporter onto a
-- device later does not resolve the live alert and fire a duplicate.
UPDATE alerting.alert_rules
   SET annotations = jsonb_set(annotations, '{summary}',
       '"No flow from {{device}} for 20m — exporter {{exporter}}"')
 WHERE id = 'ar_flow_exporter_stale' AND is_builtin;

-- +goose Down
UPDATE alerting.alert_rules
   SET annotations = jsonb_set(annotations, '{summary}',
       '"Flow exporter {{exporter}} has sent nothing for 20m"')
 WHERE id = 'ar_flow_exporter_stale' AND is_builtin;
