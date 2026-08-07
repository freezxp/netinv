-- +goose Up
-- Forced password change on first login for seeded accounts (doc 20 §4).
ALTER TABLE iam.users
  ADD COLUMN password_change_required boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE iam.users DROP COLUMN password_change_required;
