// Package app — Inventory use cases. Sprint 4: sites + credentials.
package app

import (
	"context"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

// Meta mirrors iam's ClientMeta without importing across contexts (doc 13 rule 3).
type Meta struct {
	Actor, SourceIP, UserAgent, TraceID string
}

func (m Meta) event(action, kind, rid string, before, after any) audit.Event {
	return audit.Event{
		ActorKind: "user", ActorID: m.Actor, Action: action,
		ResourceKind: kind, ResourceID: rid, Before: before, After: after,
		SourceIP: m.SourceIP, UserAgent: m.UserAgent, TraceID: m.TraceID,
	}
}

type SiteRepo interface {
	List(ctx context.Context) ([]*domain.Site, error)
	Get(ctx context.Context, id string) (*domain.Site, error)
	Create(ctx context.Context, s *domain.Site) error
	Update(ctx context.Context, s *domain.Site) error
	Delete(ctx context.Context, id string) error // errx.Conflict when devices exist
}

type SiteService struct {
	Repo  SiteRepo
	Audit audit.Writer
}

type SiteInput struct {
	Name         string  `json:"name"`
	ParentSiteID *string `json:"parent_site_id"`
	Location     string  `json:"location"`
	Contact      string  `json:"contact"`
}

func (s *SiteService) Create(ctx context.Context, in SiteInput, m Meta) (*domain.Site, error) {
	if in.Name == "" {
		return nil, errx.New(errx.KindInvalid, "name is required")
	}
	if in.ParentSiteID != nil {
		if _, err := s.Repo.Get(ctx, *in.ParentSiteID); err != nil {
			return nil, errx.New(errx.KindInvalid, "parent site not found")
		}
	}
	site := &domain.Site{
		ID: id.New("s"), TenantID: "t_default", Name: in.Name,
		ParentSiteID: in.ParentSiteID, Location: in.Location, Contact: in.Contact,
		Status: "active",
	}
	if err := s.Repo.Create(ctx, site); err != nil {
		return nil, err
	}
	s.Audit.Write(ctx, m.event("site.create", "site", site.ID, nil,
		map[string]any{"name": site.Name, "location": site.Location}))
	return site, nil
}

func (s *SiteService) Update(ctx context.Context, siteID string, in SiteInput, m Meta) (*domain.Site, error) {
	site, err := s.Repo.Get(ctx, siteID)
	if err != nil {
		return nil, err
	}
	before := map[string]any{"name": site.Name, "location": site.Location, "contact": site.Contact}
	if in.Name != "" {
		site.Name = in.Name
	}
	site.Location, site.Contact = in.Location, in.Contact
	if in.ParentSiteID != nil {
		if *in.ParentSiteID == siteID {
			return nil, errx.New(errx.KindInvalid, "site cannot be its own parent")
		}
		site.ParentSiteID = in.ParentSiteID
	}
	if err := s.Repo.Update(ctx, site); err != nil {
		return nil, err
	}
	s.Audit.Write(ctx, m.event("site.update", "site", site.ID, before,
		map[string]any{"name": site.Name, "location": site.Location, "contact": site.Contact}))
	return site, nil
}

func (s *SiteService) Delete(ctx context.Context, siteID string, m Meta) error {
	// Read it before it is gone: an audit row saying only that site s_01H… was
	// deleted is close to useless six months later, when the name is the only
	// handle anyone still has on what it was. Deletion is the one action where
	// the record cannot be reconstructed from the surviving state.
	before, err := s.Repo.Get(ctx, siteID)
	if err != nil {
		return err
	}
	if err := s.Repo.Delete(ctx, siteID); err != nil {
		return err
	}
	s.Audit.Write(ctx, m.event("site.delete", "site", siteID,
		map[string]any{"name": before.Name, "location": before.Location,
			"contact": before.Contact, "parent_site_id": before.ParentSiteID}, nil))
	return nil
}
