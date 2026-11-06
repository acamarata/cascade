package main

import gate "github.com/acamarata/cascade/internal/rpc"

const qualified = "fixture.qualified"

var _ = gate.DeclareAccess(qualified, gate.AccessOwnerView)
var _ = gate.DeclareAccess("fixture.conditional", gate.AccessScoped)

func init() {
	gate.RegisterElevatedVerb("fixture.conditional", gate.ElevationRule{Conditional: predicate})
}
