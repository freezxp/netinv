-- +goose Up
-- Alert suppression for ports that have never been in service (FR-ALR-08).
--
-- A port that was never plugged in reports oper=down/admin=up forever and
-- alerts forever. Distinguishing "never worked" from "stopped working" needs
-- memory, and the honest place to keep it is a monotonic flag rather than a
-- MetricsQL lookback window: with a window, a genuinely failed link stops
-- alerting once its last healthy sample ages out, which is worse than the
-- noise it removes.
ALTER TABLE inventory.interfaces
  ADD COLUMN ever_up boolean NOT NULL DEFAULT false;

-- Backfill what we can prove right now: anything currently up has been up.
-- Interfaces already down keep false — we cannot know their history, and the
-- flag flips the first time sync sees them up.
UPDATE inventory.interfaces SET ever_up = true WHERE oper_status = 1;

COMMENT ON COLUMN inventory.interfaces.ever_up IS
  'True once sync has observed oper_status=up. Monotonic; never cleared. Interface-down alerts are suppressed while false (FR-ALR-08).';

-- +goose Down
ALTER TABLE inventory.interfaces DROP COLUMN ever_up;
