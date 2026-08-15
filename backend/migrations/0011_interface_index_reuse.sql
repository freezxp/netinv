-- +goose Up
-- An ifIndex identifies an interface among the ones a device presents *now*,
-- not for all time. Agents renumber: a pilot UniFi gateway rebooted and moved
-- ppp2 from ifIndex 76 to 41, where a long-retired row still sat holding 41.
--
-- The unconditional unique constraint made that unrepresentable, so the sync
-- transaction aborted on
--   duplicate key value violates unique constraint "interfaces_device_id_if_index_key"
-- and — because a failed sync result is requeued — retried every second
-- forever. Inventory froze at the pre-reboot topology, and every weathermap
-- link bound to a renumbered interface silently went flat, since it resolved
-- to a row whose ifIndex the device no longer had.
--
-- Uniqueness now applies only to present interfaces. Retired rows keep their
-- last-known ifIndex for history and stop blocking its reuse.
ALTER TABLE inventory.interfaces
    DROP CONSTRAINT interfaces_device_id_if_index_key;

CREATE UNIQUE INDEX interfaces_device_if_index_present_key
    ON inventory.interfaces (device_id, if_index)
    WHERE state = 'present';

-- +goose Down
DROP INDEX inventory.interfaces_device_if_index_present_key;

-- Restoring the stricter constraint can fail on data the partial index allowed
-- (a retired row sharing an ifIndex with a live one), which is exactly the
-- state this migration exists to permit. Retire the duplicates first, keeping
-- the present row.
DELETE FROM inventory.interfaces a
    USING inventory.interfaces b
    WHERE a.device_id = b.device_id
      AND a.if_index = b.if_index
      AND a.id <> b.id
      AND a.state <> 'present';

ALTER TABLE inventory.interfaces
    ADD CONSTRAINT interfaces_device_id_if_index_key UNIQUE (device_id, if_index);
