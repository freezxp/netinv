package cisco

import (
	"context"
	"testing"

	"github.com/freezxp/netinv/connectors/sdk/sdktest"
)

// A chassis platform reports several processors, and this connector emits one
// series per row. That is the right call — a busy forwarding engine and a busy
// control plane are different faults — but it is only usable if the rows can be
// told apart.
//
// The case behind this test: an ASR 903 showed 99% CPU in NetInv while the CLI
// showed 30%. Both figures were real, from different processors. Labelled
// "cpu=1" and "cpu=2" there is no way to know which is which, and the
// dashboard's topk surfaces whichever is worst without saying what it is.
func TestCPUSeriesAreNamedByPhysicalEntity(t *testing.T) {
	f := sdktest.Load(t, "testdata/cisco-multicpu.snmpwalk")
	samples, err := New().CollectHealth(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}

	byLabel := map[string]float64{}
	for _, s := range samples {
		if s.Name == "netinv_device_cpu_percent" {
			byLabel[s.Labels["cpu"]] = s.Value
		}
	}
	if len(byLabel) != 2 {
		t.Fatalf("got %d CPU series, want 2 — both processors must be kept, "+
			"collapsing them hides one of two distinct faults: %v", len(byLabel), byLabel)
	}
	if v, ok := byLabel["Route Processor 0"]; !ok || v != 30 {
		t.Errorf("Route Processor 0 = %v (present=%v), want 30; labels come from "+
			"cpmCPUTotalPhysicalIndex -> entPhysicalName: %v", v, ok, byLabel)
	}
	if v, ok := byLabel["Embedded Services Processor"]; !ok || v != 99 {
		t.Errorf("Embedded Services Processor = %v (present=%v), want 99: %v", v, ok, byLabel)
	}
}

// Not every agent populates ENTITY-MIB, and plenty restrict the view. Falling
// back to the table index keeps the series rather than dropping a CPU reading
// because it could not be named.
func TestCPUFallsBackToIndexWithoutEntityNames(t *testing.T) {
	f := sdktest.Load(t, "testdata/cisco-cpu-noentity.snmpwalk")
	samples, err := New().CollectHealth(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range samples {
		if s.Name == "netinv_device_cpu_percent" {
			found = true
			if s.Labels["cpu"] == "" {
				t.Error("CPU series must always carry an identifying label")
			}
		}
	}
	if !found {
		t.Error("a CPU reading must survive even when the entity name is unavailable")
	}
}
