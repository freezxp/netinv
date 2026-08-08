// Package ubiquiti — EdgeMAX/airOS/UniFi-SNMP connector. SNMP-first per C3:
// IF-MIB/system/health via generic; the UniFi Controller REST connector is the
// roadmap item that exercises the non-SNMP Session seam (doc 29).
package ubiquiti

import (
	"strings"

	"github.com/freezxp/netinv/connectors/generic"
	"github.com/freezxp/netinv/connectors/sdk"
)

func init() { sdk.Register(New()) }

func New() *Connector { return &Connector{} }

type Connector struct{ generic.Base }

func (c *Connector) Info() sdk.Info {
	return sdk.Info{
		ID: "ubiquiti", Vendor: "Ubiquiti", DisplayName: "Ubiquiti (SNMP)",
		Version: "0.1.0",
		SysObjectIDPrefixes: []string{
			".1.3.6.1.4.1.41112.", // UniFi
			".1.3.6.1.4.1.10002.", // airOS/EdgeMAX
		},
	}
}

// Match handles both Ubiquiti addressing styles. Older airOS/EdgeMAX gear
// answers with a Ubiquiti enterprise sysObjectID, but UniFi OS consoles
// (UDM, UDM-Pro, UDM-SE) run stock net-snmp and report the generic net-snmp
// sysObjectID (.1.3.6.1.4.1.8072.3.2.10) — verified against real UDM-Pro
// hardware — so those are only identifiable from sysDescr.
func (c *Connector) Match(sys sdk.SysInfo) sdk.MatchScore {
	if s := sdk.PrefixScore(sys, c.Info().SysObjectIDPrefixes); s > 0 {
		return s
	}
	descr := strings.ToLower(sys.SysDescr)
	for _, hint := range []string{"ubiquiti", "unifi", "udm-pro", "edgeos", "airos"} {
		if strings.Contains(descr, hint) {
			// Above the generic floor (1), below a sysObjectID prefix match.
			return 5
		}
	}
	return 0
}
