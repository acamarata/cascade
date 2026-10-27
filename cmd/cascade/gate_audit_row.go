// Purpose: the local elevation gate's audit rows: a closed vocabulary the
//
//	CLI, the daemon's audit.gate_append verb and the direct store path all
//	validate the same way, and the one typed log they are appended through.
//
// Inputs: a gateRow (kind, verb, reason code, params hash, and for
//
//	vault.access only a secret name).
//
// Outputs: gateRow.validate, gateRow.event (the audit.Event built from the
//
//	validated fields only) and gateRowLog, which appends nothing else.
//
// Constraints: no free text reaches a gate row. Kind and reason come from
//
//	fixed sets, the verb and the name must match strict identifier grammars,
//	the hash must be hex. The log has no redactor, so appending a gate row
//	never reads the vault: there is nothing in a row to redact.
//
// SPORT: cmd/cascade/vault elevation gate audit rows (P1-SEC-08).

package main

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// gateAppendMethod is the daemon verb that appends one gate row.
const gateAppendMethod = "audit.gate_append"

// gateRowSource is the unverified provenance every gate row carries.
const gateRowSource = "cli-gate"

// gateRowMaxBytes bounds one encoded row on the wire.
const gateRowMaxBytes = 512

// gateRow is one gate audit row. Every field is validated against a fixed
// vocabulary before it is written anywhere.
type gateRow struct {
	Kind       string `json:"kind"`
	Verb       string `json:"verb"`
	Reason     string `json:"reason,omitempty"`
	ParamsHash string `json:"params_hash"`
	Name       string `json:"name,omitempty"`
}

// gateRowKinds maps each allowed kind to its audit kind and outcome.
var gateRowKinds = map[string]struct {
	kind    audit.Kind
	outcome string
}{
	string(audit.KindElevationAttempt): {audit.KindElevationAttempt, "attempt"},
	string(audit.KindElevationGrant):   {audit.KindElevationGrant, "grant"},
	string(audit.KindElevationDeny):    {audit.KindElevationDeny, "deny"},
	string(audit.KindVaultAccess):      {audit.KindVaultAccess, "requested"},
}

// gateLocalReasons are the gate's own deny codes; every taxonomy kind name
// (a middleware refusal's code) is allowed too.
var gateLocalReasons = []string{
	"no_input", "not_configured", "custody_tier", "unbound", "not_elevated", "challenge_refused",
	"sign_failed", "no_signature", "encode_failed", "not_granted", "daemon_required", "denied",
}

var (
	gateVerbPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$`)
	gateHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gateNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.-]{0,127}$`)
)

// gateReasonAllowed reports whether reason is a gate code or a kind name.
func gateReasonAllowed(reason string) bool {
	for _, r := range gateLocalReasons {
		if r == reason {
			return true
		}
	}
	for k := cascade.Kind(1); k <= cascade.KindInternal; k++ {
		if k.String() == reason {
			return true
		}
	}
	return false
}

// validate refuses any row outside the vocabulary.
func (r gateRow) validate() error {
	spec, ok := gateRowKinds[r.Kind]
	switch {
	case !ok:
		return cascade.New(cascade.KindInvalidInput, "gate audit: unknown row kind")
	case len(r.Verb) > 64 || !gateVerbPattern.MatchString(r.Verb):
		return cascade.New(cascade.KindInvalidInput, "gate audit: the verb is not an identifier")
	case !gateHashPattern.MatchString(r.ParamsHash):
		return cascade.New(cascade.KindInvalidInput, "gate audit: the params hash is not a hex digest")
	case spec.kind == audit.KindVaultAccess && (!gateNamePattern.MatchString(r.Name) || r.Reason != ""):
		return cascade.New(cascade.KindInvalidInput, "gate audit: a vault.access row needs a secret name and no reason")
	case spec.kind != audit.KindVaultAccess && r.Name != "":
		return cascade.New(cascade.KindInvalidInput, "gate audit: only a vault.access row names a secret")
	case spec.kind == audit.KindElevationDeny && !gateReasonAllowed(r.Reason):
		return cascade.New(cascade.KindInvalidInput, "gate audit: a deny needs a known reason code")
	case spec.kind != audit.KindElevationDeny && r.Reason != "":
		return cascade.New(cascade.KindInvalidInput, "gate audit: only a deny carries a reason")
	}
	return nil
}

// event builds the audit event from the validated fields only.
func (r gateRow) event() (audit.Event, error) {
	if err := r.validate(); err != nil {
		return audit.Event{}, err
	}
	spec := gateRowKinds[r.Kind]
	// source marks every gate row as written by a CLI process and NOT
	// verified by the daemon; the daemon's own elevation rows (actor
	// "elevation") never carry it, and a gate row can never carry theirs.
	explain := map[string]any{"local": true, "reason": r.Reason, "source": gateRowSource}
	actor := "elevation.local"
	if spec.kind == audit.KindVaultAccess {
		explain, actor = map[string]any{"name": r.Name, "verb": r.Verb, "source": gateRowSource}, "cli"
	}
	raw, err := json.Marshal(explain)
	if err != nil {
		return audit.Event{}, cascade.Wrap(cascade.KindInternal, err, "gate audit: encode the row")
	}
	return audit.Event{Kind: spec.kind, Actor: actor, Action: r.Verb, ParamsHash: r.ParamsHash,
		Outcome: spec.outcome, Explain: raw}, nil
}

// gateSink receives gate rows. The returned sequence number is the
// committed record's (0 when the sink cannot report one).
type gateSink interface {
	AppendGateRow(ctx context.Context, row gateRow) (uint64, error)
}

// gateRowLog appends validated gate rows to an audit log and nothing else.
type gateRowLog struct{ log audit.Writer }

// AppendGateRow implements gateSink.
func (l gateRowLog) AppendGateRow(ctx context.Context, row gateRow) (uint64, error) {
	if l.log == nil {
		return 0, cascade.New(cascade.KindUnavailable, "gate audit: no audit log is configured")
	}
	event, err := row.event()
	if err != nil {
		return 0, err
	}
	rec, err := l.log.Append(ctx, event)
	if err != nil {
		return 0, err
	}
	return rec.Seq, nil
}
