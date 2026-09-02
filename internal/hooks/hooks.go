package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the HookConfig TOML schema, the HookFire audit-event struct,
//
//	the two W1 action-type interfaces (PluginDispatcher, NoteWriter), and
//	the action-type refusal error this package returns for shell/unknown
//	action types.
//
// Inputs: HookConfig values decoded from config.toml's [hooks] section
//
//	(08-INIT-CONFIG-SPEC.md §3); action_params carried on a HookConfig.
//
// Outputs: a deterministic hook ID (DeriveHookID) and params hash
//
//	(paramsHash) for use by registry.go and audit.go.
//
// Constraints: see doc.go's package comment for the action-type
//
//	restriction and CONTRACT DEVIATION note on HOOK_ACTION_NOT_PERMITTED
//	below.
//
// SPORT: internal.hooks.HookConfig/ADDED, internal.hooks.HookFire/ADDED,
//
//	internal.hooks.PluginDispatcher/ADDED, internal.hooks.NoteWriter/ADDED
//	(P1-E03-W1-S05-T1).

// ActionType identifies the kind of action a hook fires. The set of
// action types this package will ever RECOGNISE (as opposed to permit) is
// deliberately closed and small — see the Register/dispatch refusal logic
// in registry.go and dispatcher.go.
type ActionType string

const (
	// ActionTypePluginCall invokes a plugin tool or intent via the
	// injected PluginDispatcher. Permitted at W1.
	ActionTypePluginCall ActionType = "plugin-call"
	// ActionTypeAgentNote writes a structured note via the injected
	// NoteWriter. Permitted at W1.
	ActionTypeAgentNote ActionType = "agent-note"
	// ActionTypeShell is RECOGNISED but permanently refused at W1 — shell
	// actions land only via I/S-18.T5's policy-routed risk ladder. It is
	// declared here (rather than left as just another unknown string) so
	// registry.go and dispatcher.go can name it explicitly in refusal
	// messages instead of reporting it identically to a typo.
	ActionTypeShell ActionType = "shell"
)

// permittedActionTypes is the W1 registrable set. Anything not in this
// set — including ActionTypeShell — is refused by Registry.Register.
var permittedActionTypes = map[ActionType]bool{
	ActionTypePluginCall: true,
	ActionTypeAgentNote:  true,
}

// HookConfig is one hook definition, matching config.toml's [hooks]
// section row (08-INIT-CONFIG-SPEC.md §3: id, trigger, action_type,
// action_params; reload class hot per R-14.9). Decoding config.toml into
// this shape is composition-root work outside this ticket's scope — this
// package only defines and validates the shape.
type HookConfig struct {
	// ID is the hook's stable identifier. If empty at Register time, it
	// is derived deterministically via DeriveHookID so hook identity
	// survives a daemon restart without requiring the config author to
	// hand-assign one.
	ID string `toml:"id"`
	// Trigger is the event-kind string this hook fires on. Matching is
	// exact-string against internal/events.Event.Kind (dispatcher.go);
	// no wildcard or pattern syntax is defined at W1.
	Trigger string `toml:"trigger"`
	// ActionType selects plugin-call or agent-note (or, for hooks a
	// misconfigured file names, shell or anything else — refused, never
	// silently coerced).
	ActionType ActionType `toml:"action_type"`
	// ActionParams is the action's opaque parameter bag. Interpreting
	// reserved keys (e.g. which plugin, which tool) is the injected
	// PluginDispatcher/NoteWriter implementation's responsibility, not
	// this package's — this package only hashes it for the audit trail
	// (paramsHash) and never inspects individual keys itself.
	ActionParams map[string]string `toml:"action_params"`
}

// ResultCode is the outcome recorded on a HookFire audit event.
type ResultCode string

const (
	// ResultSuccess reports that the inner PluginDispatcher/NoteWriter call
	// returned nil.
	ResultSuccess ResultCode = "success"
	// ResultError reports that the inner call returned a non-nil error.
	ResultError ResultCode = "error"
	// ResultPanic reports that the inner call panicked; recovered by
	// dispatcher.go's deferred recover.
	ResultPanic ResultCode = "panic"
	// ResultTimeout reports that the inner call did not complete within the
	// dispatcher's configured action timeout. The underlying goroutine is
	// abandoned, not killed (Go has no mechanism to force that) — see
	// dispatcher.go.
	ResultTimeout ResultCode = "timeout"
	// ResultRefused reports that the action type was shell or unrecognised
	// and was refused at dispatch time (defense-in-depth — Registry.Register
	// should already have refused it, so reaching this path means some
	// other path stored a HookConfig without going through Register).
	ResultRefused ResultCode = "refused"
)

