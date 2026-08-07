package domain

import "time"

type DeviceStatus string

const (
	DevicePending     DeviceStatus = "pending"
	DeviceActive      DeviceStatus = "active"
	DeviceUnreachable DeviceStatus = "unreachable"
	DeviceDisabled    DeviceStatus = "disabled"
	DeviceRetired     DeviceStatus = "retired"
)

type Device struct {
	ID           string
	TenantID     string
	SiteID       string
	ConnectorID  string
	CredentialID string
	ProfileID    string
	Name         string
	MgmtIP       string
	Status       DeviceStatus
	SysName      string
	SysDescr     string
	Vendor       string
	Model        string
	SerialNumber string
	OSVersion    string
	Tags         []string
	Notes        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Pollable reports whether the scheduler should dispatch jobs for the device.
func (d *Device) Pollable() bool {
	return d.Status == DeviceActive || d.Status == DevicePending || d.Status == DeviceUnreachable
}
