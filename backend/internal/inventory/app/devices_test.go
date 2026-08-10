package app

import (
	"context"
	"strings"
	"testing"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// stubRepo accepts everything, so any rejection in these tests comes from
// validation rather than from a repository refusing the write.
type stubRepo struct{ created, updated *domain.Device }

func (r *stubRepo) List(context.Context, DeviceFilter) ([]*domain.Device, string, error) {
	return nil, "", nil
}
func (r *stubRepo) Get(_ context.Context, id string) (*domain.Device, error) {
	if r.created != nil && r.created.ID == id {
		return r.created, nil
	}
	return &domain.Device{ID: id}, nil
}
func (r *stubRepo) Create(_ context.Context, d *domain.Device) error { r.created = d; return nil }
func (r *stubRepo) Update(_ context.Context, d *domain.Device) error {
	r.updated = d
	return nil
}
func (r *stubRepo) SetStatus(context.Context, string, domain.DeviceStatus) error { return nil }
func (r *stubRepo) Purge(context.Context, string) error                          { return nil }
func (r *stubRepo) RefsExist(context.Context, string, string, string, string) error {
	return nil
}

type nopAudit struct{}

func (nopAudit) Write(context.Context, audit.Event) {}

func newService() (*DeviceService, *stubRepo) {
	repo := &stubRepo{}
	return &DeviceService{Repo: repo, Audit: nopAudit{}}, repo
}

func validInput() DeviceInput {
	return DeviceInput{
		Name: "edge-01", MgmtIP: "198.51.100.7", SiteID: "s_default",
		CredentialID: "cr_1", ConnectorID: "generic", ProfileID: "pp_default",
	}
}

// The poller narrows the port to a uint16 for gosnmp. Anything above 65535
// wraps rather than failing, so 99999 would silently poll 33999 and the device
// would appear unreachable with nothing anywhere explaining why.
func TestCreateRejectsOutOfRangeSNMPPort(t *testing.T) {
	for _, port := range []int{65536, 99999, -1} {
		svc, repo := newService()
		in := validInput()
		in.SNMPPort = port

		_, err := svc.Create(context.Background(), in, Meta{})
		if err == nil {
			t.Fatalf("port %d was accepted; it wraps to %d in the poller",
				port, uint16(port)) //nolint:gosec // demonstrating the wrap is the point
		}
		if errx.KindOf(err) != errx.KindInvalid {
			t.Errorf("port %d: error kind = %v, want KindInvalid so the API answers 400",
				port, errx.KindOf(err))
		}
		if !strings.Contains(err.Error(), "snmp_port") {
			t.Errorf("port %d: message %q does not name the offending field", port, err)
		}
		if repo.created != nil {
			t.Errorf("port %d: device was written despite the rejection", port)
		}
	}
}

func TestCreateAcceptsPortsInRange(t *testing.T) {
	// 0 means "unset" and must keep defaulting to 161 rather than being
	// rejected by the new bounds check.
	for _, port := range []int{0, 1, 161, 1161, 65535} {
		svc, repo := newService()
		in := validInput()
		in.SNMPPort = port

		if _, err := svc.Create(context.Background(), in, Meta{}); err != nil {
			t.Fatalf("port %d was rejected: %v", port, err)
		}
		if repo.created == nil {
			t.Fatalf("port %d: nothing was written", port)
		}
		got, set := repo.created.Attrs["snmp_port"]
		switch port {
		case 0, 161:
			// The default is implied by absence; storing it would make a later
			// change of default invisible to existing devices.
			if set {
				t.Errorf("port %d: stored %v, want the attribute left unset", port, got)
			}
		default:
			if got != port {
				t.Errorf("port %d: stored %v", port, got)
			}
		}
	}
}

// The API accepted a re-address, returned 200, and changed nothing: neither
// the service nor the repository's UPDATE touched mgmt_ip. A device that moved
// could only be corrected by deleting it, which loses its history.
func TestUpdateAppliesManagementAddress(t *testing.T) {
	svc, repo := newService()
	repo.created = &domain.Device{
		ID: "d_1", MgmtIP: "198.51.100.7", Name: "edge-01",
		SiteID: "s_default", CredentialID: "cr_1",
		Attrs: map[string]any{},
	}

	got, err := svc.Update(context.Background(), "d_1",
		DeviceInput{MgmtIP: "198.51.100.9"}, Meta{})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.MgmtIP != "198.51.100.9" {
		t.Errorf("mgmt_ip = %q, want the new address", got.MgmtIP)
	}
	if repo.updated == nil || repo.updated.MgmtIP != "198.51.100.9" {
		t.Error("the new address never reached the repository")
	}
}

func TestUpdateRejectsAnUnparseableAddress(t *testing.T) {
	svc, repo := newService()
	repo.created = &domain.Device{ID: "d_1", MgmtIP: "198.51.100.7",
		Attrs: map[string]any{}}

	_, err := svc.Update(context.Background(), "d_1",
		DeviceInput{MgmtIP: "not-an-ip"}, Meta{})
	if err == nil {
		t.Fatal("an unparseable address was accepted")
	}
	if errx.KindOf(err) != errx.KindInvalid {
		t.Errorf("kind = %v, want KindInvalid", errx.KindOf(err))
	}
}

// The port lives in attrs and is read at poll time, so it must round-trip the
// same way — and 161 has to *clear* the override rather than pin the device to
// today's default.
func TestUpdateManagesTheSNMPPortOverride(t *testing.T) {
	svc, repo := newService()
	repo.created = &domain.Device{ID: "d_1", MgmtIP: "198.51.100.7",
		Attrs: map[string]any{}}

	if _, err := svc.Update(context.Background(), "d_1",
		DeviceInput{SNMPPort: 1161}, Meta{}); err != nil {
		t.Fatalf("set port: %v", err)
	}
	if got := repo.updated.Attrs["snmp_port"]; got != 1161 {
		t.Errorf("snmp_port = %v, want 1161", got)
	}

	// Absent from a partial update means "leave it".
	if _, err := svc.Update(context.Background(), "d_1",
		DeviceInput{Name: "renamed"}, Meta{}); err != nil {
		t.Fatalf("partial update: %v", err)
	}
	if got := repo.updated.Attrs["snmp_port"]; got != 1161 {
		t.Errorf("snmp_port = %v after an unrelated update, want it untouched", got)
	}

	if _, err := svc.Update(context.Background(), "d_1",
		DeviceInput{SNMPPort: 161}, Meta{}); err != nil {
		t.Fatalf("reset port: %v", err)
	}
	if _, still := repo.updated.Attrs["snmp_port"]; still {
		t.Error("161 should clear the override, not store it")
	}
}

func TestUpdateRejectsAnOutOfRangePort(t *testing.T) {
	svc, repo := newService()
	repo.created = &domain.Device{ID: "d_1", MgmtIP: "198.51.100.7",
		Attrs: map[string]any{}}
	for _, bad := range []int{-1, 65536, 99999} {
		if _, err := svc.Update(context.Background(), "d_1",
			DeviceInput{SNMPPort: bad}, Meta{}); err == nil {
			t.Errorf("port %d was accepted on update", bad)
		}
	}
}
