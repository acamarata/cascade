//go:build !windows

// Purpose: recovery from a second process leaves a live recorder's pending
// call untouched (its lock is held), the recorder's own later confirm is the
// only terminal record, and keys other effect sites own are never touched.
// SPORT: internal/plugins plugin effect recovery tests (P1-PLG-03).
package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestRecoveryIgnoresLiveRecorder(t *testing.T) {
	if spec := os.Getenv(effectChildEnv); spec != "" {
		_, marked := childFix(t, spec) // a second process opening the effect log
		code := map[string]int{"0": 20, "1": 21}[marked]
		if code == 0 {
			code = 29
		}
		os.Exit(code)
	}
	f := newLaunchFix(t)
	h := liveGitHub(t, f)
	ctx := context.Background()
	intent, err := h.beginCall(ctx, helloMethod, nil)
	if err != nil {
		t.Fatalf("beginCall: %v", err)
	}
	if code := runCrashChild(t, f, "TestRecoveryIgnoresLiveRecorder", "recover"); code != 20 {
		t.Fatalf("second-process recovery exit = %d, want 20 (marked none)", code)
	}
	h.effects = f.effects // the reopened store's effect log
	if err := h.endCall(ctx, intent, nil); err != nil {
		t.Fatalf("the live recorder's confirm: %v", err)
	}
	requireOnePair(t, f, intent.Key, "confirmed")
}

func TestRecoveryLeavesOtherSitesAndBadKeys(t *testing.T) {
	f := newLaunchFix(t)
	ctx := context.Background()
	for _, key := range []string{"approval.grant:x-1", "plugin.call:demo:not-hex:1", "plugin.launch:demo"} {
		if _, err := f.effects.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect, Actor: "t", Action: "a", Key: key}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := RecoverPluginEffects(ctx, f.effects, f.lockDir); err != nil || n != 0 {
		t.Fatalf("recovery over foreign and malformed keys marked %d (%v), want 0", n, err)
	}
	// A dead recorder's leftovers (an unlocked lock file, an owner file whose
	// launch has nothing pending) are swept; a live recorder's are kept.
	dead, launch := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for name, body := range map[string]string{dead + ".lock": "", launch + ".launch": dead} {
		if err := os.WriteFile(filepath.Join(f.lockDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := liveGitHub(t, f)
	if _, err := RecoverPluginEffects(ctx, f.effects, f.lockDir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{dead + ".lock": false, launch + ".launch": false,
		h.token + ".lock": true, h.launchID + ".launch": true} {
		if _, err := os.Stat(filepath.Join(f.lockDir, name)); (err == nil) != want {
			t.Fatalf("%s present = %v after the sweep, want %v", name, err == nil, want)
		}
	}
	if _, err := RecoverPluginEffects(ctx, nil, f.lockDir); err == nil {
		t.Fatal("recovery without an effect log succeeded")
	}
}

// failingEffects is the real effect writer with injected failures.
type failingEffects struct {
	audit.EffectWriter
	begin, confirm bool
}

var errInjectedEffect = cascade.New(cascade.KindUnavailable, "injected effect-log failure")

// refusedByInjection reports err is the refusal of an intent the injected
// failure blocked (errors.Is compares cascade Kinds only, so match text).
func refusedByInjection(err error) bool {
	return err != nil && strings.Contains(err.Error(), "refused: its intent was not recorded") &&
		strings.Contains(err.Error(), errInjectedEffect.Error())
}

func (w failingEffects) BeginEffect(ctx context.Context, r audit.EffectRequest) (audit.EffectHandle, error) {
	if w.begin {
		return audit.EffectHandle{}, errInjectedEffect
	}
	return w.EffectWriter.BeginEffect(ctx, r)
}

func (w failingEffects) ConfirmEffect(ctx context.Context, h audit.EffectHandle, o string) (audit.Record, error) {
	if w.confirm {
		return audit.Record{}, errInjectedEffect
	}
	return w.EffectWriter.ConfirmEffect(ctx, h, o)
}

// TestEffectWriterFailuresRefuse: a refused launch intent starts nothing, a
// refused call intent never reaches the child, an unrecordable launch
// confirm stops the child, and a closed handle refuses further calls.
func TestEffectWriterFailuresRefuse(t *testing.T) {
	f := newLaunchFix(t)
	h := liveGitHub(t, f)
	ctx := context.Background()
	before := len(f.allRecords(t))
	h.effects = failingEffects{EffectWriter: f.effects, begin: true}
	if _, err := h.Call(ctx, helloMethod, nil); !refusedByInjection(err) {
		t.Fatalf("Call with a failing BeginEffect = %v, want the refusal", err)
	}
	if after := len(f.allRecords(t)); after != before {
		t.Fatalf("a refused call intent wrote %d records", after-before)
	}
	h.effects = f.effects
	_ = h.Close(ctx)
	beforeClosedCall := len(f.allRecords(t))
	if _, err := h.Call(ctx, helloMethod, nil); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Call after Close = %v, want KindUnavailable", err)
	}
	if after := len(f.allRecords(t)); after != beforeClosedCall {
		t.Fatalf("a Call after Close wrote %d records, want none", after-beforeClosedCall)
	}
	var closed int
	realClose := closeProcess
	closeProcess = func(ctx context.Context, p *process.Handle) error { closed++; return realClose(ctx, p) }
	defer func() { closeProcess = realClose }()
	for i, w := range []failingEffects{{EffectWriter: f.effects, begin: true}, {EffectWriter: f.effects, confirm: true}} {
		restore := SetLaunchEnvSource(func(ctx context.Context) (LaunchEnv, error) {
			env, err := f.source(ctx)
			env.Effects = w
			return env, err
		})
		got, err := LaunchInstalled(ctx, cascadeGitHubPluginID)
		restore()
		if got != nil || !strings.Contains(fmt.Sprint(err), errInjectedEffect.Error()) || len(launchedHandles()) != 0 {
			t.Fatalf("launch with %+v = %v, %v (%d live); want a refusal and no live child", w, got, err, len(launchedHandles()))
		}
		if closed != i { // a refused intent starts nothing; an unrecordable confirm stops the started child
			t.Fatalf("leg %d: the child was closed %d times, want %d", i, closed, i)
		}
	}
}

// closeStore closes a fixture store so every later read of it errs.
func closeStore(t *testing.T, s provider.Store) {
	t.Helper()
	c, ok := s.(interface{ Close() error })
	if !ok {
		t.Fatalf("%T has no Close", s)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

// withRecorderState runs fn with the process recorder pointed at dir, no
// token or held lock for it, and restores the previous state afterwards (the
// recorder is process-global; the real fixture arms it per test).
func withRecorderState(t *testing.T, dir string, fn func()) {
	t.Helper()
	recorder.mu.Lock()
	saved := struct {
		token, dir string
		held       map[string]*os.File
	}{recorder.token, recorder.dir, recorder.held}
	recorder.dir, recorder.held = dir, nil
	recorder.mu.Unlock()
	defer func() {
		recorder.mu.Lock()
		for _, f := range recorder.held {
			_ = f.Close()
		}
		recorder.token, recorder.dir, recorder.held = saved.token, saved.dir, saved.held
		recorder.mu.Unlock()
	}()
	fn()
}
