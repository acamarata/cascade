//go:build !windows

package process

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
)

// fakeExitError is a minimal exitCoder, standing in for *exec.ExitError.
type fakeExitError struct{ code int }

func (e fakeExitError) Error() string { return "fake exit" }
func (e fakeExitError) ExitCode() int { return e.code }

// fakeCommander is a Commander driven entirely in-process over io.Pipe,
// so Launch's trusted-tier gate, consent warning, handshake and monitor
// can be exercised without forking a real subprocess.
type fakeCommander struct {
	version         string
	exitCode        int
	crashAfterHello bool
	starts          *int32

	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	waitErr chan error
}

func newFakeCommander(version string, exitCode int, crashAfterHello bool, starts *int32) *fakeCommander {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	return &fakeCommander{
		version: version, exitCode: exitCode, crashAfterHello: crashAfterHello, starts: starts,
		stdinR: stdinR, stdinW: stdinW, stdoutR: stdoutR, stdoutW: stdoutW,
		waitErr: make(chan error, 1),
	}
}

func (f *fakeCommander) StdinPipe() (io.WriteCloser, error) { return f.stdinW, nil }
func (f *fakeCommander) StdoutPipe() (io.ReadCloser, error) { return f.stdoutR, nil }

func (f *fakeCommander) Start() error {
	if f.starts != nil {
		atomic.AddInt32(f.starts, 1)
	}
	go f.respond()
	return nil
}

func (f *fakeCommander) Wait() error { return <-f.waitErr }

func (f *fakeCommander) respond() {
	scanner := bufio.NewScanner(f.stdinR)
	seenHello := false
	for scanner.Scan() {
		var probe struct {
			ID *uint64 `json:"id"`
		}
		line := scanner.Bytes()
		if json.Unmarshal(line, &probe) != nil || probe.ID == nil {
			continue
		}
		var result json.RawMessage
		if !seenHello {
			ack := HelloAckMsg{ProtocolVersion: f.version, ManifestHash: "h"}
			result, _ = json.Marshal(ack)
			seenHello = true
		} else {
			result = json.RawMessage(`{"ok":true}`)
		}
		if !f.writeResponse(*probe.ID, result) {
			return
		}
		if seenHello && f.crashAfterHello {
			f.waitErr <- fakeExitError{code: f.exitCode}
			return
		}
	}
	// The stdin pipe closed (EOF or an explicit Close, e.g. the runtime
	// terminating this plugin after a version-mismatch refusal). Report
	// an exit so a blocked cmd.Wait() unblocks, unless a crash exit was
	// already sent above.
	select {
	case f.waitErr <- fakeExitError{code: f.exitCode}:
	default:
	}
}

func (f *fakeCommander) writeResponse(id uint64, result json.RawMessage) bool {
	resp := Response{JSONRPC: "2.0", ID: id, Result: result}
	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	_, err := f.stdoutW.Write(data)
	return err == nil
}

// fakeAuditSink records every CrashReport it is handed.
type fakeAuditSink struct {
	mu      sync.Mutex
	reports []CrashReport
}

func (a *fakeAuditSink) LogCrash(r CrashReport) {
	a.mu.Lock()
	a.reports = append(a.reports, r)
	a.mu.Unlock()
}

func (a *fakeAuditSink) snapshot() []CrashReport {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]CrashReport, len(a.reports))
	copy(out, a.reports)
	return out
}

// egressRegistryAdapter binds this package's local EgressRegistrar seam
// to a real internal/hooks/egress.Registry, exactly as a composition-root
// package (out of this ticket's files_scope) would. Living in a _test.go
// file is deliberate: golangci's plugins-providers-boundary depguard rule
// exempts test files from the "no internal/** import" restriction
// (12-QUALITY-CONSTITUTION Art.10.2), so this is the one place in the
// package allowed to know internal/hooks/egress exists, and it proves the
// seam against the REAL registry rather than a second fake.
type egressRegistryAdapter struct{ reg *egress.Registry }

func (a egressRegistryAdapter) Register(class EgressClass, cfg EgressRegistrationConfig) error {
	tiers := make([]egress.SensitivityTier, len(cfg.AllowedTiers))
	for i, t := range cfg.AllowedTiers {
		tiers[i] = egress.SensitivityTier(t)
	}
	err := a.reg.Register(egress.EgressClass(class), egress.InterceptConfig{
		Enabled: cfg.Enabled, AllowRestricted: cfg.AllowRestricted, AllowedTiers: tiers, Owner: cfg.Owner,
	})
	if err != nil && errors.Is(err, egress.ErrDuplicateClass) {
		return nil // EgressRegistrar.Register's documented idempotency contract
	}
	return err
}

func trustedManifest(name string) Manifest {
	return Manifest{Name: name, TrustTier: TrustTierTrusted, NetScopes: []string{"github.com"}, Command: "unused"}
}

