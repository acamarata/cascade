//go:build !windows

package process

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
)

// capabilitySpy is a real HostCapabilityChecker that reports the one
// host call a launched plugin makes. It is the only positive evidence
// available that the plugin process is still ALIVE after Launch has
// returned: a killed process makes no host call at all.
type capabilitySpy struct{ seen chan string }

func (capabilitySpy) CheckHTTP(context.Context, string) error    { return nil }
func (capabilitySpy) CheckStorage(context.Context, string) error { return nil }
func (capabilitySpy) CheckGeneric(context.Context, string) error { return nil }

func (c capabilitySpy) CheckSecretRef(_ context.Context, key string) (any, error) {
	c.seen <- key
	return nil, nil
}

// TestLaunch_PluginOutlivesTheStartupDeadline is the regression proof for
// the lifetime defect lifetime.go documents: Launch bounded the plugin
// process with the SAME context that bounds its startup handshake, and
// that context's `defer cancel()` fires the moment Launch returns — so
// defaultCommandFactory's os/exec.CommandContext SIGKILLed every plugin
// immediately on launch, before it could issue a single host call.
//
// The script deliberately makes its host call AFTER the startup deadline
// has already expired (1s timeout, 2s sleep). That ordering is what makes
// this a real lifetime assertion rather than a cancel()-removal
// assertion: any wiring that leaves the process bounded by the startup
// deadline fails here even if nothing cancels it early.
func TestLaunch_PluginOutlivesTheStartupDeadline(t *testing.T) {
	frame := `{"jsonrpc":"2.0","method":"host_secret_ref","params":{"key":"after-startup-deadline"}}`
	script := `read _line; ` +
		`printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocol_version":"1.0.0","manifest_hash":"deadbeef"}}'; ` +
		`sleep 2; printf '%s\n' '` + frame + `'`

	spy := capabilitySpy{seen: make(chan string, 4)}
	rt := NewProcessRuntime()
	rt.Stderr = &bytes.Buffer{}
	rt.Registrar = egressRegistryAdapter{reg: egress.NewRegistry()}
	rt.CapabilityChecker = spy
	rt.StartupTimeout = time.Second
	// One exit is terminal: a respawn would re-run the script and emit a
	// second, meaningless host call. InitialBackoff nonzero is what makes
	// MaxAttempts:0 stick (RestartPolicy.resolved() reads both fields as
	// unset only when both are the zero value).
	rt.Restart = RestartPolicy{MaxAttempts: 0, InitialBackoff: time.Nanosecond}

	m := Manifest{
		Name: "sh-outlives-startup", TrustTier: TrustTierTrusted,
		Command: "sh", Args: []string{"-c", script},
	}
	if _, err := rt.Launch(context.Background(), m); err != nil {
		t.Skipf("real sh subprocess unavailable, skipping: %v", err)
	}

	select {
	case key := <-spy.seen:
		if key != "after-startup-deadline" {
			t.Fatalf("host call key = %q, want %q", key, "after-startup-deadline")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the plugin made no host call after Launch returned: its process was killed with the startup context (lifetime.go)")
	}
}

// drainCommander is a fake process whose stdout the test closes by hand, so
// "the plugin has exited but its output has not been fully read" becomes a
// state the test controls rather than a race it hopes to hit.
type drainCommander struct {
	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	// waitEntered closes the moment Wait is called, which is the exact
	// instant os/exec would close the stdout pipe on a real *exec.Cmd.
	waitEntered chan struct{}
}

func newDrainCommander() *drainCommander {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	return &drainCommander{
		stdinR: stdinR, stdinW: stdinW, stdoutR: stdoutR, stdoutW: stdoutW,
		waitEntered: make(chan struct{}),
	}
}

func (c *drainCommander) StdinPipe() (io.WriteCloser, error) { return c.stdinW, nil }
func (c *drainCommander) StdoutPipe() (io.ReadCloser, error) { return c.stdoutR, nil }
func (c *drainCommander) Wait() error                        { close(c.waitEntered); return nil }

// Start answers the handshake and nothing else; the test drives every
// later frame itself.
func (c *drainCommander) Start() error {
	go func() {
		scanner := bufio.NewScanner(c.stdinR)
		if !scanner.Scan() {
			return
		}
		var probe struct {
			ID *uint64 `json:"id"`
		}
		if json.Unmarshal(scanner.Bytes(), &probe) != nil || probe.ID == nil {
			return
		}
		ack, _ := json.Marshal(HelloAckMsg{ProtocolVersion: "1.0.0", ManifestHash: "h"})
		resp, _ := json.Marshal(Response{JSONRPC: "2.0", ID: *probe.ID, Result: ack})
		_, _ = c.stdoutW.Write(append(resp, '\n'))
	}()
	return nil
}

// TestMonitorReadsPluginOutputBeforeReapingIt is the ordering proof for the
// drain barrier. os/exec closes the stdout pipe as soon as Wait sees the
// command exit, so a supervisor that reaps first discards whatever the
// plugin wrote on its way out -- including a host call it is entitled to
// have handled. That loss is load-sensitive, so it is asserted here as an
// ORDERING invariant rather than by trying to lose a frame on purpose:
// while stdout is still open, Wait must not have been called.
func TestMonitorReadsPluginOutputBeforeReapingIt(t *testing.T) {
	cmd := newDrainCommander()
	spy := capabilitySpy{seen: make(chan string, 4)}
	rt := &ProcessRuntime{
		Stderr:            &bytes.Buffer{},
		Registrar:         egressRegistryAdapter{reg: egress.NewRegistry()},
		CapabilityChecker: spy,
		Restart:           RestartPolicy{MaxAttempts: 0, InitialBackoff: time.Nanosecond},
		commandFactory:    func(context.Context, Manifest) Commander { return cmd },
	}

	if _, err := rt.Launch(context.Background(), trustedManifest("drain")); err != nil {
		t.Fatalf("launch: %v", err)
	}

	// The plugin makes a host call and then goes quiet WITHOUT its stdout
	// being closed: it has effectively exited, but its output is unread.
	frame := `{"jsonrpc":"2.0","method":"host_secret_ref","params":{"key":"last-words"}}`
	if _, err := cmd.stdoutW.Write([]byte(frame + "\n")); err != nil {
		t.Fatalf("write the plugin's final frame: %v", err)
	}

	select {
	case key := <-spy.seen:
		if key != "last-words" {
			t.Fatalf("host call key = %q, want %q", key, "last-words")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the plugin's final host call was never dispatched")
	}

	// THE INVARIANT: stdout is still open, so reads are not finished, so
	// the monitor must not have reaped the process yet. Without the drain
	// barrier Wait is called immediately after launch and this fails.
	select {
	case <-cmd.waitEntered:
		t.Fatal("the monitor called Wait while the plugin's stdout was still open: os/exec closes that pipe on Wait, so any output not yet read is discarded")
	default:
	}

	// Closing stdout is what a real exit does; the monitor may reap now.
	if err := cmd.stdoutW.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cmd.waitEntered:
	case <-time.After(30 * time.Second):
		t.Fatal("the monitor never reaped the process after its stdout reached EOF")
	}
}
