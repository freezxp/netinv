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
	Attrs        map[string]any // connector/transport extras, e.g. snmp_port
	// WANCapacityBPS is the subscribed uplink rate, stated by an operator
	// because SNMP cannot report it (a PPPoE session has no ifSpeed). 0 when
	// unknown. Weathermap links over tunnels divide by it — see FR-MAP-08.
	WANCapacityBPS int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Pollable reports whether the scheduler should dispatch jobs for the device.
func (d *Device) Pollable() bool {
	return d.Status == DeviceActive || d.Status == DevicePending || d.Status == DeviceUnreachable
}
