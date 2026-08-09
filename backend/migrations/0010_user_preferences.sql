-- +goose Up
-- Per-user UI preferences, of which the dashboard layout is the first.
--
-- A table rather than a column on iam.users: preferences are read on almost
-- every page load and written rarely, and keeping them separate means a
-- preferences write never touches the row that authentication reads.
--
-- The shape is deliberately opaque jsonb. What a dashboard layout contains
-- will change as panels are added, and a migration per panel type would be a
-- poor trade for data that only the client interprets.
CREATE TABLE IF NOT EXISTS iam.user_preferences (
    user_id    text PRIMARY KEY REFERENCES iam.users(id) ON DELETE CASCADE,
    prefs      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS iam.user_preferences;
