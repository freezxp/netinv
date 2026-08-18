package domain

import "time"

type PollerStatus string

const (
	PollerPending  PollerStatus = "pending"
	PollerActive   PollerStatus = "active"
	PollerDisabled PollerStatus = "disabled"
)

// Poller is a registered site-local collection agent (FR-PLT-02).
type Poller struct {
	ID              string
	TenantID        string
	SiteID          string
	Name            string
	Status          PollerStatus
	Version         string
	LastHeartbeatAt *time.Time
	Stats           map[string]any
	CreatedAt       time.Time
}

// HeartbeatStats is what a poller reports every cycle.
type HeartbeatStats struct {
	PollsOK     int64 `json:"polls_ok"`
	PollsFailed int64 `json:"polls_failed"`
	Batches     int64 `json:"batches"`
	BufferDepth int   `json:"buffer_depth"`
	BufferBytes int64 `json:"buffer_bytes"`
	Workers     int   `json:"workers"`
}

// SiteQueueState is what the broker reports about a site's job queue when the
// scheduler declares it before publishing. Consumers is the number of pollers
// reading the queue: zero means every job dispatched to this site is being
// queued and never executed, which is silent on every other signal — the jobs
// are routable, the publish succeeds, and nothing fails.
type SiteQueueState struct {
	Consumers int
	Queued    int
}
