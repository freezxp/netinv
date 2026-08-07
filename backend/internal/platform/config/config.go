// Package config loads service configuration from the environment (NFR-54).
// Every option is documented in deploy/helm values; services fail fast on
// invalid config but stay dependency-patient at startup (doc 23 §7).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
