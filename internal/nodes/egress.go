package nodes

import "github.com/acamarata/cascade/internal/hooks/egress"

// Purpose: registers the node-dispatch egress class (§D-30, 06 §5.17) on
//
//	the shared registry at this package's own init, per the registrant
//	convention (R-16.56): the CONSTANT lives in
//	internal/hooks/egress/classes.go, the REGISTRATION lives here, in the
//	package that actually emits the traffic.
//
// Inputs: none (init-time side effect).
// Outputs: egress.DefaultRegistry() carries EgressClassNodeDispatch once
//
//	this package is linked into a build.
//
// Constraints: AllowRestricted is NOT set. Restricted work reaches a node
//
//	only after placement has proved that node's trust tier; this class
//	carries no wider admission than any other default, so writing straight
//	to it cannot bypass that proof.
//
// SPORT: internal/nodes:egress (ADD) — P1-E17-W4-S37-T2.
func init() {
	egress.DefaultRegistry().MustRegister(egress.EgressClassNodeDispatch, egress.InterceptConfig{
		Enabled: true,
		Owner:   "P1-E17-W4-S37-T2",
	})
}
