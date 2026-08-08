// Package wire holds cross-service message contracts carried over RabbitMQ
// (doc 05 §4–5). These types are the compatibility surface between scheduler,
// pollers, and ingesters — change them additively only (±1 version skew,
// doc 10 §3).
package wire

import "time"

// SNMPCred is the decrypted credential embedded per-job by the scheduler and
// held in poller memory only (doc 20 §6). It never appears in logs or events.
type SNMPCred struct {
	Version   string `json:"version"` // v2c | v3
	Community string `json:"community,omitempty"`
	Username  string `json:"username,omitempty"`
	AuthProto string `json:"auth_proto,omitempty"`
	AuthPass  string `json:"auth_pass,omitempty"`
	PrivProto string `json:"priv_proto,omitempty"`
	PrivPass  string `json:"priv_pass,omitempty"`
	Context   string `json:"context,omitempty"`
}

type PollJob struct {
	JobID       string    `json:"job_id"`
	DeviceID    string    `json:"device_id"`
	SiteID      string    `json:"site_id"`
	Family      string    `json:"family"`            // traffic | health | icmp | sync
	Trigger     string    `json:"trigger,omitempty"` // scheduled | manual | onboarding
	MgmtIP      string    `json:"mgmt_ip"`
	Port        int       `json:"port"`
	ConnectorID string    `json:"connector_id"`
	Cred        SNMPCred  `json:"cred"`
	ScheduledAt time.Time `json:"scheduled_at"`
	IntervalS   int       `json:"interval_s"`
	TimeoutMS   int       `json:"timeout_ms"`
	Retries     int       `json:"retries"`
}

// ---- sync results (poller → API sync consumer, doc 11) ----

type SyncInterface struct {
	IfIndex     int    `json:"if_index"`
	Name        string `json:"name"`
	Alias       string `json:"alias"`
	Descr       string `json:"descr"`
	IfType      int    `json:"if_type"`
	MTU         int    `json:"mtu"`
	SpeedBPS    int64  `json:"speed_bps"`
	PhysAddress string `json:"phys_address"`
	AdminStatus int    `json:"admin_status"`
	OperStatus  int    `json:"oper_status"`
}

type SyncAdjacency struct {
	LocalIfIndex  int    `json:"local_if_index"`
	RemoteSysName string `json:"remote_sysname"`
	RemotePortID  string `json:"remote_port_id"`
	RemoteChassis string `json:"remote_chassis"`
	Protocol      string `json:"protocol"`
}

type SyncSnapshot struct {
	SysName     string          `json:"sys_name"`
	SysDescr    string          `json:"sys_descr"`
	SysObjectID string          `json:"sys_object_id"`
	SysLocation string          `json:"sys_location"`
	SysContact  string          `json:"sys_contact"`
	UptimeS     int64           `json:"uptime_s"`
	Interfaces  []SyncInterface `json:"interfaces"`
	Adjacencies []SyncAdjacency `json:"adjacencies,omitempty"`
}

// ---- subnet discovery (FR-SYNC-04, doc 11 §7) ----

// NamedCred is a candidate credential for a discovery sweep: the poller tries
// each in turn and reports which one answered.
type NamedCred struct {
	CredentialID string   `json:"credential_id"`
	Name         string   `json:"name"`
	Cred         SNMPCred `json:"cred"`
}

type DiscoveryJob struct {
	// Family is always "discovery" — sweeps share the site job queue with
	// polls, so the poller discriminates on this field.
	Family    string      `json:"family"`
	JobID     string      `json:"job_id"`
	RuleID    string      `json:"rule_id"`
	SiteID    string      `json:"site_id"`
	CIDR      string      `json:"cidr"`
	Port      int         `json:"port"`
	Creds     []NamedCred `json:"creds"`
	TimeoutMS int         `json:"timeout_ms"`
}

type DiscoveredHost struct {
	IP           string `json:"ip"`
	SysName      string `json:"sys_name"`
	SysDescr     string `json:"sys_descr"`
	SysObjectID  string `json:"sys_object_id"`
	CredentialID string `json:"credential_id"`
}

type DiscoveryResult struct {
	JobID    string           `json:"job_id"`
	RuleID   string           `json:"rule_id"`
	PollerID string           `json:"poller_id"`
	Scanned  int              `json:"scanned"`
	Found    []DiscoveredHost `json:"found"`
	Error    string           `json:"error,omitempty"`
}

// AlertEvent rides events.domain (rk alert.fired / alert.resolved) from the
// alerter to the notifier — fat event, no callback needed (doc 05 §8).
type AlertEvent struct {
	Event    string            `json:"event"` // alert.fired | alert.resolved
	AlertID  string            `json:"alert_id"`
	RuleID   string            `json:"rule_id"`
	RuleName string            `json:"rule_name"`
	Severity string            `json:"severity"`
	State    string            `json:"state"`
	DeviceID string            `json:"device_id,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Value    float64           `json:"value"`
	FiredAt  time.Time         `json:"fired_at"`
	Summary  string            `json:"summary"`
	GraphURL string            `json:"graph_url,omitempty"`
}

type SyncResult struct {
	JobID       string        `json:"job_id"`
	DeviceID    string        `json:"device_id"`
	PollerID    string        `json:"poller_id"`
	Trigger     string        `json:"trigger"`
	CollectedAt time.Time     `json:"collected_at"`
	Error       string        `json:"error,omitempty"` // non-empty = failed run
	Snapshot    *SyncSnapshot `json:"snapshot,omitempty"`
}

// Sample is one metric observation in a batch.
type Sample struct {
	DeviceID string            `json:"d"`
	Name     string            `json:"n"`
	Labels   map[string]string `json:"l,omitempty"`
	TSMillis int64             `json:"t"`
	Value    float64           `json:"v"`
}

// MetricBatch is the poller→ingester payload (JSON v1; protobuf+zstd is the
// planned optimization at scale — doc 05 §5).
type MetricBatch struct {
	PollerID string   `json:"poller_id"`
	SiteID   string   `json:"site_id"`
	Samples  []Sample `json:"samples"`
}
