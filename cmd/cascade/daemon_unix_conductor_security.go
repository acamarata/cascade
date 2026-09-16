//go:build !windows

// Purpose: builds the five R-21.206 security-pipeline collaborators the
//
//	daemon's conductor.execute door needs before Pipeline.Ready() will let
//	any call through.
//
// WHY: until this landed, wireConductorExecute passed none of them, so the
//
//	executor constructed and then refused every single call with
//	"conductor: security pipeline not ready". The W3 hardening gate caught
//	it in the tagged artifact — a real `cascade run` against a real,
//	live-verified provider, refused at cascade's own door (R-14.243,
//	P1-E10-W4-S87-T1).
//
// Inputs: the daemon's path provider (for the vault directory the egress
//
//	firewall's value source reads).
//
// Outputs: a daemon.ConductorSecurity, or a real construction error.
// Constraints: every collaborator here is a REAL implementation. A zero
//
//	ConductorSecurity would restore the refuse-everything behaviour, so a
//	failure to build one is propagated, never swallowed into a partial
//	struct that Ready() would reject with a message naming nothing.
//
// SPORT: cmd/cascade/daemon:conductor-security (ADD) — P1-E10-W4-S87-T1.
package main

import (
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/hooks/egress"
	providerdispatch "github.com/acamarata/cascade/internal/providers/dispatch"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// detectorScanner adapts *secrets.Detector to conductor.CredentialScanner.
//
// The adapter lives here rather than in internal/conductor because this is
// the package that already imports both sides; conductor states the seam in
// plain strings precisely so it never needs internal/secrets' types.
type detectorScanner struct{ detector *secrets.Detector }

// ScanCertainClasses returns the class names of the certain hits only.
// Ambiguous signals are dropped by ScanCertain itself: a prompt refused on
// a guess is worse than useless, because the operator learns to ignore it.
func (d detectorScanner) ScanCertainClasses(content string) []string {
	hits := d.detector.ScanCertain([]byte(content))
	if len(hits) == 0 {
		return nil
	}
	out := make([]string, 0, len(hits))
	for _, hit := range hits {
		out = append(out, string(hit.Class))
	}
	return out
}

// conductorSecurity builds the five collaborators.
func conductorSecurity(paths runtime.PathProvider) (daemon.ConductorSecurity, error) {
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		return daemon.ConductorSecurity{}, err
	}
	classifier, err := conductor.NewContentClassifier(detectorScanner{detector: detector})
	if err != nil {
		return daemon.ConductorSecurity{}, err
	}
	firewall, err := daemonEgressFirewall(paths, detector)
	if err != nil {
		return daemon.ConductorSecurity{}, err
	}
	return daemon.ConductorSecurity{
		Classifier: classifier,
		Taxonomy:   conductor.TaskClassRegistry{},
		// No deny store is wired yet: `cascade policy` owns the operator's
		// rules and has no daemon-side reader at this call site. nil means
		// "no deny rules configured", which NewOwnerPolicy documents as the
		// deliberate default HERE — identity is proven by the socket-owner
		// check below this seam, not re-proven by a rule.
		Policy:      conductor.NewOwnerPolicy(nil),
		Sensitivity: conductor.FailClosedSensitivity{},
		Firewall:    firewall,
	}, nil
}

// daemonEgressFirewall builds the outbound firewall the pipeline enforces.
//
// It uses the SAME custody the `cascade vault` commands use, through an
// EgressVault — the unelevated value source internal/secrets already ships
// for exactly this purpose: the firewall's exact-value substitution pass
// runs on the daemon's own path with no operator present, so raising an
// elevation prompt there would either hang the daemon or be answered by
// nobody. That reasoning is unelevated.go's, not this file's; this call
// site only supplies the custody.
func daemonEgressFirewall(paths runtime.PathProvider, detector *secrets.Detector) (*egress.Engine, error) {
	if paths == nil {
		return nil, cascade.New(cascade.KindUnavailable,
			"conductor: the egress firewall needs a resolved data directory")
	}
	custody, err := secrets.SelectCustody(secrets.Config{Service: vaultService, Dir: paths.DataDir()})
	if err != nil {
		return nil, err
	}
	// No elevation gate: this broker exists only to back the EgressVault,
	// whose Get is the documented unelevated path. Broker.Get itself still
	// refuses without a gate, so nothing here gains an elevated read.
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		return nil, err
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		return nil, err
	}
	return egress.NewEngine(egress.DefaultRegistry(), vault, detector)
}

// daemonCredentialSource builds the resolver's credential reader.
//
// It opens the SAME custody and the SAME grant register the `cascade vault`
// commands use, so a grant issued at the terminal is the grant the daemon
// reads under — there is one register, not a daemon-side copy.
//
// No elevation gate is passed: the daemon has no human to prove presence,
// which is the whole reason grants exist. Broker.Get itself still refuses
// without a gate, so this path gains no elevated read; only GetGranted
// succeeds, and only for a key a human has granted.
func daemonCredentialSource(paths runtime.PathProvider) (*providerdispatch.GrantedCredentials, error) {
	if paths == nil {
		return nil, cascade.New(cascade.KindUnavailable,
			"conductor: the credential source needs a resolved data directory")
	}
	dir := paths.DataDir()
	custody, err := secrets.SelectCustody(secrets.Config{Service: vaultService, Dir: dir})
	if err != nil {
		return nil, err
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		return nil, err
	}
	store, err := secrets.NewFileGrantStore(dir)
	if err != nil {
		return nil, err
	}
	grants, err := secrets.NewGrants(store, runtime.NewSystemClock())
	if err != nil {
		return nil, err
	}
	return providerdispatch.NewGrantedCredentials(broker, grants)
}
