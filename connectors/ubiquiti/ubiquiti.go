// Package ubiquiti — EdgeMAX/airOS/UniFi-SNMP connector. SNMP-first per C3:
// IF-MIB/system via generic; the UniFi Controller REST connector is the
// roadmap item that exercises the non-SNMP Session seam (doc 29).
package ubiquiti

import (
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

func (c *Connector) Match(sys sdk.SysInfo) sdk.MatchScore {
	return sdk.PrefixScore(sys, c.Info().SysObjectIDPrefixes)
}
