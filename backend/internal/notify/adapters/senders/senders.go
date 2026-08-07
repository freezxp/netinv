// Package senders — Email/Webhook/Slack delivery (FR-NOT-01).
package senders

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"github.com/freezxp/netinv/backend/internal/notify/app"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

func subject(ev wire.AlertEvent) string {
	verb := "FIRING"
	if ev.Event == "alert.resolved" {
		verb = "RESOLVED"
	}
	return fmt.Sprintf("[%s] [%s] %s", verb, strings.ToUpper(ev.Severity), ev.RuleName)
}

func textBody(ev wire.AlertEvent) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "%s\n\n%s\n", subject(ev), ev.Summary)
	if dev := ev.Labels["device"]; dev != "" {
		fmt.Fprintf(b, "Device: %s\n", dev)
	}
	if ifn := ev.Labels["if_index"]; ifn != "" {
		fmt.Fprintf(b, "Interface index: %s\n", ifn)
	}
	fmt.Fprintf(b, "Value: %g\nFired: %s\n", ev.Value, ev.FiredAt.Format(time.RFC3339))
	if ev.GraphURL != "" {
		fmt.Fprintf(b, "Graph: %s\n", ev.GraphURL)
	}
	return b.String()
}

// ---- email (SMTP, config: host, port, from, to[]; secret: password) ----

type Email struct{}

func (Email) Send(_ context.Context, ch app.Channel, ev wire.AlertEvent) error {
	host, _ := ch.Config["host"].(string)
	from, _ := ch.Config["from"].(string)
	port, _ := ch.Config["port"].(float64)
	var to []string
	if raw, ok := ch.Config["to"].([]any); ok {
		for _, t := range raw {
			if s, ok := t.(string); ok {
				to = append(to, s)
			}
		}
	}
	if host == "" || from == "" || len(to) == 0 {
		return errx.New(errx.KindInvalid, "email channel misconfigured")
	}
	addr := fmt.Sprintf("%s:%d", host, int(port))
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		from, strings.Join(to, ", "), subject(ev), textBody(ev))
	var auth smtp.Auth
	if pw := ch.Secret["password"]; pw != "" {
		user, _ := ch.Config["username"].(string)
		auth = smtp.PlainAuth("", user, pw, host)
	}
	if err := smtp.SendMail(addr, auth, from, to, []byte(msg)); err != nil {
		return errx.Wrap(errx.KindTransient, err, "smtp send")
	}
	return nil
}

// ---- webhook (config: url, headers; secret: hmac_key) ----

type Webhook struct{ HTTP *http.Client }

func (w Webhook) client() *http.Client {
	if w.HTTP != nil {
		return w.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (w Webhook) Send(ctx context.Context, ch app.Channel, ev wire.AlertEvent) error {
	url, _ := ch.Config["url"].(string)
	if url == "" {
		url = ch.Secret["url"] // URLs with embedded tokens live in the secret
	}
	if url == "" {
		return errx.New(errx.KindInvalid, "webhook channel misconfigured")
	}
	payload, _ := json.Marshal(ev)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if hdrs, ok := ch.Config["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if s, ok := v.(string); ok {
				req.Header.Set(k, s)
			}
		}
	}
	if key := ch.Secret["hmac_key"]; key != "" {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write(payload)
		req.Header.Set("X-NetInv-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return doPost(w.client(), req)
}

// ---- slack (secret: url — incoming webhook) ----

type Slack struct{ HTTP *http.Client }

func (s Slack) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (s Slack) Send(ctx context.Context, ch app.Channel, ev wire.AlertEvent) error {
	url := ch.Secret["url"]
	if url == "" {
		return errx.New(errx.KindInvalid, "slack channel misconfigured")
	}
	emoji := ":red_circle:"
	switch {
	case ev.Event == "alert.resolved":
		emoji = ":large_green_circle:"
	case ev.Severity == "warning":
		emoji = ":large_yellow_circle:"
	}
	text := fmt.Sprintf("%s *%s*\n%s", emoji, subject(ev), ev.Summary)
	if ev.GraphURL != "" {
		text += fmt.Sprintf("\n<%s|View graph>", ev.GraphURL)
	}
	payload, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doPost(s.client(), req)
}

// doPost classifies HTTP outcomes per doc 23 §2: 4xx permanent, 5xx transient.
func doPost(client *http.Client, req *http.Request) error {
	resp, err := client.Do(req)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "post")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode < 300:
		return nil
	case resp.StatusCode < 500:
		return errx.New(errx.KindInvalid, "endpoint rejected notification (status %d)", resp.StatusCode)
	default:
		return errx.New(errx.KindTransient, "endpoint error (status %d)", resp.StatusCode)
	}
}
