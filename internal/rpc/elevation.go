package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// codeElevationRequired reuses pkg/cascade's existing application error
// code table verbatim (RPCCodeElevationRequired = -32008, the wire code for
// cascade.KindElevationRequired) rather than defining a second mapping —
// this is the one case in this package where an application-taxonomy code,
// not a protocol-framing code, is the correct choice: ELEVATION_REQUIRED is
// exactly what the taxonomy's KindElevationRequired kind exists to name.
const codeElevationRequired = cascade.RPCCodeElevationRequired

// elevatedEnvelope is the params wire shape for a call to an elevated
// method. On a FIRST call (no attestation yet), a client sends its normal
// params directly and the whole params blob is what gets hashed and bound
// into the nonce; on a RETRY carrying a satisfying attestation, the client
// wraps its original params under "_args" alongside "_attestation". This
// concrete envelope shape is a design decision this ticket makes explicit
// (see elevation_attest.go's signedFields doc comment for the parallel
// decision on the signed-bytes format) since S-07.T6, which mints the
// attestation client-side, had not landed a wire convention as of this
// ticket.
type elevatedEnvelope struct {
	Attestation *Attestation    `json:"_attestation,omitempty"`
	Args        json.RawMessage `json:"_args,omitempty"`
}

// elevationRequiredData is the ELEVATION_REQUIRED error's Data payload:
// the nonce the client's next attestation-bearing retry must present.
type elevationRequiredData struct {
	Reason string `json:"reason"`
	Nonce  string `json:"nonce"`
}

// verbRule describes one §5.14 elevated-verb table entry. Always is true
// for a verb that is unconditionally elevated; Conditional, when non-nil,
// is consulted for a verb elevated only under specific params (e.g.
// "plugin.add" is elevated only when it requests a process-tier or
// grant-expansion change) — see elevationTable's per-entry comments for
// which spec clause each Conditional encodes.
type verbRule struct {
	method      string
	always      bool
	conditional func(params json.RawMessage) bool
}

// The three helpers below back the conditional rules in elevationTable.
// All of them FAIL CLOSED: when a helper cannot prove that a request is the
// harmless case, it reports true and the verb is elevated.
//
// The direction matters more than it looks. These helpers do not decide
// whether a request is valid, they decide whether it needs authorisation.
// A helper that returns false on input it could not parse hands an attacker
// a bypass: send the privileged verb with a payload the helper chokes on and
// the elevation gate disappears, while the handler downstream may still
// decode the payload its own way and act on it. Refusing to guess, and
// demanding elevation whenever the answer is unclear, costs a legitimate
// caller one authorisation prompt on a malformed request. Guessing the other
// way costs the user the gate.

// unparseableParams reports whether raw cannot be read as a JSON object.
// Callers treat this as "elevate": params we cannot read cannot be shown to
// be a narrowing.
func unparseableParams(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, true
	}
	return m, false
}

