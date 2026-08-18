-- +goose Up
-- The two interface-utilization rules have never been able to fire.
--
-- Since migration 0005 they have read `netinv_if_utilization_percent`, and
-- nothing in the product has ever written that metric. The collector publishes
-- counters — netinv_if_in_octets_total, netinv_if_out_octets_total — and
-- netinv_if_speed_bps; utilization is derived from them at query time by the
-- frontend and by the weathermap, and was never materialised as a series. So
-- both rules evaluate an empty selector, match nothing, and stay silent
-- forever. A rule that cannot fire looks exactly like a network that is
-- behaving, which is why this survived from Sprint 9 to now: "interface over
-- 80%" is one of the promises an operator most relies on, and it has been
-- decorative.
--
-- The expressions now compute utilization the same way every reader of these
-- counters already does:
--
--   * label_set(...) around each direction, then `or`. MetricsQL's `or`
--     matches on labels *excluding* __name__, so in and out — identical in
--     every other label — would collapse into one series and the out
--     direction would vanish silently. The explicit dir label keeps both, and
--     max by (device_id, if_index) then takes the busier one, which is what
--     "utilization" means on a full-duplex link.
--   * a 15m rate window rather than max_over_time over a subquery. The rule
--     is "above the threshold for 15 minutes" (condition for_s 900) and a
--     15-minute average rate expresses that directly, without the cost of
--     evaluating an inner query per step.
--   * `(netinv_if_speed_bps > 0)` as the right operand, not group_left with a
--     filter — group_left takes a label list, not an expression. The > 0 is
--     load-bearing: an interface whose speed is unknown or zero would divide
--     to +Inf and fire permanently, and unknown speed is common on exactly
--     the ports that matter (a PPPoE session has no ifSpeed at all).
--
-- Verified against the pilot before shipping: 108 of its interfaces have a
-- computable utilization, and the values match what the device detail graphs
-- show. The remainder are ports with no speed, correctly excluded rather than
-- reported as 0% or as infinite.
UPDATE alerting.alert_rules SET
  expr = '100 * max by (device_id, if_index) ('
      || 'label_set(rate(netinv_if_in_octets_total[15m]) * 8, "dir", "in")'
      || ' or label_set(rate(netinv_if_out_octets_total[15m]) * 8, "dir", "out")'
      || ') / on (device_id, if_index) (netinv_if_speed_bps > 0) > 80'
WHERE id = 'ar_util_warn' AND is_builtin;

UPDATE alerting.alert_rules SET
  expr = '100 * max by (device_id, if_index) ('
      || 'label_set(rate(netinv_if_in_octets_total[15m]) * 8, "dir", "in")'
      || ' or label_set(rate(netinv_if_out_octets_total[15m]) * 8, "dir", "out")'
      || ') / on (device_id, if_index) (netinv_if_speed_bps > 0) > 90'
WHERE id = 'ar_util_crit' AND is_builtin;

-- +goose Down
UPDATE alerting.alert_rules
  SET expr = 'max_over_time(netinv_if_utilization_percent[15m]) > 80'
WHERE id = 'ar_util_warn' AND is_builtin;
UPDATE alerting.alert_rules
  SET expr = 'max_over_time(netinv_if_utilization_percent[15m]) > 90'
WHERE id = 'ar_util_crit' AND is_builtin;
