//go:build !windows

// Purpose: the hook runner table: one registration per action type, the
// plugin-call runner with its delivery effect record, the agent-note runner
// with its secret refusal, and the start-up recovery of hook effects.
// Inputs: registerHookRunner calls from package-level initialisers; the
// daemonWiring a daemon build hands hookRunnersFor.
// Outputs: the hooks.ActionRunner map a Dispatcher's Runners field takes,
// hookRunnable for the section parser, recoverHookEffects for start-up.
// Constraints: shell is never registered here. Every runner ctx carries
// rpc.OriginTool, so no witness a Run ctx held is readable below it. A
// plugin is reached only through plugins.LaunchInstalled and its
// TrustedHandle. The audit row's params hash is the delivery digest, never a
// hash of rehydrated params. One delivery is one effect key, so a redelivered
// event, or one after a crash, never repeats the call.
// SPORT: cmd/cascade hook runners (P1-SUP-02).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// hookEffectPrefix opens every hook effect key and the note provenance.
const hookEffectPrefix = "hook:"

// The action_params keys beyond the ones internal/hooks names. Each is
// operator config; none carries event payload data.
const (
	hookInputParam       = "input"
	hookNoteBodyParam    = "body"
	hookNoteKindParam    = "kind"
	hookNoteSummaryParam = "description"
	hookNoteScopeParam   = "scope"
)

// errHookDuplicateDelivery is a delivery whose effect record already exists:
// the plugin call was not repeated.
var errHookDuplicateDelivery = cascade.New(cascade.KindConflict,
	"hooks: this delivery already has an effect record; the plugin call was not repeated")

// errHookNoteSecret refuses an agent note holding secret material. It never
// names the value or where it sat.
var errHookNoteSecret = cascade.New(cascade.KindPolicyDenied,
	"hooks: the agent note holds secret material; nothing was written")

// hookRunnerRegistration builds the runner of one action type.
type hookRunnerRegistration struct {
	Type  hooks.ActionType
	Build func(w *daemonWiring) (hooks.ActionRunner, error)
}

// hookRunnerTable holds every registration by action type.
var hookRunnerTable = map[hooks.ActionType]hookRunnerRegistration{}

// registerHookRunner records r. It is used only as
// `var _ = registerHookRunner(...)`; a duplicate Type, ActionTypeShell or a
// nil Build panics at init.
func registerHookRunner(r hookRunnerRegistration) struct{} {
	if r.Type == hooks.ActionTypeShell {
		panic("hooks: shell never has a registered runner")
	}
	if r.Build == nil {
		panic("hooks: nil Build for action type " + string(r.Type))
	}
	if _, dup := hookRunnerTable[r.Type]; dup {
		panic("hooks: duplicate runner for action type " + string(r.Type))
	}
	hookRunnerTable[r.Type] = r
	return struct{}{}
}

var (
	_ = registerHookRunner(hookRunnerRegistration{Type: hooks.ActionTypePluginCall, Build: buildPluginCallRunner})
	_ = registerHookRunner(hookRunnerRegistration{Type: hooks.ActionTypeAgentNote, Build: buildAgentNoteRunner})
)

// hookRunnable reports whether t has a registered runner.
func hookRunnable(t hooks.ActionType) bool {
	_, ok := hookRunnerTable[t]
	return ok
}

// hookRunnersFor builds every registered runner, each under the tool origin.
func hookRunnersFor(w *daemonWiring) (map[hooks.ActionType]hooks.ActionRunner, error) {
	runners := make(map[hooks.ActionType]hooks.ActionRunner, len(hookRunnerTable))
	for t, r := range hookRunnerTable {
		runner, err := r.Build(w)
		if err != nil {
			return nil, err
		}
		runners[t] = toolOriginRunner{inner: runner}
	}
	return runners, nil
}

// toolOriginRunner stamps OriginTool on the ctx of the runner it wraps.
type toolOriginRunner struct{ inner hooks.ActionRunner }

// RunAction implements hooks.ActionRunner.
func (r toolOriginRunner) RunAction(ctx context.Context, fire hooks.Fire, params map[string]string) error {
	return r.inner.RunAction(rpc.WithOrigin(ctx, rpc.OriginTool), fire, params)
}

// buildPluginCallRunner runs plugin calls over the daemon's shared effect log.
func buildPluginCallRunner(w *daemonWiring) (hooks.ActionRunner, error) {
	effects, err := openEffectLog(w.Store, w.Clock, nil, w.Paths)
	if err != nil {
		return nil, err
	}
	return hookPluginRunner{effects: effects, launch: plugins.InstalledHandleProvider()}, nil
}

// hookDeliveryDigest is the sha256 hex of one delivery's identity.
func hookDeliveryDigest(fire hooks.Fire) string {
	sum := sha256.Sum256([]byte(fire.Hook.ID + "\x00" + fire.Namespace + "\x00" +
		strconv.FormatUint(fire.EventSeq, 10)))
	return hex.EncodeToString(sum[:])
}

// hookEffectKey is the idempotency key of one delivery.
func hookEffectKey(fire hooks.Fire) string {
	return hookEffectPrefix + hookDeliveryDigest(fire)[:32]
}

// hookPluginRunner is the plugin-call runner.
type hookPluginRunner struct {
	effects audit.EffectWriter
	launch  plugins.HandleProvider
}

