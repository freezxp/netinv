// Package registry pulls in every in-tree connector via side-effect imports
// (ADR-014). Adding a platform = new package + one line here. Imported ONLY
// by cmd/poller (doc 13 rule 5).
package registry

import (
	_ "github.com/freezxp/netinv/connectors/cisco"
	_ "github.com/freezxp/netinv/connectors/generic"
	_ "github.com/freezxp/netinv/connectors/huawei"
	_ "github.com/freezxp/netinv/connectors/juniper"
	_ "github.com/freezxp/netinv/connectors/ubiquiti"
	_ "github.com/freezxp/netinv/connectors/zte"
)
