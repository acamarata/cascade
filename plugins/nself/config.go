// Purpose (this file): the ConfigApplier seam — the one place this
//
//	plugin can ask the host to write the proposed server-profile diff
//	through internal/runtime's ConfigWriter.ApplyDiff. The default is a
//	refusing applier, mirroring doctor.go's EgressInterceptor: an unbound
//	applier means `cascade nself handshake`'s APPLY mode refuses rather
//	than silently doing nothing.
//
// Inputs: an owner name and the diff entries a handshake proposes.
// Outputs: a ConfigResult naming every entry Applied/Unchanged/Skipped,
//
//	or a typed error.
//
// Constraints: same internal/** import ban as the rest of this package
//
//	(Art.10.2) — ConfigApplier is this package's own local mirror of
//	internal/runtime's ApplyDiff shape, bound by the composition root
//	(internal/plugins/nself_wiring.go), exactly as EgressInterceptor is.
//
// SPORT: plugins/nself config (ADD) — P1-E25-W5-S103-T1.

package nself

import (
	"context"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ConfigEntry is this package's local mirror of internal/runtime's
// DiffEntry: a dotted config path and a TOML-literal value string.
type ConfigEntry struct {
	Path    string
	Literal string
}

// ConfigOutcome mirrors internal/runtime's DiffOutcome: one entry's
// classification.
type ConfigOutcome struct {
	Path   string
	Reason string
}

// ConfigResult mirrors internal/runtime's DiffResult.
type ConfigResult struct {
	Applied   []ConfigOutcome
	Unchanged []ConfigOutcome
	Skipped   []ConfigOutcome
}

// ConfigApplier is the config-write seam the human-invoked `cascade nself
// handshake` APPLY mode transits. internal/plugins/nself_wiring.go binds
// the REAL internal/runtime ConfigWriter.ApplyDiff to it at process boot,
// resolving the runtime paths and ConfigWriter at call time.
type ConfigApplier interface {
	// ApplyDiff writes entries owned by owner, all-or-nothing.
	ApplyDiff(ctx context.Context, owner string, entries []ConfigEntry) (ConfigResult, error)
}

// ErrNoConfigApplier is the refusal identity an unbound applier produces.
// Exported so the host bridge (and its test) can assert THIS condition
// rather than matching on message text — cascade.Error's own errors.Is
// compares Kind only, so a Kind check alone would match any other
// KindUnavailable failure on this path.
var ErrNoConfigApplier = errors.New(
	"cascade-nself: no config-apply seam is bound; the host composition root must call SetConfigApplier")

// refusingApplier is ConfigApplier's default: it writes nothing.
type refusingApplier struct{}

func (refusingApplier) ApplyDiff(context.Context, string, []ConfigEntry) (ConfigResult, error) {
	return ConfigResult{}, cascade.Wrap(cascade.KindUnavailable, ErrNoConfigApplier,
		"cascade-nself: refusing to apply the server-profile diff")
}

// configApplier is the active applier. Assigned once at process boot by
// the host bridge, exactly as egressInterceptor is (doctor.go).
var configApplier ConfigApplier = refusingApplier{}

// SetConfigApplier installs a as the active applier, mirroring
// SetEgressInterceptor's contract exactly: a nil a is refused rather than
// silently leaving APPLY mode unable to write without saying why.
func SetConfigApplier(a ConfigApplier) error {
	if a == nil {
		return cascade.New(cascade.KindInvalidInput,
			"cascade-nself: SetConfigApplier: applier must not be nil")
	}
	configApplier = a
	return nil
}

// LiteralScreener is the value-screen seam the handshake runs on every
// entry before proposing or applying it. internal/plugins binds it to
// internal/runtime's ScreenConfigLiteral, the same validator ConfigWriter
// Set and ApplyDiff run, so the handshake never proposes a value the
// writer would refuse (a secret split across whitespace included). There
// is no default: with no screener bound the handshake refuses outright.
type LiteralScreener interface {
	// ScreenLiteral returns a non-nil error when literal must not be
	// proposed or written at path.
	ScreenLiteral(path, literal string) error
}

// literalScreener is the active screener; nil until the host binds one.
var literalScreener LiteralScreener

// SetLiteralScreener installs s as the active screener. A nil s is
// refused, mirroring SetConfigApplier.
func SetLiteralScreener(s LiteralScreener) error {
	if s == nil {
		return cascade.New(cascade.KindInvalidInput,
			"cascade-nself: SetLiteralScreener: screener must not be nil")
	}
	literalScreener = s
	return nil
}
