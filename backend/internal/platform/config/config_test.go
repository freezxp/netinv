package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	c, err := Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", c.HTTPAddr)
	}
	if c.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", c.LogLevel)
	}
}

func TestLoadPerServicePorts(t *testing.T) {
	for svc, port := range defaultHealthPorts {
		c, err := Load(svc)
		if err != nil {
			t.Fatalf("Load(%s): %v", svc, err)
		}
		if c.HTTPAddr != port {
			t.Errorf("Load(%s).HTTPAddr = %q, want %q", svc, c.HTTPAddr, port)
		}
	}
}

func TestLoadUnknownService(t *testing.T) {
	if _, err := Load("nope"); err == nil {
		t.Fatal("want error for unknown service")
	}
}

func TestLoadInvalidLevel(t *testing.T) {
	t.Setenv("NETINV_LOG_LEVEL", "loud")
	if _, err := Load("api"); err == nil {
		t.Fatal("want error for invalid log level")
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("NETINV_HTTP_ADDR", ":9999")
	c, err := Load("poller")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.HTTPAddr != ":9999" {
		t.Errorf("HTTPAddr = %q, want :9999", c.HTTPAddr)
	}
}
