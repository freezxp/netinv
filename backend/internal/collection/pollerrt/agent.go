package pollerrt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/freezxp/netinv/backend/internal/collection/domain"
)

// Agent handles the poller's control-plane relationship with the core API:
// one-time enrollment (FR-PLT-02) and 30s heartbeats with runtime stats.
type Agent struct {
	APIURL   string // e.g. http://api:8080
	StateDir string
	Log      *slog.Logger
	HTTP     *http.Client

	Identity Identity
}

type Identity struct {
	PollerID  string `json:"poller_id"`
	SiteID    string `json:"site_id"`
	AuthToken string `json:"auth_token"`
}

func (a *Agent) statePath() string { return filepath.Join(a.StateDir, "identity.json") }

// Enroll loads the saved identity, or registers with the enrollment token.
func (a *Agent) Enroll(ctx context.Context, enrollToken, version string) error {
	if a.HTTP == nil {
		a.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if raw, err := os.ReadFile(a.statePath()); err == nil {
		if err := json.Unmarshal(raw, &a.Identity); err == nil && a.Identity.PollerID != "" {
			a.Log.Info("poller identity loaded", "poller_id", a.Identity.PollerID)
			return nil
		}
	}
	if enrollToken == "" {
		return fmt.Errorf("agent: no saved identity and no NETINV_ENROLL_TOKEN")
	}
	body, _ := json.Marshal(map[string]string{"token": enrollToken, "version": version})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.APIURL+"/api/v1/pollers/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("agent: register: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("agent: register status %d", resp.StatusCode)
	}
	var res struct {
		PollerID  string `json:"poller_id"`
		SiteID    string `json:"site_id"`
		AuthToken string `json:"auth_token"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return err
	}
	a.Identity = Identity{PollerID: res.PollerID, SiteID: res.SiteID, AuthToken: res.AuthToken}
	raw, _ := json.Marshal(a.Identity)
	if err := os.MkdirAll(a.StateDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(a.statePath(), raw, 0o600); err != nil {
		return err
	}
	a.Log.Info("poller registered", "poller_id", res.PollerID, "status", res.Status)
	return nil
}

// HeartbeatLoop reports liveness + stats every 30s until ctx ends.
func (a *Agent) HeartbeatLoop(ctx context.Context, version string, stats func() domain.HeartbeatStats) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	send := func() {
		body, _ := json.Marshal(map[string]any{"version": version, "stats": stats()})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			a.APIURL+"/api/v1/pollers/"+a.Identity.PollerID+"/heartbeat",
			bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Poller-Token", a.Identity.AuthToken)
		resp, err := a.HTTP.Do(req)
		if err != nil {
			a.Log.Warn("heartbeat failed", "err", err)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			a.Log.Warn("heartbeat rejected", "status", resp.StatusCode)
		}
	}
	send()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			send()
		}
	}
}

// Counters are the runtime stats the heartbeat reports.
type Counters struct {
	PollsOK     atomic.Int64
	PollsFailed atomic.Int64
	Batches     atomic.Int64
}
