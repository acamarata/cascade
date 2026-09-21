// Purpose (this file): the confirmation gate wiki sync's L3 push crosses
//
//	(06-FORGE-SPEC.md §5.8 automation parity, §5.23 CASCADE_NO_INPUT hard-
//	error pattern). `cascade github wiki check` never calls this: drift
//	detection is read-only (L1-L2) and needs no confirmation.
//
// Inputs: whether the caller already passed --yes, and an injected Getenv
//
//	(defaults to os.Getenv) so a test drives CASCADE_NO_INPUT without
//	mutating the process environment.
//
// Outputs: nil when the push may proceed, or a typed KindElevationRequired
//
//	refusal naming exactly why it may not.
//
// Constraints: this package speaks stdio JSON-RPC to the host (main.go);
//
//	it has no terminal of its own to prompt on, so "prompts for
//	confirmation" is the CALLER's responsibility (the CLI layer that
//	collects --yes or an interactive y/N before the RPC call is ever
//	made) — the same division T1 documents for github-repos/issues/prs'
//	still-unmounted CLI verbs. What THIS function guarantees is the part
//	that must never depend on a UI existing: an unconfirmed push refuses,
//	and CASCADE_NO_INPUT=1 without --yes refuses with the distinct
//	"no prompt was attempted" reason rather than a generic one, exactly
//	matching cmd/cascade/backup_elevation.go's confirmBackupOperation
//	convention.
//
// SPORT: plugins/github/wiki:confirm (ADD) — P1-E25-W5-S51-T6.

package wiki

import (
	"os"

	"github.com/acamarata/cascade/pkg/cascade"
)

// noInputEnvVar is the CASCADE_NO_INPUT convention (06 §5.23).
const noInputEnvVar = "CASCADE_NO_INPUT"

// defaultGetenv is the production environment reader.
func defaultGetenv(key string) string { return os.Getenv(key) }

// requireConfirmation refuses to push without an explicit --yes. When the
// caller has not confirmed AND CASCADE_NO_INPUT=1, the refusal names that
// exact condition ("no prompt was attempted") rather than a generic
// "confirmation required", so an operator scripting a headless pipeline
// sees why nothing prompted them.
func requireConfirmation(yes bool, getenv func(string) string) error {
	if yes {
		return nil
	}
	if getenv == nil {
		getenv = defaultGetenv
	}
	if getenv(noInputEnvVar) == "1" {
		return cascade.New(cascade.KindElevationRequired,
			"cascade-github wiki sync requires --yes when CASCADE_NO_INPUT=1; no prompt was attempted")
	}
	return cascade.New(cascade.KindElevationRequired,
		"cascade-github wiki sync requires confirmation before pushing; pass --yes or confirm the prompt")
}
