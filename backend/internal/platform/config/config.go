// Package config loads service configuration from the environment (NFR-54).
// Every option is documented in deploy/helm values; services fail fast on
// invalid config but stay dependency-patient at startup (doc 23 §7).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Common holds configuration shared by every netinv service.
type Common struct {
	Service   string // netinv service name: api, scheduler, poller, ingester, alerter, notifier
	LogLevel  string // debug|info|warn|error
	LogPretty bool   // human-readable console output for local dev
	HTTPAddr  string // listen address for /healthz, /readyz, /metrics
}

// defaultHealthPorts keeps locally-run services from colliding (doc 13).
var defaultHealthPorts = map[string]string{
	"api":       ":8080",
	"scheduler": ":8081",
	"poller":    ":8082",
	"ingester":  ":8083",
	"alerter":   ":8084",
	"notifier":  ":8085",
	"flow":      ":8086",
}

// Load reads common configuration for the named service.
func Load(service string) (Common, error) {
	if _, ok := defaultHealthPorts[service]; !ok {
		return Common{}, fmt.Errorf("config: unknown service %q", service)
	}
	c := Common{
		Service:   service,
		LogLevel:  getenv("NETINV_LOG_LEVEL", "info"),
		LogPretty: getbool("NETINV_LOG_PRETTY", false),
		HTTPAddr:  getenv("NETINV_HTTP_ADDR", defaultHealthPorts[service]),
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return Common{}, fmt.Errorf("config: invalid NETINV_LOG_LEVEL %q", c.LogLevel)
	}
	return c, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// DefaultRetention is what a deployment keeps when NETINV_VM_RETENTION is
// unset. Two years, so the full range selector (doc 30 §0) resolves out of the
// box: the presets are Cacti's, they run to two years, and a shorter default
// would ship a menu whose longer half is greyed out on every fresh install.
//
// The cost is real but modest at the scale this targets — roughly 75 MB per
// device per year of raw samples, so ~75 GB for 500 devices over two years.
// Operators with larger fleets or smaller disks should lower it; doc 04 §4
// carries the arithmetic.
const DefaultRetention = 730 * 24 * time.Hour

// Retention returns how far back metrics are queryable, from
// NETINV_VM_RETENTION. It is the same value VictoriaMetrics is started with,
// so the API's query ceiling and the store's actual retention cannot drift
// apart — a ceiling below retention silently hides data the operator paid to
// keep, and one above it produces long empty graphs.
//
// Accepts VictoriaMetrics' duration syntax rather than Go's: 90d, 2y, 18mo,
// 52w. Go's time.ParseDuration stops at hours, which is why a retention of
// "2y" cannot simply be parsed with it.
func Retention() time.Duration {
	d, err := ParseRetention(getenv("NETINV_VM_RETENTION", "2y"))
	if err != nil {
		return DefaultRetention
	}
	return d
}

// ParseRetention understands VictoriaMetrics-style durations. Months are 31
// days and years 365, matching VictoriaMetrics itself; the value decides how
// much history to keep, not an exact calendar boundary.
func ParseRetention(v string) (time.Duration, error) {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return 0, fmt.Errorf("config: empty retention")
	}
	units := []struct {
		suffix string
		unit   time.Duration
	}{
		{"mo", 31 * 24 * time.Hour}, // before "m", which would match its first byte
		{"y", 365 * 24 * time.Hour},
		{"w", 7 * 24 * time.Hour},
		{"d", 24 * time.Hour},
		{"h", time.Hour},
		{"m", time.Minute},
		{"s", time.Second},
	}
	for _, u := range units {
		if !strings.HasSuffix(v, u.suffix) {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSuffix(v, u.suffix), 64)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("config: invalid retention %q", v)
		}
		return time.Duration(n * float64(u.unit)), nil
	}
	return 0, fmt.Errorf("config: invalid retention %q (want e.g. 90d, 2y)", v)
}

func getbool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
