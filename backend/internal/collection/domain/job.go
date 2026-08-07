// Package domain — Collection context (doc 16): jobs, schedules, pollers.
package domain

type PollFamily string

const (
	FamilyTraffic PollFamily = "traffic"
	FamilyHealth  PollFamily = "health"
	FamilyICMP    PollFamily = "icmp"
	FamilySync    PollFamily = "sync"
)

// The PollJob wire format lives in platform/wire (cross-service contract).

// DueSchedule is the scheduler's read model over polling_schedule ⋈ devices
// ⋈ polling_profiles.
type DueSchedule struct {
	ScheduleID   int64
	DeviceID     string
	SiteID       string
	Family       PollFamily
	MgmtIP       string
	Port         int
	ConnectorID  string
	CredentialID string
	IntervalS    int
	TimeoutMS    int
	Retries      int
}