// HookFire is the audit record published to the event bus for every fire
// attempt — success, error, panic, timeout, or refusal — never omitted
// (audit.go's deferred emit covers every one of these paths, including
// panic). ErrMsg is redacted before publication if it contains the
// literal value of any of the hook's own action_params that looks
// secret-shaped (audit.go, redactSecrets) — Payload never carries the raw
// ActionParams themselves, only ParamsHash, so the hash is the only
// params-derived field that reaches the bus unconditionally.
type HookFire struct {
	HookID     string     `json:"hook_id"`
	Trigger    string     `json:"trigger"`
	ActionType ActionType `json:"action_type"`
	ParamsHash string     `json:"params_hash"`
	ResultCode ResultCode `json:"result_code"`
	ErrMsg     string     `json:"err_msg,omitempty"`
	Ts         time.Time  `json:"ts"`
}

// PluginDispatcher is the injected seam a plugin-call action dispatches
// through. The composition root wires the concrete implementation to
// C/S-05.T7's plugin registry; this package depends on the interface only
// so it never imports internal/plugins (Art.10.2). Implementations decide
// how to interpret params (e.g. which key names the plugin id / tool) —
// this package passes action_params through verbatim.
//
// Idempotency: because internal/events delivers AT-LEAST-ONCE, a given
// hook fire may invoke DispatchPluginCall twice for the same logical
// event. This package performs no de-duplication. An implementation whose
// plugin call is not naturally idempotent (e.g. "increment a counter"
// rather than "set a value") must implement its own idempotency guard
// (e.g. keyed on hookID + the event's Seq, which dispatcher.go does not
// currently thread through — a forward note for whichever ticket wires
// the concrete implementation).
type PluginDispatcher interface {
	// DispatchPluginCall invokes the plugin action a hook configures.
	// hookID identifies the firing hook (for the implementation's own
	// logging/idempotency use); params is the hook's action_params
	// verbatim.
	DispatchPluginCall(ctx context.Context, hookID string, params map[string]string) error
}

// NoteWriter is the injected seam an agent-note action dispatches
// through. The composition root wires the concrete implementation to the
// journal/memory domain once G/S-13 ships — NO production implementation
// exists yet anywhere in this tree (Art.1: stated plainly, not left
// implicit). The same at-least-once/idempotency note on PluginDispatcher
// applies here identically.
type NoteWriter interface {
	// WriteAgentNote writes a structured note. hookID identifies the
	// firing hook; params is the hook's action_params verbatim.
	WriteAgentNote(ctx context.Context, hookID string, params map[string]string) error
}

// HookActionNotPermittedCode is the stable, greppable identifier this
// package attaches to every action-type refusal (message text and
// HookFire.ResultCode=ResultRefused paths both reference it).
//
// CONTRACT DEVIATION (documented per this ticket's brief): the contract's
// task list asks for "error kind HOOK_ACTION_NOT_PERMITTED from the A-T7
// taxonomy". A-T7 (pkg/cascade, A/S-01.T7) froze a CLOSED 14-member Kind
// enumeration (pkg/cascade/kinds.go, T0 ruling R-14.3) with no member of
// that name, and R-14.3 amendments are a T0 decision, not a build-time
// one — this ticket cannot add a 15th Kind. The refusal is instead raised
// as cascade.KindPolicyDenied ("policy evaluation refused the
// operation"), the closest existing member and an exact semantic match
// for W1's shell-deferral security ruling, WITH this string carried as
// additional stable data in the error message and in every HookFire
// audit record's ResultCode/ErrMsg — so a caller, test, or audit reader
// can still identify this specific refusal class by grepping for it,
// without requiring a taxonomy amendment. This mirrors this package's own
// ResultCode being a local open string type, not a pkg/cascade Kind.
const HookActionNotPermittedCode = "HOOK_ACTION_NOT_PERMITTED"

// newActionNotPermittedError returns the refusal error for a shell or
// unrecognised action type, used identically by Registry.Register and by
// dispatcher.go's defense-in-depth check.
func newActionNotPermittedError(actionType ActionType) error {
	return cascade.Newf(
		cascade.KindPolicyDenied,
		"hooks: action type %q not permitted (%s)", actionType, HookActionNotPermittedCode,
	)
}

// paramsHash returns a deterministic, order-independent hex-encoded
// SHA-256 digest of params — the ONLY params-derived value that ever
// reaches a HookFire audit record (never the raw params themselves).
// Deterministic across map-iteration-order runs (Art.11) because keys are
// sorted before hashing.
func paramsHash(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(params[k]))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DeriveHookID returns the deterministic slug a hook's ID defaults to
// when its config omits one, so identity is stable across daemon
// restarts for two config files that declare the same
// trigger+action_type+params (full_desc task 1's requirement).
func DeriveHookID(trigger string, actionType ActionType, params map[string]string) string {
	hash := paramsHash(params)
	return fmt.Sprintf("%s-%s-%s", slugify(trigger), slugify(string(actionType)), hash[:12])
}

// slugify lower-cases s and replaces every run of characters outside
// [a-z0-9] with a single '-', trimming leading/trailing '-'. It never
// panics or returns the empty string for a non-empty input (a fully
// non-alphanumeric input becomes "-", not "").
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "-"
	}
	return out
}
