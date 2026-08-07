// Package zte — ZTE ZXR10 connector. Best-effort per risk R-07: ZTE MIB
// coverage varies widely by line; v1 ships IF-MIB/system/LLDP via generic
// with vendor identification. Health OID maps land after real-hardware
// validation (doc 10 §5).
package zte

import (
	"github.com/freezxp/netinv/connectors/generic"
	"github.com/freezxp/netinv/connectors/sdk"
)

func init() { sdk.Register(New()) }

func New() *Connector { return &Connector{} }

type Connector struct{ generic.Base }

func (c *Connector) Info() sdk.Info {
	return sdk.Info{
		ID: "zte-zxr", Vendor: "ZTE", DisplayName: "ZTE ZXR10 (best-effort)",
		Version:             "0.1.0",
		SysObjectIDPrefixes: []string{".1.3.6.1.4.1.3902."},
	}
}

func (c *Connector) Match(sys sdk.SysInfo) sdk.MatchScore {
	return sdk.PrefixScore(sys, c.Info().SysObjectIDPrefixes)
}
