// Purpose (this file): the outbound firewall Epic W's bridges write through —
//
//	an *egress.Engine bound to this host's vault, so the substitution pass has
//	a real value source and a stored secret that reached a bridge reply is
//	replaced before the transport sees it.
//
// Inputs: the custody selection the host uses for every other vault read.
// Outputs: a ready engine, or a typed refusal. Never a half-built engine: an
//
//	engine with no value source runs only the credential-SHAPE half of the
//	substitution pass, and a stored secret without credential shape then
//	crosses unredacted — the exact defect this ticket's review found.
//
// WHY THIS LIVES HERE, and not in the composition root that calls it.
// secrets.NewEgressVault is the NON-ELEVATED vault read, and
// internal/build/arch_secrets_test.go's rule 4 limits which directories may
// name it. The argument for the exemption is unelevated.go's own: an outbound
// firewall runs on the daemon's path with no operator present, so an
// attestation prompt per intercepted message is not a control anybody can
// answer, and a firewall that cannot read the vault cannot redact what is in
// it. That is the same argument internal/mcp already holds the exemption
// under. Putting the construction in THIS package rather than in
// internal/plugins keeps the grant as narrow as the argument: one small,
// single-purpose package, not the whole plugin-wiring tree.
//
// SPORT: internal.bridge.NewFirewall/ADDED (P1-E23-W5-S48-T1).

package bridge

import (
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
)

// NewFirewall builds the egress engine the bridge classes write through.
//
// No elevation gate is passed to the broker, deliberately and for the reason
// daemon_unix_conductor_security.go's daemonEgressFirewall states: this broker
// exists only to back the value source, Broker.Get itself still refuses without
// a gate, and so nothing here gains an elevated read.
//
// COLLECT, THEN CHECK: each constructor below fails only on a nil or empty
// argument, and each tolerates a nil input from the step before it
// (NewEgressVault refuses a nil broker, NewEngine refuses a nil vault), so one
// check reports the first real failure rather than three guards of which two
// can never fire.
func NewFirewall(cfg secrets.Config) (*egress.Engine, error) {
	detector, derr := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	custody, cerr := secrets.SelectCustody(cfg)
	broker, berr := newBroker(custody, cerr)
	vault, verr := secrets.NewEgressVault(broker)
	engine, eerr := egress.NewEngine(egress.DefaultRegistry(), vault, detector)
	if err := firstErr(derr, cerr, berr, verr, eerr); err != nil {
		return nil, err
	}
	return engine, nil
}

// newBroker builds the broker only when custody selection succeeded, so a
// failed selection produces one error rather than a second, misleading one
// about a nil custody.
func newBroker(custody secrets.Custody, selectErr error) (*secrets.Broker, error) {
	if selectErr != nil {
		return nil, selectErr
	}
	return secrets.NewBroker(custody, nil)
}

// firstErr returns the first non-nil error, or nil.
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
