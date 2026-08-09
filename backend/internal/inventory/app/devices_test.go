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
type stubRepo struct{ created *domain.Device }

func (r *stubRepo) List(context.Context, DeviceFilter) ([]*domain.Device, string, error) {
	return nil, "", nil
}
func (r *stubRepo) Get(_ context.Context, id string) (*domain.Device, error) {
	if r.created != nil && r.created.ID == id {
		return r.created, nil
	}
	return &domain.Device{ID: id}, nil
}
func (r *stubRepo) Create(_ context.Context, d *domain.Device) error             { r.created = d; return nil }
func (r *stubRepo) Update(context.Context, *domain.Device) error                 { return nil }
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
