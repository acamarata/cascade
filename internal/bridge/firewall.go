// Purpose (this file): the outbound firewall Epic W's bridges write through —
//
//	the process firewall's engine over the bridge classes, so the substitution
//	pass resolves against the process vault and a stored secret that reached a
//	bridge reply is replaced before the transport sees it.
//
// Inputs: the process firewall the composition root installed
//
//	(egress.ProcessEngine).
//
// Outputs: a ready engine, or a typed refusal. Never a half-built engine: an
//
//	engine with no value source runs only the credential-SHAPE half of the
//	substitution pass, and a stored secret without credential shape then
//	crosses unredacted.
//
// Constraints: this package builds no engine and opens no vault. One
//
//	firewall per process: the composition root owns the custody selection
//	and the non-elevated vault read, so no package below it can bind a
//	different, empty or fake value source.
//
// SPORT: internal.bridge.NewFirewall/CHANGED (process firewall).

package bridge

import (
	"github.com/acamarata/cascade/internal/hooks/egress"
)

// NewFirewall returns the engine the bridge classes write through: the
// process firewall over the default registry. With no process firewall
// installed it refuses with egress.ErrNoProcessFirewall; a custody failure
// is returned with its Kind and cause unchanged.
func NewFirewall() (*egress.Engine, error) {
	return egress.ProcessEngine(egress.DefaultRegistry())
}
