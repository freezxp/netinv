-- +goose Up
-- Discovery captures a device's sysName, but there was nowhere to keep it, so
-- approvals fell back to naming devices by IP. Store it properly.
ALTER TABLE platform.discovered_devices ADD COLUMN sys_name text;

-- +goose Down
ALTER TABLE platform.discovered_devices DROP COLUMN sys_name;
