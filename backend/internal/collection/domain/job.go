// Package domain — Collection context (doc 16): jobs, schedules, pollers.
package domain

import "time"

type PollFamily string

const (
	FamilyTraffic PollFamily = "traffic"
	FamilyHealth  PollFamily = "health"
	FamilyICMP    PollFamily = "icmp"
	FamilySync    PollFamily = "sync"
)

// PollJob is one unit of collection work: one device × one family (doc 05 §5).
type PollJob struct {
	JobID       string     `json:"job_id"`
	DeviceID    string     `json:"device_id"`
	SiteID      string     `json:"site_id"`
	Family      PollFamily `json:"family"`
	MgmtIP      string     `json:"mgmt_ip"`
	ConnectorID string     `json:"connector_id"`
	// CredentialRef only — pollers fetch/decrypt at use time (doc 20 §6).
	CredentialID string    `json:"credential_id"`
	ScheduledAt  time.Time `json:"scheduled_at"`
	IntervalS    int       `json:"interval_s"`
}

// DueSchedule is the scheduler's read model over polling_schedule ⋈ devices.
type DueSchedule struct {
	ScheduleID   int64
	DeviceID     string
	SiteID       string
	Family       PollFamily
	MgmtIP       string
	ConnectorID  string
	CredentialID string
	IntervalS    int
}