func TestLaunchRejectsUntrustedManifestNoProcessForked(t *testing.T) {
	var starts int32
	rt := &ProcessRuntime{
		Stderr:    &bytes.Buffer{},
		Registrar: egressRegistryAdapter{reg: egress.NewRegistry()},
		commandFactory: func(_ context.Context, _ Manifest) Commander {
			return newFakeCommander("1.0.0", 1, false, &starts)
		},
	}
	m := Manifest{Name: "untrusted-demo", TrustTier: TrustTierUntrusted}
	h, err := rt.Launch(context.Background(), m)
	if err == nil || h != nil {
		t.Fatalf("expected a refusal and a nil handle, got handle=%v err=%v", h, err)
	}
	if !errors.Is(err, ErrUntrustedPlugin) {
		t.Fatalf("expected ErrUntrustedPlugin, got %v", err)
	}
	if atomic.LoadInt32(&starts) != 0 {
		t.Fatalf("Start() was called %d times; the trusted-tier gate must fork no process", starts)
	}
}

func TestLaunchEmitsConsentWarningBeforeExec(t *testing.T) {
	var stderr bytes.Buffer
	var starts int32
	rt := &ProcessRuntime{
		Stderr:    &stderr,
		Registrar: egressRegistryAdapter{reg: egress.NewRegistry()},
		commandFactory: func(_ context.Context, _ Manifest) Commander {
			return newFakeCommander("1.0.0", 1, false, &starts)
		},
	}
	h, err := rt.Launch(context.Background(), trustedManifest("consent-demo"))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	_ = h
	warning := stderr.String()
	if !bytes.Contains([]byte(warning), []byte("consent-demo")) || !bytes.Contains([]byte(warning), []byte("github.com")) {
		t.Fatalf("consent warning missing name/scopes: %q", warning)
	}
	if atomic.LoadInt32(&starts) != 1 {
		t.Fatalf("expected exactly one process start, got %d", starts)
	}
}

func TestLaunchHandshakeReadyForRoundTripCalls(t *testing.T) {
	rt := &ProcessRuntime{
		Stderr:    &bytes.Buffer{},
		Registrar: egressRegistryAdapter{reg: egress.NewRegistry()},
		commandFactory: func(_ context.Context, _ Manifest) Commander {
			return newFakeCommander("1.0.0", 0, false, nil)
		},
	}
	h, err := rt.Launch(context.Background(), trustedManifest("roundtrip-demo"))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	result, err := h.Call(context.Background(), "demo.echo", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(result) != `{"ok":true}` {
		t.Fatalf("result = %s", result)
	}
}

func TestLaunchVersionMismatchTerminatesProcess(t *testing.T) {
	rt := &ProcessRuntime{
		Stderr:    &bytes.Buffer{},
		Registrar: egressRegistryAdapter{reg: egress.NewRegistry()},
		commandFactory: func(_ context.Context, _ Manifest) Commander {
			// Below the host's default PluginProtocolVersion (1.0.0):
			// Launch must refuse, close stdin, and reap the process
			// rather than leaving it running or Launch blocked forever.
			return newFakeCommander("0.1.0", 0, false, nil)
		},
	}
	h, err := rt.Launch(context.Background(), trustedManifest("mismatch-demo"))
	if h != nil || err == nil {
		t.Fatalf("expected a version-mismatch refusal, got handle=%v err=%v", h, err)
	}
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("expected ErrVersionMismatch, got %v", err)
	}
}

func TestLaunchCrashIsolationExhaustsRestartsToInvalid(t *testing.T) {
	audit := &fakeAuditSink{}
	var starts int32
	rt := &ProcessRuntime{
		Stderr:    &bytes.Buffer{},
		Registrar: egressRegistryAdapter{reg: egress.NewRegistry()},
		Restart:   RestartPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond},
		Audit:     audit,
		commandFactory: func(_ context.Context, _ Manifest) Commander {
			return newFakeCommander("1.0.0", 17, true, &starts)
		},
	}
	h, err := rt.Launch(context.Background(), trustedManifest("crashy-demo"))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.State() != "invalid" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if h.State() != "invalid" {
		t.Fatalf("expected state-invalid after exhausting restarts, got %q", h.State())
	}
	_, err = h.Call(context.Background(), "demo.echo", nil)
	if !errors.Is(err, ErrPluginUnavailable) {
		t.Fatalf("expected ErrPluginUnavailable, got %v", err)
	}
	reports := audit.snapshot()
	if len(reports) == 0 {
		t.Fatal("expected at least one crash report in the audit log")
	}
	last := reports[len(reports)-1]
	if !last.Final || last.ExitCode != 17 || last.PluginName != "crashy-demo" {
		t.Fatalf("unexpected final crash report: %+v", last)
	}
	if atomic.LoadInt32(&starts) < 2 {
		t.Fatalf("expected at least 2 process starts (initial + restart), got %d", starts)
	}
}
