// Purpose: `vault get --clipboard` over a fake clipboard writer and a
//
//	hermetic file vault: no value on stdout or stderr, the CLI returns only
//	after the clear, a cancel (the SIGINT path) clears at once, a bad TTL
//	refuses before the proof, and the approval binds to the call's own key.
//	No test touches the real clipboard, keychain or HOME.
//
// SPORT: cmd/cascade/vault get --clipboard (TEST, P1-SEC-09).
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

const clipSecret = "clip-s3cret-value-0123456789"

// fakeClipWriter stands in for the clipboard backend. With hold it blocks
// until ctx ends, as the real writer does until its TTL clear. With release
// it closes entered and blocks until the test closes release, so a command
// that returns while the writer still holds the value is caught.
type fakeClipWriter struct {
	hold    chan struct{}
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	payload []byte
	signed  []byte
	cleared bool
}

func (f *fakeClipWriter) Write(ctx context.Context, signed, payload []byte) error {
	f.mu.Lock()
	f.payload, f.signed = bytes.Clone(payload), bytes.Clone(signed)
	f.mu.Unlock()
	if f.hold != nil {
		close(f.hold)
		<-ctx.Done()
	}
	if f.release != nil {
		close(f.entered)
		<-f.release
	}
	f.mu.Lock()
	f.cleared = true
	f.mu.Unlock()
	return nil
}

func (f *fakeClipWriter) state() ([]byte, []byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.payload, f.signed, f.cleared
}

type clipCall struct {
	ttl time.Duration
	v   secrets.ApprovalVerifier
	aw  audit.Writer
	fx  audit.EffectWriter
}

// clipDeps is a vault with clipSecret stored, gated by gate, whose
// NewClipboard records its arguments and returns w.
func clipDeps(t *testing.T, gate secrets.ElevationGate, w *fakeClipWriter) (vaultDeps, *[]clipCall) {
	t.Helper()
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, clipSecret, "set", "API_TOKEN"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	deps.Gate = gate
	calls := &[]clipCall{}
	deps.NewClipboard = func(ttl time.Duration, v secrets.ApprovalVerifier, aw audit.Writer, fx audit.EffectWriter) (secrets.ClipboardWriter, error) {
		*calls = append(*calls, clipCall{ttl, v, aw, fx})
		return w, nil
	}
	return deps, calls
}

func memSinks() func(context.Context) (audit.Writer, audit.EffectWriter, func(), error) {
	return func(context.Context) (audit.Writer, audit.EffectWriter, func(), error) {
		log := audit.New(storetest.NewMemStore(), runtime.NewSystemClock(), nil)
		el, err := audit.NewEffectLog(log)
		return log, el, func() {}, err
	}
}

func runClip(ctx context.Context, t *testing.T, deps vaultDeps, host clipboardHost) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(ctx)
	err := vaultGetClipboard(cmd, deps, "API_TOKEN", host)
	return out.String(), errOut.String(), err
}

func fixedHost() clipboardHost {
	return clipboardHost{clock: runtime.NewSystemClock(), sinks: memSinks(),
		ttl: func(context.Context) (time.Duration, error) { return 30 * time.Second, nil }}
}

func TestVaultGetClipboardNeverWritesStdout(t *testing.T) {
	w := &fakeClipWriter{}
	deps, calls := clipDeps(t, okGate{}, w)
	stdout, stderr, err := runClip(context.Background(), t, deps, fixedHost())
	if err != nil {
		t.Fatalf("vault get --clipboard: %v", err)
	}
	payload, _, _ := w.state()
	if string(payload) != clipSecret || len(*calls) != 1 || (*calls)[0].ttl != 30*time.Second {
		t.Fatalf("writer got %q over %d calls; want the value once at 30s", payload, len(*calls))
	}
	if strings.Contains(stdout+stderr, clipSecret) || strings.Contains(stdout+stderr, "s3cret") || stdout != "" {
		t.Fatalf("stdout %q / stderr %q carry output or the value", stdout, stderr)
	}
}

// TestVaultGetClipboardClearsBeforeExit: RunE returns only after the
// backend saw the clear. The writer blocks until released, so a command that
// returns while the writer still holds the value (for example by running
// Write on a goroutine) is caught before the release.
func TestVaultGetClipboardClearsBeforeExit(t *testing.T) {
	w := &fakeClipWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(w.release) }) }
	defer release()
	deps, _ := clipDeps(t, okGate{}, w)
	done := make(chan error, 1)
	go func() { _, _, err := runClip(context.Background(), t, deps, fixedHost()); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("the command returned (%v) before the writer was reached", err)
	case <-w.entered:
	}
	select {
	case err := <-done:
		t.Fatalf("the command returned (%v) while the writer still held the value uncleared", err)
	case <-time.After(200 * time.Millisecond):
	}
	if _, _, cleared := w.state(); cleared {
		t.Fatal("cleared before the release")
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("vault get --clipboard: %v", err)
	}
	if _, _, cleared := w.state(); !cleared {
		t.Fatal("the command returned before the clipboard was cleared")
	}
}

