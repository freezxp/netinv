// Package registry pulls in every in-tree connector via side-effect imports
// (ADR-014). Adding a platform = new package + one line here. Imported ONLY
// by cmd/poller (doc 13 rule 5).
package registry

import (
	_ "github.com/freezxp/netinv/connectors/generic"
	// Sprint 17: cisco, juniper, huawei, zte, ubiquiti
)
