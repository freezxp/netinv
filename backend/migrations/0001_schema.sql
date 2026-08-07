-- +goose Up
-- NetInv schema v1 — physical design per docs/08-database-design.md.
-- Schema-per-bounded-context; ULIDs as text PKs; timestamptz UTC throughout.

CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE SCHEMA iam;
CREATE SCHEMA platform;
CREATE SCHEMA inventory;
CREATE SCHEMA maps;
CREATE SCHEMA alerting;
CREATE SCHEMA notify;
CREATE SCHEMA audit;
CREATE SCHEMA config;

-- ---------- enums ----------
CREATE TYPE iam.user_status AS ENUM ('active','locked','deactivated');
CREATE TYPE platform.poller_status AS ENUM ('pending','active','disabled');
CREATE TYPE platform.poll_family AS ENUM ('traffic','health','icmp','sync');
CREATE TYPE platform.sync_status AS ENUM ('running','ok','partial','failed');
CREATE TYPE platform.discovered_state AS ENUM ('pending','approved','ignored');
CREATE TYPE inventory.credential_kind AS ENUM ('snmp_v2c','snmp_v3');
CREATE TYPE inventory.device_status AS ENUM ('pending','active','unreachable','disabled','retired');
CREATE TYPE inventory.presence_state AS ENUM ('present','missing','removed');
CREATE TYPE inventory.component_kind AS ENUM
  ('cpu','memory_pool','temp_sensor','fan','psu','module','stack_member','optic');
CREATE TYPE inventory.change_kind AS ENUM ('created','updated','removed','status');
CREATE TYPE alerting.severity AS ENUM ('critical','warning','info');
CREATE TYPE alerting.rule_kind AS ENUM ('threshold','state','inventory');
CREATE TYPE alerting.alert_state AS ENUM ('firing','acknowledged','resolved','flapping');
CREATE TYPE notify.channel_kind AS ENUM ('email','webhook','slack');
CREATE TYPE notify.delivery_status AS ENUM ('ok','retrying','failed');

