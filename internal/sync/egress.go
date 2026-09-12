// Purpose: registers the sync egress class (§D-30, 06 §5.17) on the
//   shared egress.DefaultRegistry at this package's own init, per this
//   ticket's registrant convention: the CONSTANT lives in
//   internal/hooks/egress/classes.go (this ticket appended it there);
//   the REGISTRATION lives here, in the registrant package, exactly as
//   every self-registering subsystem in this tree does it.
// Inputs: none (init-time side effect).
// Outputs: internal/hooks/egress.DefaultRegistry() carries
//   EgressClassSync after this package is imported anywhere in a build.
// Constraints: AllowRestricted is NOT set. filter.go's pre-serialization
//   gate already refuses local-only/restricted records before anything
//   reaches this class, so the class itself carries no wider admission
//   than the default — a caller cannot bypass filter.go by writing
//   restricted content straight to this class, because the class refuses
//   it independently (defense in depth, R-21.223's §G.2 intent).
// SPORT: internal.sync.egress/ADDED (P1-E17-W4-S38-T1).

package sync

import "github.com/acamarata/cascade/internal/hooks/egress"

func init() {
	egress.DefaultRegistry().MustRegister(egress.EgressClassSync, egress.InterceptConfig{
		Enabled: true,
		Owner:   "P1-E17-W4-S38-T1",
	})
}
