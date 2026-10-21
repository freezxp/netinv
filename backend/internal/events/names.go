package events

// Symbolic names for the notifications defined by standard MIBs. Every entry
// is an OID fixed by an RFC, so a name here cannot be wrong for a particular
// vendor. Vendor notifications (Fortinet, Palo Alto) come from
// names_vendor.go, generated from the vendors' published MIBs and not yet
// checked against a real trap from either (doc 36 §3). Anything in neither
// table is labelled by its numeric OID.
var trapNames = map[string]string{
	// SNMPv2-MIB snmpTraps (RFC 3418) and IF-MIB (RFC 2863). v1 generic traps
	// 0-5 map onto these (RFC 3584 §3.1), so both versions share a label.
	"1.3.6.1.6.3.1.1.5.1": "coldStart",
	"1.3.6.1.6.3.1.1.5.2": "warmStart",
	"1.3.6.1.6.3.1.1.5.3": "linkDown",
	"1.3.6.1.6.3.1.1.5.4": "linkUp",
	"1.3.6.1.6.3.1.1.5.5": "authenticationFailure",
	"1.3.6.1.6.3.1.1.5.6": "egpNeighborLoss",

	// BGP4-MIB: the RFC 4273 notifications and the RFC 1657 ones they replace,
	// since agents still send either.
	"1.3.6.1.2.1.15.0.1": "bgpEstablishedNotification",
	"1.3.6.1.2.1.15.0.2": "bgpBackwardTransNotification",
	"1.3.6.1.2.1.15.7.1": "bgpEstablished",
	"1.3.6.1.2.1.15.7.2": "bgpBackwardTransition",

	// ENTITY-MIB (RFC 6933).
	"1.3.6.1.2.1.47.2.0.1": "entConfigChange",
}

// TrapName returns the symbolic name for a numeric trap OID, or "".
func TrapName(oid string) string {
	if n, ok := trapNames[oid]; ok {
		return n
	}
	return vendorTrapNames[oid]
}