// TestVaultGetClipboardSignalClearsImmediately: cancelling the command's
// context (what SIGINT/SIGTERM do) ends the hold and clears at once.
func TestVaultGetClipboardSignalClearsImmediately(t *testing.T) {
	w := &fakeClipWriter{hold: make(chan struct{})}
	deps, _ := clipDeps(t, okGate{}, w)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := runClip(ctx, t, deps, fixedHost()); done <- err }()
	<-w.hold
	if _, _, cleared := w.state(); cleared {
		t.Fatal("cleared before the signal")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("vault get --clipboard after cancel: %v", err)
	}
	if _, _, cleared := w.state(); !cleared {
		t.Fatal("the cancel did not clear")
	}
}

// TestClipboardWriteRefusedWithoutApproval: a refused gate mints nothing
// and builds no writer; a granted call's verifier accepts only its own
// record (vault.get.clipboard over sha256 of NAME, requester cli, a valid
// sensitivity), never a random token or another key's record.
func TestClipboardWriteRefusedWithoutApproval(t *testing.T) {
	w := &fakeClipWriter{}
	deps, calls := clipDeps(t, refusingRecorderGate{}, w)
	if _, _, err := runClip(context.Background(), t, deps, fixedHost()); !cascade.HasKind(err, cascade.KindElevationRequired) || len(*calls) != 0 {
		t.Fatalf("refused gate: err = %v, writers built = %d", err, len(*calls))
	}
	deps, calls = clipDeps(t, okGate{}, w)
	if _, _, err := runClip(context.Background(), t, deps, fixedHost()); err != nil || len(*calls) != 1 {
		t.Fatalf("granted: err = %v, writers = %d", err, len(*calls))
	}
	v := (*calls)[0].v
	_, own, _ := w.state()
	rec, err := v.Verify(own)
	if err != nil || rec.Verb != secrets.ClipboardApprovalVerb || rec.Requester != "cli" || rec.Sensitivity != "secret" ||
		rec.ParamsDigest != sha256Hex("API_TOKEN") {
		t.Fatalf("own record = %+v, %v", rec, err)
	}
	random := make([]byte, 64)
	_, _ = rand.Read(random)
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := policy.NewApprovalSigner(ephemeralApprovalKey{private: otherKey}, runtime.NewSystemClock())
	foreign, err := signer.Sign(context.Background(), *rec)
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string][]byte{"random": random, "foreign key": foreign} {
		if _, err := v.Verify(tok); err == nil {
			t.Fatalf("%s token verified", name)
		}
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// refusingRecorderGate records accesses but refuses the proof.
type refusingRecorderGate struct{ refusingGate }

func (refusingRecorderGate) RecordVaultAccess(context.Context, string, string) error { return nil }

// clipCountingGate counts every gate call.
type clipCountingGate struct{ n *int }

func (g clipCountingGate) Authorize(context.Context, string) error { *g.n++; return nil }
func (g clipCountingGate) RecordVaultAccess(context.Context, string, string) error {
	*g.n++
	return nil
}

// TestVaultGetClipboardBadTTLRefusesBeforeProof: through the real cobra
// flag, a 1s clipboard_ttl refuses with KindInvalidInput before the gate,
// custody or the writer is reached.
func TestVaultGetClipboardBadTTLRefusesBeforeProof(t *testing.T) {
	paths := firewallTestPaths(t)
	if err := os.WriteFile(paths.ConfigPath(), []byte("[secrets]\nclipboard_ttl = \"1s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gateCalls, custodyCalls := 0, 0
	w := &fakeClipWriter{}
	deps, calls := clipDeps(t, clipCountingGate{&gateCalls}, w)
	deps.Paths = paths
	inner := deps.NewCustody
	deps.NewCustody = func() (secrets.Custody, error) { custodyCalls++; return inner() }
	_, stderr, err := runVault(t, deps, "", "get", "API_TOKEN", "--clipboard")
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "secrets.clipboard_ttl") {
		t.Fatalf("err = %v (stderr %q), want KindInvalidInput naming secrets.clipboard_ttl", err, stderr)
	}
	if gateCalls != 0 || custodyCalls != 0 || len(*calls) != 0 {
		t.Fatalf("gate = %d, custody = %d, writers = %d; want all zero", gateCalls, custodyCalls, len(*calls))
	}
	if _, err := os.Stat(filepath.Join(paths.DataDir(), "secrets")); err == nil {
		t.Fatal("a refused call created the pending-clears directory")
	}
}
