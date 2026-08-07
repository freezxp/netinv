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
