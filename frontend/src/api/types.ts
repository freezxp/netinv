// Hand-maintained API types matching doc 09. Replaced by openapi-typescript
// generation once the OpenAPI file is emitted from handlers (doc 25 gate).

export interface User {
  id: string;
  username: string;
  display_name: string;
  roles: string[];
  password_change_required?: boolean;
}

export interface LoginResponse {
  access_token: string;
  token_type: string;
  expires_in: number;
  user: User;
}

export interface Paged<T> {
  data: T[];
  next_cursor: string | null;
}

export interface Device {
  id: string;
  name: string;
  mgmt_ip: string;
  site_id: string;
  connector_id: string;
  credential_id: string;
  profile_id: string;
  status: "pending" | "active" | "unreachable" | "disabled" | "retired";
  sys_name?: string;
  vendor?: string;
  model?: string;
  serial_number?: string;
  os_version?: string;
  tags: string[];
  created_at: string;
  updated_at: string;
}

export interface Site {
  id: string;
  name: string;
  parent_site_id: string | null;
  location: string;
  contact: string;
  status: string;
}

export interface Alert {
  id: string;
  rule: { id: string; name: string };
  state: "firing" | "acknowledged" | "resolved" | "flapping";
  severity: "critical" | "warning" | "info";
  device_id?: string;
  labels: Record<string, string>;
  value: number;
  fired_at: string;
  duration_s: number;
  acked?: { by: string; at: string; comment: string };
}

export interface DashboardSummary {
  as_of: string;
  data: {
    devices: Record<string, number>;
    alerts: Record<string, number>;
    availability_24h?: number;
    throughput_bps: { in?: number; out?: number };
  };
}
