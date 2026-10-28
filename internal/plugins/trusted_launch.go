// Package plugins (trusted_launch.go): Purpose: the one place a process
// plugin is launched for real: LaunchInstalled starts a plugin whose stored
// record is enabled and trusted and whose executable still matches the
// pinned digest, and every launch and call writes an intent record before
// and a terminal record after.
// Inputs: a plugin id; the LaunchEnv (launch_env.go); the record a witnessed
// plugin.add or plugin.update wrote (trust_grant.go).
// Outputs: a *TrustedHandle whose Call and Close reach the live child.
// Constraints: process.TrustTierTrusted is assigned and
// process.ProcessRuntime.Launch is called nowhere else (arch_launch_test.go).
// ResolveExecutable re-runs at every launch and the digested path is exactly
// the command run. The child gets childEnv only. Residual: a same-uid swap
// of the sibling between the hash and the exec (ADR-0006 section 3; not a
// boundary P1 claims); the process runtime's crash respawn re-execs the same
// path without hashing it again, which widens that same-uid window to the
// handle's life (per-call callers close the handle after one call).
// SPORT: internal/plugins trusted process launch (P1-PLG-03).
package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// TrustedHandle is one live, recorded launch of a trusted process plugin.
type TrustedHandle struct {
	id, launchID, token, dir string
	effects                  audit.EffectWriter
	rt                       *process.ProcessRuntime
	h                        *process.Handle
	seq                      atomic.Uint64
	pending                  atomic.Int64
	closed                   atomic.Bool
}

// HandleProvider resolves a live TrustedHandle for a process plugin.
type HandleProvider func(ctx context.Context, pluginID string) (*TrustedHandle, error)

// InstalledHandleProvider is the production HandleProvider: LaunchInstalled.
func InstalledHandleProvider() HandleProvider { return LaunchInstalled }

