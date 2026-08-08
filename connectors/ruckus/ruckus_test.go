package ruckus

import (
	"context"
	"strings"
	"testing"

	"github.com/freezxp/netinv/connectors/sdk"
	_ "github.com/freezxp/netinv/connectors/generic"
)

type fakeSession struct{ data map[string]any }

func (f *fakeSession) Get(_ context.Context, oids []string) ([]sdk.Var, error) {
	var out []sdk.Var
	for _, oid := range oids {
		if v, ok := f.data[oid]; ok {
			out = append(out, sdk.Var{OID: oid, Value: v})
		}
	}
	return out, nil
}
func (f *fakeSession) Walk(_ context.Context, root string) ([]sdk.Var, error) {
	var out []sdk.Var
	for oid, v := range f.data {
		if strings.HasPrefix(oid, root+".") {
			out = append(out, sdk.Var{OID: oid, Value: v})
		}
	}
	return out, nil
}
func (f *fakeSession) Target() sdk.TargetMeta { return sdk.TargetMeta{} }

// Values recorded from a real R710 Unleashed master (pilot validation).
func r710() *fakeSession {
	return &fakeSession{data: map[string]any{
		oidSystemName:    "22BI8-Unleashed",
		oidSystemModel:   "R710",
		oidSystemSerial:  "421803003168",
		oidSystemVersion: "200.15.6.212 build 27",
		oidStatsNumAP:    uint64(2),
		oidStatsNumClient: uint64(28),
		// Two APs in the table: one up (1), one down (0).
		apTable + ".3.6.24.75.13.34.224.192":  1,
		apTable + ".3.6.44.197.211.35.33.224": 0,
		apTable + ".4.6.24.75.13.34.224.192":  "R710",
		apTable + ".4.6.44.197.211.35.33.224": "R710",
		// System group so the embedded generic layer has something to read.
		".1.3.6.1.2.1.1.5.0": "22BI8-Unleashed",
		".1.3.6.1.2.1.1.2.0": ".1.3.6.1.4.1.25053.3.1.5.20",
	}}
}

func find(samples []sdk.Sample, name string) (float64, bool) {
	for _, s := range samples {
		if s.Name == name {
			return s.Value, true
		}
	}
	return 0, false
}

func TestWirelessMetrics(t *testing.T) {
	samples, err := New().CollectHealth(context.Background(), r710())
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := find(samples, "netinv_wireless_ap_count"); !ok || v != 2 {
		t.Errorf("ap_count = %v, want 2", v)
	}
	if v, ok := find(samples, "netinv_wireless_client_count"); !ok || v != 28 {
		t.Errorf("client_count = %v, want 28", v)
	}
	if v, ok := find(samples, "netinv_wireless_ap_up_count"); !ok || v != 1 {
		t.Errorf("ap_up_count = %v, want 1 (one AP reports down)", v)
	}
	if v, ok := find(samples, "netinv_wireless_ap_total"); !ok || v != 2 {
		t.Errorf("ap_total = %v, want 2", v)
	}
	// This platform has no host health; it must not invent any.
	for _, forbidden := range []string{
		"netinv_device_cpu_percent", "netinv_device_memory_percent",
		"netinv_sensor_temperature_celsius",
	} {
		if _, ok := find(samples, forbidden); ok {
			t.Errorf("%s reported, but this platform exposes no host health", forbidden)
		}
	}
}

func TestInventoryIdentity(t *testing.T) {
	snap, err := New().CollectInventory(context.Background(), r710())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Vendor != "Ruckus" || snap.Model != "R710" ||
		snap.Serial != "421803003168" || !strings.HasPrefix(snap.OSVersion, "200.15.6.212") {
		t.Errorf("identity = %+v", snap)
	}
}

func TestMatchesRuckus(t *testing.T) {
	best, _ := sdk.BestMatch(sdk.SysInfo{
		SysObjectID: ".1.3.6.1.4.1.25053.3.1.5.20", SysDescr: "Ruckus Wireless R710"})
	if best == nil || best.Info().ID != "ruckus" {
		t.Fatalf("best match = %v, want ruckus", best)
	}
}
