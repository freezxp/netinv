-- +goose Up
-- Consecutive-miss counter driving the missing->removed lifecycle (FR-SYNC-03).
ALTER TABLE inventory.interfaces
  ADD COLUMN miss_streak integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE inventory.interfaces DROP COLUMN miss_streak;