// LaunchInstalled launches id's installed process plugin. A missing record
// is KindNotFound; a disabled, untrusted or non-process record, a refused
// executable and a digest mismatch are KindPolicyDenied; no launch
// environment or recorder is KindUnavailable. Nothing starts on a refusal.
func LaunchInstalled(ctx context.Context, id string) (*TrustedHandle, error) {
	env, err := ResolveLaunchEnv(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := trustedRecord(ctx, env, id)
	if err != nil {
		return nil, err
	}
	return launchPinned(ctx, env, rec, rec.ExecDigest)
}

// trustedRecord loads id's record and refuses unless it is an enabled,
// trusted process record with a pinned digest.
func trustedRecord(ctx context.Context, env LaunchEnv, id string) (PluginMetadata, error) {
	rec, ok, err := LoadMetadata(ctx, env.Store, id)
	if err != nil {
		return PluginMetadata{}, err
	}
	if !ok {
		return PluginMetadata{}, cascade.Newf(cascade.KindNotFound,
			"plugins: %s is not installed; run `cascade plugin add %s`", id, id)
	}
	if rec.Name != id || !rec.Enabled || rec.TrustTier != TrustTrusted || rec.RuntimeMode != plugin.RuntimeProcess || rec.ExecDigest == "" {
		return PluginMetadata{}, cascade.Newf(cascade.KindPolicyDenied,
			"plugins: %s is not an enabled, trusted process plugin; run `cascade plugin add %s` "+
				"(an owner-attested add) before it can start", id, id)
	}
	return rec, nil
}

// launchPlan is a launch whose executable passed every check and whose
// intent is recorded; start runs it.
type launchPlan struct {
	env                      LaunchEnv
	rec                      PluginMetadata
	path, launchID, token, d string
	intent                   audit.EffectHandle
}

// launchPinned launches rec's executable when its digest is want.
func launchPinned(ctx context.Context, env LaunchEnv, rec PluginMetadata, want string) (*TrustedHandle, error) {
	plan, err := beginLaunch(ctx, env, rec, want)
	if err != nil {
		return nil, err
	}
	return plan.start(ctx)
}

// beginLaunch resolves and hashes the executable, refuses a mismatch, then
// records the launch owner and begins the plugin.launch intent.
func beginLaunch(ctx context.Context, env LaunchEnv, rec PluginMetadata, want string) (*launchPlan, error) {
	path, err := ResolveExecutable(rec.Name)
	if err == nil {
		var digest string
		if digest, err = ExecutableDigest(path); err == nil && digest != want {
			err = cascade.Newf(cascade.KindPolicyDenied, "plugins: %s: executable changed since it was trusted; "+
				"run `cascade plugin update %s --reverify`", rec.Name, rec.Name)
		}
	}
	if err != nil {
		if cascade.HasKind(err, cascade.KindPolicyDenied) {
			return nil, err
		}
		return nil, cascade.Wrapf(cascade.KindPolicyDenied, err, "plugins: %s: refusing to launch", rec.Name)
	}
	token, dir, err := armedRecorder()
	if err != nil {
		return nil, err
	}
	launchID, err := mintHex()
	if err == nil {
		err = recordLaunchOwner(dir, launchID, token)
	}
	if err != nil {
		return nil, err
	}
	intent, err := beginEffect(ctx, env.Effects, rec.Name, "plugin.launch",
		launchEffectPrefix+rec.Name+":"+launchID, want, token)
	if err != nil {
		removeLaunchOwner(dir, launchID)
		return nil, err
	}
	return &launchPlan{env: env, rec: rec, path: path, launchID: launchID, token: token, d: dir, intent: intent}, nil
}

// start runs the plan's executable and records the launch's outcome.
func (p *launchPlan) start(ctx context.Context) (*TrustedHandle, error) {
	rt := process.NewProcessRuntime()
	rt.Stderr, rt.Registrar, rt.Clock = p.env.Stderr, p.env.Registrar, p.env.Clock
	configureRuntime(ctx, p.env, p.rec.Name, EffectiveGrants(p.rec), rt)
	h, err := rt.Launch(ctx, process.Manifest{
		Name: p.rec.Name, TrustTier: process.TrustTierTrusted, Command: p.path, Args: nil,
		Env: childEnv(p.rec, p.env.Getenv), NetScopes: netScopes(p.rec),
	})
	terminalCtx := context.WithoutCancel(ctx)
	if err != nil {
		if r, ferr := p.env.Effects.FailEffect(terminalCtx, p.intent, kindName(err)); recorded(r, ferr) {
			removeLaunchOwner(p.d, p.launchID)
		}
		return nil, err
	}
	if r, cerr := p.env.Effects.ConfirmEffect(terminalCtx, p.intent, "launched"); !recorded(r, cerr) {
		closeCtx, cancel := context.WithTimeout(terminalCtx, closeGrace)
		defer cancel()
		_ = closeProcess(closeCtx, h)
		return nil, cascade.Wrapf(cascade.KindUnavailable, cerr, "plugins: %s: the launch could not be recorded; stopped it", p.rec.Name)
	}
	t := &TrustedHandle{id: p.rec.Name, launchID: p.launchID, token: p.token, dir: p.d, effects: p.env.Effects, rt: rt, h: h}
	trackLaunched(t)
	return t, nil
}

// configureRuntime sets the per-launch runtime fields a platform supports;
// host_boundary_hook.go (unix) installs the capability checker through it.
var configureRuntime = func(context.Context, LaunchEnv, string, []string, *process.ProcessRuntime) {}

// closeProcess closes a live child (process.Handle.Close; the windows build
// has no live child to close).
var closeProcess = func(ctx context.Context, h *process.Handle) error {
	if c, ok := any(h).(interface{ Close(context.Context) error }); ok {
		return c.Close(ctx)
	}
	return nil
}

// beginEffect begins one plugin effect. An intent whose notification failed
// is failed at once, and the effect is refused either way.
func beginEffect(ctx context.Context, w audit.EffectWriter, id, action, key, paramsHash, token string) (audit.EffectHandle, error) {
	explain, _ := json.Marshal(map[string]string{"recorder": token})
	h, err := w.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect, Actor: "plugin:" + id,
		Action: action, Key: key, ParamsHash: paramsHash, Explain: explain})
	if err != nil {
		if h.IntentSeq != 0 {
			_, _ = w.FailEffect(context.WithoutCancel(ctx), h, "intent notification failed; effect not run")
		}
		return audit.EffectHandle{}, cascade.Wrapf(cascade.KindUnavailable, err, "plugins: %s: %s refused: its intent was not recorded", id, action)
	}
	return h, nil
}