-- ---------- platform ----------
CREATE TABLE platform.tenants (
  id         text PRIMARY KEY,
  name       text NOT NULL,
  status     text NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE platform.sites (
  id             text PRIMARY KEY,
  tenant_id      text NOT NULL REFERENCES platform.tenants(id),
  name           text NOT NULL,
  parent_site_id text REFERENCES platform.sites(id),
  location       text,
  contact        text,
  status         text NOT NULL DEFAULT 'active',
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);
CREATE INDEX sites_parent_idx ON platform.sites (parent_site_id);

CREATE TABLE platform.pollers (
  id                    text PRIMARY KEY,
  tenant_id             text NOT NULL REFERENCES platform.tenants(id),
  site_id               text NOT NULL REFERENCES platform.sites(id),
  name                  text NOT NULL,
  enrollment_token_hash text,
  status                platform.poller_status NOT NULL DEFAULT 'pending',
  version               text,
  last_heartbeat_at     timestamptz,
  stats                 jsonb NOT NULL DEFAULT '{}',
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pollers_site_idx ON platform.pollers (site_id);
CREATE INDEX pollers_heartbeat_idx ON platform.pollers (last_heartbeat_at)
  WHERE status = 'active';

CREATE TABLE platform.connectors (
  id                      text PRIMARY KEY,
  vendor                  text NOT NULL,
  display_name            text NOT NULL,
  version                 text NOT NULL,
  capabilities            jsonb NOT NULL DEFAULT '[]',
  sys_object_id_prefixes  jsonb NOT NULL DEFAULT '[]',
  enabled                 boolean NOT NULL DEFAULT true
);

CREATE TABLE platform.polling_profiles (
  id                   text PRIMARY KEY,
  tenant_id            text NOT NULL REFERENCES platform.tenants(id),
  name                 text NOT NULL,
  traffic_interval_s   integer NOT NULL DEFAULT 60  CHECK (traffic_interval_s >= 10),
  health_interval_s    integer NOT NULL DEFAULT 300 CHECK (health_interval_s >= 10),
  icmp_interval_s      integer NOT NULL DEFAULT 30  CHECK (icmp_interval_s >= 5),
  sync_interval_s      integer NOT NULL DEFAULT 21600 CHECK (sync_interval_s >= 300),
  snmp_timeout_ms      integer NOT NULL DEFAULT 5000,
  snmp_retries         integer NOT NULL DEFAULT 2,
  bulk_max_repetitions integer NOT NULL DEFAULT 25,
  families_enabled     jsonb NOT NULL DEFAULT '["traffic","health","icmp","sync"]',
  is_default           boolean NOT NULL DEFAULT false,
  UNIQUE (tenant_id, name)
);

-- ---------- iam ----------
CREATE TABLE iam.users (
  id            text PRIMARY KEY,
  tenant_id     text NOT NULL REFERENCES platform.tenants(id),
  username      citext NOT NULL UNIQUE,
  email         citext NOT NULL UNIQUE,
  password_hash text NOT NULL,
  display_name  text NOT NULL,
  status        iam.user_status NOT NULL DEFAULT 'active',
  mfa           jsonb,
  last_login_at timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE iam.roles (
  id          text PRIMARY KEY,
  name        text NOT NULL UNIQUE,
  description text,
  permissions jsonb NOT NULL DEFAULT '[]',
  is_builtin  boolean NOT NULL DEFAULT false
);

CREATE TABLE iam.user_roles (
  user_id    text NOT NULL REFERENCES iam.users(id) ON DELETE CASCADE,
  role_id    text NOT NULL REFERENCES iam.roles(id),
  granted_by text REFERENCES iam.users(id),
  granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, role_id)
);

CREATE TABLE iam.refresh_tokens (
  id          text PRIMARY KEY,
  user_id     text NOT NULL REFERENCES iam.users(id) ON DELETE CASCADE,
  token_hash  text NOT NULL UNIQUE,
  family_id   text NOT NULL,
  issued_at   timestamptz NOT NULL DEFAULT now(),
  expires_at  timestamptz NOT NULL,
  rotated_at  timestamptz,
  revoked_at  timestamptz,
  client_meta jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX refresh_tokens_user_idx ON iam.refresh_tokens (user_id);
CREATE INDEX refresh_tokens_family_idx ON iam.refresh_tokens (family_id);
CREATE INDEX refresh_tokens_expiry_idx ON iam.refresh_tokens (expires_at)
  WHERE revoked_at IS NULL;

CREATE TABLE iam.api_tokens (
  id           text PRIMARY KEY,
  user_id      text NOT NULL REFERENCES iam.users(id) ON DELETE CASCADE,
  name         text NOT NULL,
  token_hash   text NOT NULL UNIQUE,
  scopes       jsonb NOT NULL DEFAULT '[]',
  expires_at   timestamptz,
  last_used_at timestamptz,
  revoked_at   timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_tokens_user_idx ON iam.api_tokens (user_id);

-- ---------- inventory ----------
CREATE TABLE inventory.credentials (
  id          text PRIMARY KEY,
  tenant_id   text NOT NULL REFERENCES platform.tenants(id),
  name        text NOT NULL,
  kind        inventory.credential_kind NOT NULL,
  enc_payload bytea NOT NULL,
  enc_dek     bytea NOT NULL,
  key_version integer NOT NULL DEFAULT 1,
  meta        jsonb NOT NULL DEFAULT '{}',
  created_by  text REFERENCES iam.users(id),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);

CREATE TABLE inventory.devices (
  id            text PRIMARY KEY,
  tenant_id     text NOT NULL REFERENCES platform.tenants(id),
  site_id       text NOT NULL REFERENCES platform.sites(id),
  connector_id  text NOT NULL REFERENCES platform.connectors(id),
  credential_id text NOT NULL REFERENCES inventory.credentials(id) ON DELETE RESTRICT,
  profile_id    text NOT NULL REFERENCES platform.polling_profiles(id),
  name          text NOT NULL,
  mgmt_ip       inet NOT NULL,
  status        inventory.device_status NOT NULL DEFAULT 'pending',
  sys_name      text, sys_descr text, sys_object_id text,
  sys_location  text, sys_contact text,
  vendor        text, model text, serial_number text, os_version text,
  uptime_basis  timestamptz,
  tags          jsonb NOT NULL DEFAULT '[]',
  notes         text,
  attrs         jsonb NOT NULL DEFAULT '{}',
  retired_at    timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX devices_ip_unique ON inventory.devices (tenant_id, mgmt_ip)
  WHERE status != 'retired';
CREATE INDEX devices_site_idx ON inventory.devices (site_id);
CREATE INDEX devices_connector_idx ON inventory.devices (connector_id);
CREATE INDEX devices_status_idx ON inventory.devices (status);
CREATE INDEX devices_tags_gin ON inventory.devices USING gin (tags);
CREATE INDEX devices_search_fts ON inventory.devices USING gin (
  to_tsvector('simple',
    coalesce(name,'') || ' ' || coalesce(sys_name,'') || ' ' ||
    coalesce(sys_descr,'') || ' ' || coalesce(model,'') || ' ' ||
    coalesce(serial_number,''))
);
CREATE INDEX devices_name_trgm ON inventory.devices USING gin (name gin_trgm_ops);
CREATE INDEX devices_serial_trgm ON inventory.devices
  USING gin (serial_number gin_trgm_ops);

CREATE TABLE inventory.interfaces (
  id            text PRIMARY KEY,
  device_id     text NOT NULL REFERENCES inventory.devices(id) ON DELETE CASCADE,
  if_index      integer NOT NULL,
  name          text,
  alias         text,
  descr         text,
  if_type       integer,
  mtu           integer,
  speed_bps     bigint,
  phys_address  macaddr,
  admin_status  integer,
  oper_status   integer,
  monitor       boolean NOT NULL DEFAULT true,
  state         inventory.presence_state NOT NULL DEFAULT 'present',
  missing_since timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (device_id, if_index)
);
CREATE INDEX interfaces_device_idx ON inventory.interfaces (device_id);
CREATE INDEX interfaces_alias_trgm ON inventory.interfaces USING gin (alias gin_trgm_ops);

CREATE TABLE inventory.device_components (
  id           text PRIMARY KEY,
  device_id    text NOT NULL REFERENCES inventory.devices(id) ON DELETE CASCADE,
  kind         inventory.component_kind NOT NULL,
  source_index text NOT NULL,
  name         text NOT NULL,
  model        text, serial text,
  state        inventory.presence_state NOT NULL DEFAULT 'present',
  attrs        jsonb NOT NULL DEFAULT '{}',
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (device_id, kind, source_index)
);

CREATE TABLE inventory.device_groups (
  id             text PRIMARY KEY,
  tenant_id      text NOT NULL REFERENCES platform.tenants(id),
  name           text NOT NULL,
  description    text,
  dynamic_filter jsonb,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);

CREATE TABLE inventory.device_group_members (
  group_id  text NOT NULL REFERENCES inventory.device_groups(id) ON DELETE CASCADE,
  device_id text NOT NULL REFERENCES inventory.devices(id) ON DELETE CASCADE,
  PRIMARY KEY (group_id, device_id)
);

CREATE TABLE inventory.asset_history (
  id          bigint GENERATED ALWAYS AS IDENTITY,
  device_id   text NOT NULL,
  object_kind text NOT NULL,
  object_id   text NOT NULL,
  field       text NOT NULL,
  old_value   text,
  new_value   text,
  change_kind inventory.change_kind NOT NULL,
  sync_run_id text,
  detected_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, detected_at)
) PARTITION BY RANGE (detected_at);
CREATE TABLE inventory.asset_history_default
  PARTITION OF inventory.asset_history DEFAULT;
CREATE INDEX asset_history_device_idx
  ON inventory.asset_history (device_id, detected_at DESC);

CREATE TABLE inventory.topology_links (
  id            text PRIMARY KEY,
  a_device_id   text NOT NULL REFERENCES inventory.devices(id) ON DELETE CASCADE,
  a_if_id       text REFERENCES inventory.interfaces(id) ON DELETE SET NULL,
  b_device_id   text REFERENCES inventory.devices(id) ON DELETE SET NULL,
  b_if_id       text REFERENCES inventory.interfaces(id) ON DELETE SET NULL,
  b_sysname     text, b_port_descr text, b_chassis_id text,
  protocol      text NOT NULL,
  state         text NOT NULL DEFAULT 'active',
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (a_device_id, a_if_id, b_chassis_id, b_port_descr)
);
CREATE INDEX topology_links_b_idx ON inventory.topology_links (b_device_id);

-- ---------- platform (device-dependent) ----------
CREATE TABLE platform.polling_schedule (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  device_id   text NOT NULL REFERENCES inventory.devices(id) ON DELETE CASCADE,
  family      platform.poll_family NOT NULL,
  interval_s  integer NOT NULL,
  next_due_at timestamptz NOT NULL,
  last_run_at timestamptz,
  last_status text,
  enabled     boolean NOT NULL DEFAULT true,
  UNIQUE (device_id, family)
);
CREATE INDEX polling_schedule_due_idx ON platform.polling_schedule (next_due_at)
  WHERE enabled;

CREATE TABLE platform.sync_runs (
  id            text PRIMARY KEY,
  device_id     text NOT NULL REFERENCES inventory.devices(id) ON DELETE CASCADE,
  trigger       text NOT NULL,
  started_at    timestamptz NOT NULL DEFAULT now(),
  finished_at   timestamptz,
  status        platform.sync_status NOT NULL DEFAULT 'running',
  changes_count integer NOT NULL DEFAULT 0,
  error         text,
  stats         jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX sync_runs_device_idx ON platform.sync_runs (device_id, started_at DESC);

CREATE TABLE platform.discovery_rules (
  id             text PRIMARY KEY,
  tenant_id      text NOT NULL REFERENCES platform.tenants(id),
  site_id        text NOT NULL REFERENCES platform.sites(id),
  cidr           cidr NOT NULL,
  credential_ids jsonb NOT NULL DEFAULT '[]',
  schedule_cron  text,
  enabled        boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE platform.discovered_devices (
  id                       text PRIMARY KEY,
  rule_id                  text NOT NULL REFERENCES platform.discovery_rules(id) ON DELETE CASCADE,
  ip                       inet NOT NULL,
  sys_object_id            text,
  sys_descr                text,
  matched_connector_id     text REFERENCES platform.connectors(id),
  responding_credential_id text,
  state                    platform.discovered_state NOT NULL DEFAULT 'pending',
  seen_first_at            timestamptz NOT NULL DEFAULT now(),
  seen_last_at             timestamptz NOT NULL DEFAULT now(),
  UNIQUE (rule_id, ip)
);

-- ---------- maps ----------
CREATE TABLE maps.maps (
  id            text PRIMARY KEY,
  tenant_id     text NOT NULL REFERENCES platform.tenants(id),
  name          text NOT NULL,
  description   text,
  background    jsonb,
  options       jsonb NOT NULL DEFAULT '{}',
  min_role      text NOT NULL DEFAULT 'readonly',
  published_rev integer NOT NULL DEFAULT 0,
  created_by    text REFERENCES iam.users(id),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);

CREATE TABLE maps.map_revisions (
  map_id     text NOT NULL REFERENCES maps.maps(id) ON DELETE CASCADE,
  rev        integer NOT NULL,
  state      text NOT NULL DEFAULT 'draft',
  definition jsonb NOT NULL,
  saved_by   text REFERENCES iam.users(id),
  saved_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (map_id, rev)
);

CREATE TABLE maps.map_links (
  map_id   text NOT NULL REFERENCES maps.maps(id) ON DELETE CASCADE,
  link_key text NOT NULL,
  a_if_id  text REFERENCES inventory.interfaces(id) ON DELETE SET NULL,
  b_if_id  text REFERENCES inventory.interfaces(id) ON DELETE SET NULL,
  PRIMARY KEY (map_id, link_key)
);
CREATE INDEX map_links_a_if_idx ON maps.map_links (a_if_id);

-- ---------- alerting ----------
CREATE TABLE alerting.alert_rules (
  id          text PRIMARY KEY,
  tenant_id   text NOT NULL REFERENCES platform.tenants(id),
  name        text NOT NULL,
  kind        alerting.rule_kind NOT NULL,
  severity    alerting.severity NOT NULL,
  expr        text,
  condition   jsonb NOT NULL DEFAULT '{}',
  scope       jsonb NOT NULL DEFAULT '{"all": true}',
  enabled     boolean NOT NULL DEFAULT true,
  is_builtin  boolean NOT NULL DEFAULT false,
  annotations jsonb NOT NULL DEFAULT '{}',
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);
CREATE INDEX alert_rules_enabled_idx ON alerting.alert_rules (enabled);

CREATE TABLE alerting.alert_instances (
  id           text PRIMARY KEY,
  rule_id      text NOT NULL REFERENCES alerting.alert_rules(id),
  fingerprint  text NOT NULL,
  state        alerting.alert_state NOT NULL DEFAULT 'firing',
  severity     alerting.severity NOT NULL,
  device_id    text REFERENCES inventory.devices(id) ON DELETE SET NULL,
  interface_id text REFERENCES inventory.interfaces(id) ON DELETE SET NULL,
  labels       jsonb NOT NULL DEFAULT '{}',
  value        double precision,
  fired_at     timestamptz NOT NULL DEFAULT now(),
  acked_at     timestamptz,
  acked_by     text REFERENCES iam.users(id),
  ack_comment  text,
  resolved_at  timestamptz,
  flap_count   integer NOT NULL DEFAULT 0,
  last_eval_at timestamptz
);
CREATE UNIQUE INDEX alert_instances_live_unique
  ON alerting.alert_instances (rule_id, fingerprint)
  WHERE state != 'resolved';
CREATE INDEX alert_instances_panel_idx
  ON alerting.alert_instances (state, severity, fired_at DESC);
CREATE INDEX alert_instances_device_idx ON alerting.alert_instances (device_id);

CREATE TABLE alerting.alert_events (
  id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  alert_id text NOT NULL REFERENCES alerting.alert_instances(id) ON DELETE CASCADE,
  event    text NOT NULL,
  actor_id text REFERENCES iam.users(id),
  detail   jsonb NOT NULL DEFAULT '{}',
  at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alert_events_alert_idx ON alerting.alert_events (alert_id, at);

CREATE TABLE alerting.silences (
  id         text PRIMARY KEY,
  tenant_id  text NOT NULL REFERENCES platform.tenants(id),
  scope      jsonb NOT NULL,
  reason     text NOT NULL,
  starts_at  timestamptz NOT NULL,
  ends_at    timestamptz NOT NULL,
  created_by text NOT NULL REFERENCES iam.users(id),
  revoked_at timestamptz
);
CREATE INDEX silences_active_idx ON alerting.silences (ends_at)
  WHERE revoked_at IS NULL;

-- ---------- notify ----------
CREATE TABLE notify.channels (
  id          text PRIMARY KEY,
  tenant_id   text NOT NULL REFERENCES platform.tenants(id),
  name        text NOT NULL,
  kind        notify.channel_kind NOT NULL,
  config      jsonb NOT NULL DEFAULT '{}',
  enc_secret  bytea,
  enc_dek     bytea,
  key_version integer,
  enabled     boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);

CREATE TABLE notify.policies (
  id          text PRIMARY KEY,
  tenant_id   text NOT NULL REFERENCES platform.tenants(id),
  name        text NOT NULL,
  match       jsonb NOT NULL DEFAULT '{}',
  channel_ids jsonb NOT NULL DEFAULT '[]',
  enabled     boolean NOT NULL DEFAULT true,
  ord         integer NOT NULL DEFAULT 0
);

CREATE TABLE notify.deliveries (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  alert_id     text NOT NULL REFERENCES alerting.alert_instances(id) ON DELETE CASCADE,
  channel_id   text NOT NULL REFERENCES notify.channels(id) ON DELETE CASCADE,
  event        text NOT NULL,
  status       notify.delivery_status NOT NULL DEFAULT 'retrying',
  attempts     integer NOT NULL DEFAULT 0,
  last_error   text,
  queued_at    timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz
);
CREATE INDEX deliveries_alert_idx ON notify.deliveries (alert_id);
CREATE INDEX deliveries_failed_idx ON notify.deliveries (status)
  WHERE status = 'failed';

-- ---------- audit (append-only; app role grants restricted in 0003) ----------
CREATE TABLE audit.events (
  id            bigint GENERATED ALWAYS AS IDENTITY,
  tenant_id     text NOT NULL,
  at            timestamptz NOT NULL DEFAULT now(),
  actor_kind    text NOT NULL,
  actor_id      text,
  action        text NOT NULL,
  resource_kind text,
  resource_id   text,
  before        jsonb,
  after         jsonb,
  source_ip     inet,
  user_agent    text,
  trace_id      text,
  detail        jsonb NOT NULL DEFAULT '{}',
  PRIMARY KEY (id, at)
) PARTITION BY RANGE (at);
CREATE TABLE audit.events_default PARTITION OF audit.events DEFAULT;
CREATE INDEX audit_events_at_idx ON audit.events (at DESC);
CREATE INDEX audit_events_actor_idx ON audit.events (actor_id, at DESC);
CREATE INDEX audit_events_action_idx ON audit.events (action, at DESC);
CREATE INDEX audit_events_resource_idx ON audit.events (resource_kind, resource_id);

-- ---------- config ----------
CREATE TABLE config.settings (
  key          text PRIMARY KEY,
  tenant_id    text NOT NULL REFERENCES platform.tenants(id),
  value        jsonb NOT NULL,
  value_schema text NOT NULL,
  updated_by   text REFERENCES iam.users(id),
  updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE config.user_preferences (
  user_id      text PRIMARY KEY REFERENCES iam.users(id) ON DELETE CASCADE,
  theme        text NOT NULL DEFAULT 'system',
  landing_page text,
  timezone     text,
  prefs        jsonb NOT NULL DEFAULT '{}'
);

-- +goose Down
DROP SCHEMA config CASCADE;
DROP SCHEMA audit CASCADE;
DROP SCHEMA notify CASCADE;
DROP SCHEMA alerting CASCADE;
DROP SCHEMA maps CASCADE;
DROP SCHEMA inventory CASCADE;
DROP SCHEMA iam CASCADE;
DROP SCHEMA platform CASCADE;