// hasJSONField reports whether raw contains key with any non-null value.
// Unreadable params elevate.
func hasJSONField(raw json.RawMessage, key string) bool {
	m, bad := unparseableParams(raw)
	if bad {
		return true
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	return string(v) != "null"
}

// jsonFieldEquals reports whether raw's key field equals want. Unreadable
// params elevate, and so does a value present in some shape other than the
// string this rule expects, because such a value cannot be shown to differ
// from want.
func jsonFieldEquals(raw json.RawMessage, key, want string) bool {
	m, bad := unparseableParams(raw)
	if bad {
		return true
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return true
	}
	return s == want
}

// jsonFieldEnables reports whether raw's key field requests turning a flag
// ON. It accepts the JSON boolean true and the string "true", because both
// reach the daemon in practice: a typed client marshals a Go bool, while a
// shell client sending hand-built JSON often quotes it.
//
// Only a value that positively reads as OFF returns false. An absent key
// returns false as well, since a request that does not mention the flag is
// not turning it on. Everything else elevates.
func jsonFieldEnables(raw json.RawMessage, key string) bool {
	m, bad := unparseableParams(raw)
	if bad {
		return true
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	var b bool
	if err := json.Unmarshal(v, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "false", "0", "no", "off", "":
			return false
		}
		return true
	}
	return true
}

// elevationTable is the authoritative elevated-verb table, transcribed from
// 06-FORGE-SPEC.md's numbered item 14 (cited by this ticket's contract and
// by 15-T0-RULINGS-R14.md as "§5.14"), amended by R-14.48 for
// enable_remote_runtime. elevation_test.go's TestElevationTableMatchesSpec
// independently re-transcribes the same spec prose into a second data
// structure and asserts set-equality against this table's method names, so
// a typo or omission here fails that test rather than merely reflecting
// itself.
var elevationTable = []verbRule{
	{method: "vault.get", always: true},
	{method: "vault.rotate", always: true},
	{method: "approval.grant", always: true},
	{method: "standing_grant.create", always: true},
	{method: "standing_grant.change", always: true},
	{method: "backup.create", always: true},
	{method: "backup.export", always: true},
	{method: "backup.import", always: true},
	{method: "backup.restore", always: true},
	{method: "backup.key_export", always: true},
	{method: "backup.key_import", always: true},
	// plugin.add is elevated only when it requests a process-tier change
	// or a grant expansion, per §5.14's parenthetical.
	{method: "plugin.add", conditional: func(p json.RawMessage) bool {
		return hasJSONField(p, "process_tier") || hasJSONField(p, "grant_expand")
	}},
	{method: "perms.grant", always: true},
	{method: "perms.revoke", always: true},
	{method: "node.enroll", always: true},
	{method: "node.remove", always: true},
	{method: "node.upgrade", always: true},
	// sync.conflicts_resolve is elevated only when the resolution
	// discards local state in favor of server-primary, per §5.14.
	{method: "sync.conflicts_resolve", conditional: func(p json.RawMessage) bool {
		return jsonFieldEquals(p, "resolution", "server-primary")
	}},
	// policy/sensitivity changes are elevated only when they LOOSEN
	// (widen) the resolved value, per §5.14 + §5 item 16's "widening a
	// resolved tier is a LOOSENING (elevated verb); narrowing is always
	// allowed."
	{method: "policy.set", conditional: func(p json.RawMessage) bool {
		return jsonFieldEquals(p, "direction", "loosen")
	}},
	{method: "sensitivity.set", conditional: func(p json.RawMessage) bool {
		return jsonFieldEquals(p, "direction", "loosen")
	}},
	{method: "elevation.set_allow_remote", conditional: func(p json.RawMessage) bool {
		return jsonFieldEnables(p, "allow_remote") || jsonFieldEnables(p, "enable")
	}},
	// R-14.48: enabling [plugins].enable_remote_runtime is elevated.
	{method: "plugins.set_enable_remote_runtime", conditional: func(p json.RawMessage) bool {
		return jsonFieldEnables(p, "enable_remote_runtime") || jsonFieldEnables(p, "enable")
	}},
	{method: "uninstall.purge_data", always: true},
}

// IsElevated reports whether method (with the given raw params) is an
// elevated verb per elevationTable.
func IsElevated(method string, params json.RawMessage) bool {
	for _, rule := range elevationTable {
		if rule.method != method {
			continue
		}
		if rule.always {
			return true
		}
		return rule.conditional != nil && rule.conditional(params)
	}
	return false
}

// ElevationMiddleware builds the elevation MiddlewareFunc: for a
// non-elevated method it is a pure pass-through; for an elevated method
// without a satisfying attestation it returns ELEVATION_REQUIRED + nonce;
// with an attestation attached (elevatedEnvelope._attestation) it verifies
// and, on success, calls next with the original ("_args") params.
// platformElevationRefusal (elevation_unix.go / elevation_windows.go)
// supplies the tier-2 Windows override.
func ElevationMiddleware(ledger *NonceLedger, trust TrustStore, clock runtime.Clock) MiddlewareFunc {
	return func(method string, next HandlerFunc) HandlerFunc {
		return func(ctx context.Context, params json.RawMessage) (any, error) {
			var env elevatedEnvelope
			_ = json.Unmarshal(params, &env)

			checkParams := params
			if env.Attestation != nil {
				checkParams = env.Args
			}
			if !IsElevated(method, checkParams) {
				return next(ctx, params)
			}
			if refusal := platformElevationRefusal(); refusal != nil {
				return nil, refusal
			}
			if env.Attestation == nil {
				return nil, requireElevation(ledger, method, params)
			}
			return attestAndProceed(ctx, *env.Attestation, ledger, trust, clock, method, env.Args, next)
		}
	}
}

// requireElevation issues a fresh nonce bound to {method, paramsHash} and
// returns the ELEVATION_REQUIRED error carrying it.
func requireElevation(ledger *NonceLedger, method string, params json.RawMessage) *ErrorObject {
	hash := hashParams(params)
	nonce, err := ledger.Issue(method, hash)
	if err != nil {
		return errorObjectFrom(err)
	}
	return &ErrorObject{
		Code:    codeElevationRequired,
		Message: "elevation required: " + method,
		Data:    elevationRequiredData{Reason: "ELEVATION_REQUIRED", Nonce: nonce},
	}
}

// attestAndProceed verifies att against the original ("_args") params and,
// on success, dispatches to next with those original params; on failure it
// returns the typed denial as-is.
func attestAndProceed(ctx context.Context, att Attestation, ledger *NonceLedger, trust TrustStore,
	clock runtime.Clock, method string, args json.RawMessage, next HandlerFunc) (any, error) {
	hash := hashParams(args)
	now := elevationNow(clock)
	if err := VerifyAttestation(att, trust, ledger, method, hash, now); err != nil {
		return nil, err
	}
	return next(ctx, args)
}

// elevationNow reads the injected clock, never the wall clock directly
// (R-14.132's forbidigo + AST alias gate).
func elevationNow(clock runtime.Clock) time.Time {
	return clock.Now()
}
