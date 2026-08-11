package app

import "testing"

// Flow is attributed by the datagram's source address, and a router routinely
// exports from an uplink or loopback rather than its management address. Two
// pilot gateways managed on their LAN addresses export from their WAN side, so
// their flow arrived, decoded, and belonged to no device.
func TestFlowExportersValidation(t *testing.T) {
	list := func(a ...string) *[]string { return &a }

	t.Run("rejects a value that is not an address", func(t *testing.T) {
		if err := validateFlowExporters(list("192.0.2.1", "not-an-ip")); err == nil {
			t.Error("a non-address must be rejected rather than stored and never matched")
		}
	})
	t.Run("accepts addresses and trims blanks", func(t *testing.T) {
		got, err := cleanFlowExporters(list(" 192.0.2.1 ", "", "198.51.100.7"), "10.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0] != "192.0.2.1" || got[1] != "198.51.100.7" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("drops the management address", func(t *testing.T) {
		// It is always attributed already; listing it again would put a
		// duplicate into every selector built from the two.
		got, err := cleanFlowExporters(list("10.0.0.1", "192.0.2.1"), "10.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != "192.0.2.1" {
			t.Errorf("got %v, want the mgmt address removed", got)
		}
	})
	t.Run("an empty list clears rather than leaving stale entries", func(t *testing.T) {
		got, err := cleanFlowExporters(list(), "10.0.0.1")
		if err != nil || len(got) != 0 {
			t.Errorf("got %v, %v", got, err)
		}
	})
}
