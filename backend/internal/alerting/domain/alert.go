// Package domain — Alerting context (doc 16 §3).
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"
)

type Severity string

const (
	SevCritical Severity = "critical"
	SevWarning  Severity = "warning"
	SevInfo     Severity = "info"
)

type AlertState string

const (
	StateFiring   AlertState = "firing"
	StateAcked    AlertState = "acknowledged"
	StateResolved AlertState = "resolved"
	StateFlapping AlertState = "flapping"
)

type Rule struct {
	ID          string
	Name        string
	Kind        string // threshold | state | inventory
	Severity    Severity
	Expr        string
	Annotations map[string]string
	Enabled     bool
	Builtin     bool
}

type Instance struct {
	ID          string
	RuleID      string
	RuleName    string
	Fingerprint string
	State       AlertState
	Severity    Severity
	DeviceID    string
	InterfaceID string
	Labels      map[string]string
	Value       float64
	FiredAt     time.Time
	AckedAt     *time.Time
	AckedBy     string
	AckComment  string
	ResolvedAt  *time.Time
	FlapCount   int
}

// FlapWindow / FlapThreshold: re-fire within window after resolve counts a
// flap; crossing the threshold collapses into 'flapping' (FR-ALR-05).
const (
	FlapWindow    = 15 * time.Minute
	FlapThreshold = 3
)

// Fingerprint identifies one alert series under a rule: rule + sorted labels.
func Fingerprint(ruleID string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if k == "__name__" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte(ruleID))
	for _, k := range keys {
		h.Write([]byte{0})
		h.Write([]byte(k))
		h.Write([]byte{1})
		h.Write([]byte(labels[k]))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
