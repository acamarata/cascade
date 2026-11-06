package fixture

import rpc "github.com/acamarata/cascade/internal/rpc"

func init() {
	rpc.RegisterElevatedVerb("fixture.always", rpc.ElevationRule{Always: true})
	rpc.RegisterElevatedVerb("fixture.conditional", rpc.ElevationRule{Conditional: predicate})
	rpc.RegisterElevatedVerb("fixture.both", rpc.ElevationRule{Always: true, Conditional: predicate})
	rpc.RegisterElevatedVerb("fixture.neither", rpc.ElevationRule{})
	rpc.RegisterElevatedVerb("fixture.flag", rpc.ElevationRule{Always: flag})
}
