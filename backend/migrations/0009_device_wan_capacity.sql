-- +goose Up
-- Operator-stated uplink rate for a site (FR-MAP-08).
--
-- SNMP cannot report it: a PPPoE session has no fixed negotiated rate, so
-- ifSpeed and ifHighSpeed on the ppp interface both read 0, and the physical
-- port underneath reports the link to the ONT rather than the subscribed
-- service. Weathermap links over tunnels have no capacity to divide by unless
-- someone says what the circuit is worth.
ALTER TABLE inventory.devices
  ADD COLUMN wan_capacity_bps bigint
  CONSTRAINT devices_wan_capacity_positive CHECK (wan_capacity_bps IS NULL OR wan_capacity_bps > 0);

COMMENT ON COLUMN inventory.devices.wan_capacity_bps IS
  'Subscribed uplink rate in bits/s, stated by an operator. NULL when unknown. A weathermap link whose interfaces report no speed takes min() of the capacity at each end — the slower end being the bottleneck (FR-MAP-08).';

-- +goose Down
ALTER TABLE inventory.devices DROP COLUMN wan_capacity_bps;
