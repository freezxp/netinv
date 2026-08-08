package app

import (
	"context"
	"encoding/csv"
	"io"
	"net/netip"
	"strings"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

type DeviceFilter struct {
	SiteID string
	Status []string
	Query  string
	Cursor string
	Limit  int
}

type DeviceRepo interface {
	List(ctx context.Context, f DeviceFilter) (items []*domain.Device, nextCursor string, err error)
	Get(ctx context.Context, id string) (*domain.Device, error)
	// Create inserts the device and its polling_schedule rows in one tx.
	Create(ctx context.Context, d *domain.Device) error
	Update(ctx context.Context, d *domain.Device) error
	SetStatus(ctx context.Context, id string, status domain.DeviceStatus) error
	// Purge hard-deletes a retired device and its owned rows.
	Purge(ctx context.Context, id string) error
	// RefsExist validates site/connector/credential/profile in one round trip.
	RefsExist(ctx context.Context, siteID, connectorID, credentialID, profileID string) error
}

type DeviceService struct {
	Repo  DeviceRepo
	Audit audit.Writer
	// Optional, for the OID browser; nil disables the feature.
	Vault  CredentialVault
	Walker OIDWalker
}

type DeviceInput struct {
	Name         string   `json:"name"`
	MgmtIP       string   `json:"mgmt_ip"`
	SiteID       string   `json:"site_id"`
	ConnectorID  string   `json:"connector_id"`
	CredentialID string   `json:"credential_id"`
	ProfileID    string   `json:"profile_id"`
	Tags         []string `json:"tags"`
	Notes        string   `json:"notes"`
	SNMPPort     int      `json:"snmp_port"` // default 161
}

func (s *DeviceService) validate(ctx context.Context, in DeviceInput) error {
	if in.Name == "" || in.MgmtIP == "" || in.SiteID == "" ||
		in.CredentialID == "" {
		return errx.New(errx.KindInvalid, "name, mgmt_ip, site_id and credential_id are required")
	}
	if _, err := netip.ParseAddr(in.MgmtIP); err != nil {
		return errx.New(errx.KindInvalid, "mgmt_ip is not a valid IP address")
	}
	return s.Repo.RefsExist(ctx, in.SiteID, in.ConnectorID, in.CredentialID, in.ProfileID)
}

func (s *DeviceService) Create(ctx context.Context, in DeviceInput, m Meta) (*domain.Device, error) {
	if in.ConnectorID == "" {
		in.ConnectorID = "generic" // auto-match refines this during sync (doc 11 §7)
	}
	if in.ProfileID == "" {
		in.ProfileID = "pp_default"
	}
	if err := s.validate(ctx, in); err != nil {
		return nil, err
	}
	d := &domain.Device{
		ID: id.New("d"), TenantID: "t_default",
		SiteID: in.SiteID, ConnectorID: in.ConnectorID,
		CredentialID: in.CredentialID, ProfileID: in.ProfileID,
		Name: in.Name, MgmtIP: in.MgmtIP, Status: domain.DevicePending,
		Tags: in.Tags, Notes: in.Notes,
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	d.Attrs = map[string]any{}
	if in.SNMPPort > 0 && in.SNMPPort != 161 {
		d.Attrs["snmp_port"] = in.SNMPPort
	}
	if err := s.Repo.Create(ctx, d); err != nil {
		return nil, err
	}
	s.Audit.Write(ctx, m.event("device.create", "device", d.ID, nil,
		map[string]any{"name": d.Name, "mgmt_ip": d.MgmtIP, "site_id": d.SiteID}))
	return s.Repo.Get(ctx, d.ID) // re-read for DB-assigned timestamps
}

func (s *DeviceService) Update(ctx context.Context, deviceID string, in DeviceInput, m Meta) (*domain.Device, error) {
	d, err := s.Repo.Get(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	before := map[string]any{"name": d.Name, "site_id": d.SiteID,
		"credential_id": d.CredentialID, "profile_id": d.ProfileID, "notes": d.Notes}
	// Operator-owned fields only (doc 11 §3): identity fields from sync are
	// not writable here.
	if in.Name != "" {
		d.Name = in.Name
	}
	if in.SiteID != "" {
		d.SiteID = in.SiteID
	}
	if in.CredentialID != "" {
		d.CredentialID = in.CredentialID
	}
	if in.ProfileID != "" {
		d.ProfileID = in.ProfileID
	}
	if in.ConnectorID != "" {
		d.ConnectorID = in.ConnectorID
	}
	if in.Tags != nil {
		d.Tags = in.Tags
	}
	d.Notes = in.Notes
	if err := s.Repo.RefsExist(ctx, d.SiteID, d.ConnectorID, d.CredentialID, d.ProfileID); err != nil {
		return nil, err
	}
	if err := s.Repo.Update(ctx, d); err != nil {
		return nil, err
	}
	s.Audit.Write(ctx, m.event("device.update", "device", d.ID, before,
		map[string]any{"name": d.Name, "site_id": d.SiteID,
			"credential_id": d.CredentialID, "profile_id": d.ProfileID, "notes": d.Notes}))
	return d, nil
}

func (s *DeviceService) SetStatus(ctx context.Context, deviceID string,
	status domain.DeviceStatus, m Meta) error {
	if err := s.Repo.SetStatus(ctx, deviceID, status); err != nil {
		return err
	}
	s.Audit.Write(ctx, m.event("device."+string(status), "device", deviceID, nil, nil))
	return nil
}

// Purge permanently removes a retired device and everything owned by it
// (FR-DEV-08). Deliberately two-step: a device must be retired first, so a
// single mis-click can never destroy history. Time-series samples are not
// deleted — they age out under the retention policy (doc 11 §4).
func (s *DeviceService) Purge(ctx context.Context, deviceID string, m Meta) error {
	d, err := s.Repo.Get(ctx, deviceID)
	if err != nil {
		return err
	}
	if d.Status != domain.DeviceRetired {
		return errx.New(errx.KindConflict,
			"device must be retired before it can be permanently deleted")
	}
	if err := s.Repo.Purge(ctx, deviceID); err != nil {
		return err
	}
	s.Audit.Write(ctx, m.event("device.purge", "device", deviceID,
		map[string]any{"name": d.Name, "mgmt_ip": d.MgmtIP}, nil))
	return nil
}

// ImportRowResult reports per-row CSV import outcomes (FR-DEV-02).
type ImportRowResult struct {
	Row      int    `json:"row"`
	Name     string `json:"name"`
	DeviceID string `json:"device_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ImportCSV expects header: name,mgmt_ip,site_id,credential_id[,connector_id][,profile_id][,tags]
func (s *DeviceService) ImportCSV(ctx context.Context, r io.Reader, m Meta) ([]ImportRowResult, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, errx.New(errx.KindInvalid, "empty or unreadable CSV")
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, required := range []string{"name", "mgmt_ip", "site_id", "credential_id"} {
		if _, ok := col[required]; !ok {
			return nil, errx.New(errx.KindInvalid, "missing required CSV column %q", required)
		}
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	var results []ImportRowResult
	for row := 2; ; row++ {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			results = append(results, ImportRowResult{Row: row, Error: "unparseable row"})
			continue
		}
		in := DeviceInput{
			Name: get(rec, "name"), MgmtIP: get(rec, "mgmt_ip"),
			SiteID: get(rec, "site_id"), CredentialID: get(rec, "credential_id"),
			ConnectorID: get(rec, "connector_id"), ProfileID: get(rec, "profile_id"),
		}
		if tags := get(rec, "tags"); tags != "" {
			in.Tags = strings.Split(tags, ";")
		}
		d, err := s.Create(ctx, in, m)
		res := ImportRowResult{Row: row, Name: in.Name}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.DeviceID = d.ID
		}
		results = append(results, res)
	}
	ok := 0
	for _, r := range results {
		if r.Error == "" {
			ok++
		}
	}
	s.Audit.Write(ctx, m.event("device.import", "device", "", nil,
		map[string]any{"rows": len(results), "created": ok}))
	if len(results) == 0 {
		return nil, errx.New(errx.KindInvalid, "CSV contains no data rows")
	}
	return results, nil
}

// ---- OID browser (doc 30 §5) ----

// OIDValue is one object returned by a live SNMP walk.
type OIDValue struct {
	OID   string `json:"oid"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// OIDWalker performs a live walk against a device.
type OIDWalker interface {
	Walk(ctx context.Context, target string, port int, kind domain.CredentialKind,
		secret domain.Secret, root string, limit int) ([]OIDValue, error)
}

// WalkOIDs dumps what a device actually exposes — the tool for working out
// which MIBs a new platform supports before writing a connector for it.
func (s *DeviceService) WalkOIDs(ctx context.Context, deviceID, root string,
	limit int, m Meta) ([]OIDValue, error) {
	if s.Walker == nil || s.Vault == nil {
		return nil, errx.New(errx.KindTransient, "OID browsing is not configured")
	}
	if root == "" {
		root = ".1.3.6.1.2.1" // mib-2: the standard starting point
	}
	d, err := s.Repo.Get(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	cred, err := s.Vault.Get(ctx, d.CredentialID)
	if err != nil {
		return nil, err
	}
	secret, err := s.Vault.Decrypt(ctx, d.CredentialID)
	if err != nil {
		return nil, err
	}
	port := 161
	if v, ok := d.Attrs["snmp_port"].(float64); ok && v > 0 {
		port = int(v)
	}
	values, err := s.Walker.Walk(ctx, d.MgmtIP, port, cred.Kind, secret, root, limit)
	if err != nil && len(values) == 0 {
		return nil, err
	}
	// Walking is a read, but it puts load on the device — worth an audit trail.
	s.Audit.Write(ctx, m.event("device.oid_walk", "device", deviceID, nil,
		map[string]any{"root": root, "returned": len(values)}))
	return values, nil
}
