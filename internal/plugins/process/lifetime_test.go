//go:build !windows

package process

import (
	"bytes"
	"context"
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