// RunAction begins the delivery's effect, calls the plugin once and writes
// the terminal record. A call whose outcome is unknown keeps its intent, and
// recoverHookEffects marks it.
func (r hookPluginRunner) RunAction(ctx context.Context, fire hooks.Fire, params map[string]string) error {
	id, tool := params[hooks.PluginParamName], params[hooks.PluginToolParam]
	input := []byte(params[hookInputParam])
	if len(input) == 0 {
		input = []byte("{}")
	}
	if id == "" || tool == "" || !json.Valid(input) {
		return cascade.Newf(cascade.KindInvalidInput, "hooks: hook %q: plugin-call needs plugin, tool and JSON input", fire.Hook.ID)
	}
	digest := hookDeliveryDigest(fire)
	h, err := r.begin(ctx, fire, digest)
	if err != nil {
		return err
	}
	wctx := context.WithoutCancel(ctx)
	th, err := r.launch(ctx, id)
	if err != nil {
		return r.fail(wctx, h, err)
	}
	_, callErr := th.Call(plugins.WithParamsDigest(ctx, digest), tool, input)
	closeErr := th.Close(wctx)
	switch {
	case callErr == nil:
		if _, err := r.effects.ConfirmEffect(wctx, h, "ok"); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "hooks: hook %q: the call ran but its outcome was not recorded", fire.Hook.ID)
		}
		return closeErr
	case hookOutcomeUnknown(callErr):
		return callErr
	default:
		return r.fail(wctx, h, callErr)
	}
}

// begin records the delivery's intent. An existing key is a duplicate; an
// intent whose notification failed is failed at once.
func (r hookPluginRunner) begin(ctx context.Context, fire hooks.Fire, digest string) (audit.EffectHandle, error) {
	h, err := r.effects.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect,
		Actor: hookEffectPrefix + fire.Hook.ID, Action: "hook.plugin-call", Key: hookEffectKey(fire), ParamsHash: digest})
	switch {
	case err == nil:
		return h, nil
	case errors.Is(err, audit.ErrEffectExists):
		return audit.EffectHandle{}, errHookDuplicateDelivery
	}
	if h.IntentSeq != 0 {
		_, _ = r.effects.FailEffect(context.WithoutCancel(ctx), h, "intent notification failed; effect not run")
	}
	return audit.EffectHandle{}, cascade.Wrapf(cascade.KindUnavailable, err, "hooks: hook %q: intent not recorded; call refused", fire.Hook.ID)
}

// fail records that the effect did not happen and returns cause. The reason
// is the kind alone, so no param value reaches the record.
func (r hookPluginRunner) fail(ctx context.Context, h audit.EffectHandle, cause error) error {
	reason := "error"
	if k, ok := cascade.KindOf(cause); ok {
		reason = k.String()
	}
	if _, err := r.effects.FailEffect(ctx, h, reason); err != nil {
		// A raw errors.Join would mint a kindless error at this boundary:
		// keep cause's kind (internal when it has none) and chain, and name
		// the record failure in the message.
		kind, ok := cascade.KindOf(cause)
		if !ok {
			kind = cascade.KindInternal
		}
		return cascade.Wrapf(kind, cause, "%v; failure not recorded: %v", cause, err)
	}
	return cause
}

// hookOutcomeUnknown reports whether err leaves it unknown whether the
// plugin acted: a timeout or a lost transport, never a typed error reply.
func hookOutcomeUnknown(err error) bool {
	return cascade.HasKind(err, cascade.KindTimeout) || cascade.HasKind(err, cascade.KindUnavailable) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// recoverHookEffects marks every open hook effect unknown-outcome: a crash
// left it unclear whether the call ran, so it is never retried. Intents of
// other sites are left for their own recovery. It returns how many it marked.
func recoverHookEffects(ctx context.Context, log *audit.EffectLog) (int, error) {
	if log == nil {
		return 0, cascade.New(cascade.KindInvalidInput, "hooks: effect recovery needs an effect log")
	}
	pending, err := log.PendingEffects(ctx)
	if err != nil {
		return 0, err
	}
	marked := 0
	for _, h := range pending {
		if !strings.HasPrefix(h.Key, hookEffectPrefix) {
			continue
		}
		if _, err := log.MarkUnknownOutcome(ctx, h); err != nil {
			if errors.Is(err, audit.ErrEffectNotPending) {
				continue
			}
			return marked, err
		}
		marked++
	}
	return marked, nil
}

// buildAgentNoteRunner writes notes to the daemon's memory tree.
func buildAgentNoteRunner(w *daemonWiring) (hooks.ActionRunner, error) {
	if w.Paths == nil || w.Clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "hooks: the agent-note runner needs paths and a clock")
	}
	return hooks.AgentNoteRunner(hookNoteWriter{store: memory.NewFileStore(memoryStoreDir(w.Paths), w.Clock)}), nil
}

// hookNoteWriter is the agent-note hooks.NoteWriter over the memory store.
type hookNoteWriter struct{ store *memory.FileStore }

// WriteAgentNote refuses a note with any secret-shaped param, then writes it
// with hook provenance. FileStore.Write is idempotent, so a redelivery of the
// same note changes nothing.
func (n hookNoteWriter) WriteAgentNote(ctx context.Context, hookID string, params map[string]string) error {
	detector, err := secrets.ProcessDetector()
	if err != nil {
		return err
	}
	for _, v := range params {
		if len(detector.Scan([]byte(v))) > 0 {
			return errHookNoteSecret
		}
	}
	kind := memory.KindProject
	if raw := params[hookNoteKindParam]; raw != "" {
		if kind, err = memory.ParseKind(raw); err != nil {
			return err
		}
	}
	scope := params[hookNoteScopeParam]
	if scope == "" {
		scope = hookEffectPrefix + hookID
	}
	return n.store.Write(ctx, memory.MemoryEntry{Name: params[hooks.NoteNameParam], Kind: kind,
		Description: params[hookNoteSummaryParam], Body: params[hookNoteBodyParam], ScopeRef: scope,
		Provenance: memory.Provenance{Origin: memory.OriginHarness, SessionID: hookEffectPrefix + hookID}})
}
