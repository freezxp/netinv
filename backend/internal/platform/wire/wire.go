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
	Family      string    `json:"family"` // traffic | health | icmp | sync
	MgmtIP      string    `json:"mgmt_ip"`
	Port        int       `json:"port"`
	ConnectorID string    `json:"connector_id"`
	Cred        SNMPCred  `json:"cred"`
	ScheduledAt time.Time `json:"scheduled_at"`
	IntervalS   int       `json:"interval_s"`
	TimeoutMS   int       `json:"timeout_ms"`
	Retries     int       `json:"retries"`
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
