package app

import (
	"context"
	"log/slog"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// Discovery — subnet sweeps producing an approval queue (FR-SYNC-04).
// Nothing is ever auto-managed: an operator approves each find.

type DiscoveryRule struct {
	ID            string   `json:"id"`
	SiteID        string   `json:"site_id"`
	CIDR          string   `json:"cidr"`
	CredentialIDs []string `json:"credential_ids"`
	Enabled       bool     `json:"enabled"`
	CreatedAt     string   `json:"created_at"`
}

type Discovered struct {
	ID           string `json:"id"`
	RuleID       string `json:"rule_id"`
	SiteID       string `json:"site_id"`
	IP           string `json:"ip"`
	SysName      string `json:"sys_name"`
	SysDescr     string `json:"sys_descr"`
	SysObjectID  string `json:"sys_object_id"`
	ConnectorID  string `json:"matched_connector_id"`
	CredentialID string `json:"responding_credential_id"`
	State        string `json:"state"`
	Managed      bool   `json:"already_managed"`
	SeenLastAt   string `json:"seen_last_at"`
}

type DiscoveryRepo interface {
	CreateRule(ctx context.Context, r *DiscoveryRule) error
	ListRules(ctx context.Context) ([]DiscoveryRule, error)
	GetRule(ctx context.Context, ruleID string) (*DiscoveryRule, error)
	DeleteRule(ctx context.Context, ruleID string) error
	// UpsertFound records a sweep's hits, refreshing seen_last_at on repeats.
	UpsertFound(ctx context.Context, ruleID string, hosts []wire.DiscoveredHost,
		match func(sysObjectID, sysDescr string) string) (int, error)
	ListFound(ctx context.Context, state string) ([]Discovered, error)
	GetFound(ctx context.Context, foundID string) (*Discovered, error)
	SetFoundState(ctx context.Context, foundID, state string) error
}

// DiscoveryDispatcher publishes a sweep to the site's poller queue.
type DiscoveryDispatcher interface {
	Dispatch(ctx context.Context, job wire.DiscoveryJob) error
}

// CredentialLookup resolves candidate credentials for a sweep.
type CredentialLookup interface {
	Named(ctx context.Context, credentialIDs []string) ([]wire.NamedCred, error)
}

// ConnectorMatcher scores a discovered system against the connector catalog
// (doc 11 §7). The API can't import the connector registry (doc 13 rule 5),
// so matching uses the catalog's sysObjectID prefixes plus descr hints.
type ConnectorMatcher func(sysObjectID, sysDescr string) string

type DiscoveryService struct {
	Repo       DiscoveryRepo
	Dispatcher DiscoveryDispatcher
	Creds      CredentialLookup
	Match      ConnectorMatcher
	Audit      audit.Writer
	Log        *slog.Logger
}

func (s *DiscoveryService) CreateRule(ctx context.Context, siteID, cidr string,
	credentialIDs []string, m Meta) (*DiscoveryRule, error) {
	if siteID == "" || cidr == "" || len(credentialIDs) == 0 {
		return nil, errx.New(errx.KindInvalid,
			"site_id, cidr and at least one credential are required")
	}
	rule := &DiscoveryRule{
		ID: id.New("dr"), SiteID: siteID, CIDR: cidr,
		CredentialIDs: credentialIDs, Enabled: true,
	}
	if err := s.Repo.CreateRule(ctx, rule); err != nil {
		return nil, err
	}
	s.Audit.Write(ctx, m.event("discovery.rule.create", "discovery_rule", rule.ID,
		map[string]any{"cidr": cidr, "site_id": siteID}))
	return rule, nil
}

// Run dispatches a sweep for the rule to its site's pollers.
func (s *DiscoveryService) Run(ctx context.Context, ruleID string, m Meta) (string, error) {
	rule, err := s.Repo.GetRule(ctx, ruleID)
	if err != nil {
		return "", err
	}
	creds, err := s.Creds.Named(ctx, rule.CredentialIDs)
	if err != nil {
		return "", err
	}
	if len(creds) == 0 {
		return "", errx.New(errx.KindInvalid, "no usable credentials on this rule")
	}
	job := wire.DiscoveryJob{
		Family: "discovery", JobID: id.New("job"), RuleID: rule.ID,
		SiteID: rule.SiteID, CIDR: rule.CIDR, Port: 161,
		Creds: creds, TimeoutMS: 1500,
	}
	if err := s.Dispatcher.Dispatch(ctx, job); err != nil {
		return "", err
	}
	s.Audit.Write(ctx, m.event("discovery.run", "discovery_rule", rule.ID,
		map[string]any{"cidr": rule.CIDR}))
	return job.JobID, nil
}

// HandleResult stores a sweep's findings in the approval queue.
func (s *DiscoveryService) HandleResult(ctx context.Context, res wire.DiscoveryResult) error {
	if res.Error != "" {
		s.Log.Warn("discovery sweep reported an error",
			"rule", res.RuleID, "err", res.Error)
	}
	n, err := s.Repo.UpsertFound(ctx, res.RuleID, res.Found, s.Match)
	if err != nil {
		return err
	}
	s.Log.Info("discovery results stored", "rule", res.RuleID,
		"scanned", res.Scanned, "found", len(res.Found), "recorded", n)
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "system", Action: "discovery.completed",
		ResourceKind: "discovery_rule", ResourceID: res.RuleID,
		Detail: map[string]any{"scanned": res.Scanned, "found": len(res.Found)},
	})
	return nil
}

func (s *DiscoveryService) Ignore(ctx context.Context, foundID string, m Meta) error {
	if err := s.Repo.SetFoundState(ctx, foundID, "ignored"); err != nil {
		return err
	}
	s.Audit.Write(ctx, m.event("discovery.ignore", "discovered_device", foundID, nil))
	return nil
}