// kindName names err's kind for a failed record.
func kindName(err error) string {
	if k, ok := cascade.KindOf(err); ok {
		return k.String()
	}
	return "error"
}

// Call records an intent, calls method on the live child, and records the
// outcome: confirmed, failed on a typed plugin error reply, or
// unknown-outcome on a timeout or transport loss. A refused intent refuses
// the call; nothing reaches the child.
func (t *TrustedHandle) Call(ctx context.Context, method string, params []byte) ([]byte, error) {
	if t == nil || t.h == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "plugins: Call on a TrustedHandle that was never launched")
	}
	intent, err := t.beginCall(ctx, method, params)
	if err != nil {
		return nil, err
	}
	out, callErr := t.h.Call(ctx, method, params)
	return out, t.endCall(ctx, intent, callErr)
}

// beginCall records one call's intent under the next sequence number.
func (t *TrustedHandle) beginCall(ctx context.Context, method string, params []byte) (audit.EffectHandle, error) {
	digest, overridden := ParamsDigestFrom(ctx)
	if !overridden {
		sum := sha256.Sum256(params)
		digest = hex.EncodeToString(sum[:])
	} else if b, err := hex.DecodeString(digest); err != nil || len(b) != sha256.Size {
		return audit.EffectHandle{}, cascade.Newf(cascade.KindInvalidInput,
			"plugins: %s: the params digest override for %s is not a sha256 hex digest; call refused", t.id, method)
	}
	key := callEffectPrefix + t.id + ":" + t.launchID + ":" + strconv.FormatUint(t.seq.Add(1), 10)
	t.pending.Add(1) // before the intent exists, so Close never drops its owner file
	if t.closed.Load() {
		t.pending.Add(-1)
		return audit.EffectHandle{}, cascade.Newf(cascade.KindUnavailable, "plugins: %s: Call after Close", t.id)
	}
	h, err := beginEffect(ctx, t.effects, t.id, "plugin.call", key, digest, t.token)
	if err != nil {
		t.pending.Add(-1)
	}
	return h, err
}

// endCall writes the call's terminal record. A record another process
// already settled (ErrEffectNotPending) counts as done.
func (t *TrustedHandle) endCall(ctx context.Context, intent audit.EffectHandle, callErr error) error {
	defer func() {
		if t.pending.Add(-1) == 0 && t.closed.Load() {
			removeLaunchOwner(t.dir, t.launchID)
		}
	}()
	wctx := context.WithoutCancel(ctx)
	var werr error
	var rec audit.Record
	switch {
	case callErr == nil:
		rec, werr = t.effects.ConfirmEffect(wctx, intent, "ok")
	case outcomeUnknown(callErr):
		if m, ok := t.effects.(interface {
			MarkUnknownOutcome(context.Context, audit.EffectHandle) (audit.Record, error)
		}); ok {
			rec, werr = m.MarkUnknownOutcome(wctx, intent)
		} // otherwise the intent stays pending for RecoverPluginEffects
	default:
		rec, werr = t.effects.FailEffect(wctx, intent, kindName(callErr))
	}
	if callErr != nil || recorded(rec, werr) {
		return callErr // a failed call already errs; its intent stays for recovery if unsealed
	}
	return cascade.Wrapf(cascade.KindUnavailable, werr, "plugins: %s: the call ran but its outcome was not recorded", t.id)
}

// outcomeUnknown reports whether a call error leaves it unknown whether the
// plugin acted: a timeout or a lost transport, never a typed error reply.
func outcomeUnknown(err error) bool {
	return cascade.HasKind(err, cascade.KindTimeout) || cascade.HasKind(err, cascade.KindUnavailable) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// Close stops the child (process.Handle.Close). Once no call of this launch
// is pending, the launch's owner file is removed.
func (t *TrustedHandle) Close(ctx context.Context) error {
	if t == nil || t.h == nil {
		return nil
	}
	t.closed.Store(true)
	err := closeProcess(ctx, t.h)
	untrackLaunched(t)
	if t.pending.Load() == 0 {
		removeLaunchOwner(t.dir, t.launchID)
	}
	return err
}
