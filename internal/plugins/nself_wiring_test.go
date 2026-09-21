// Purpose: prove the cascade-nself bridge is REAL — the plugin reaches the
//
//	host's own builtin loader, the interceptor this file installs is backed
//	by the actual egress.Engine (a payload carrying a detectable secret
//	comes back substituted, not verbatim), the class is the only one it may
//	write to, and a registry that refuses the class leaves the plugin's
//	fail-closed default in place instead of taking the process down.
//
// SPORT: internal/plugins:nself-wiring (TEST) — P1-E25-W5-S52-T2.

package plugins

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
	nselfplugin "github.com/acamarata/cascade/plugins/nself"
)

// TestNselfWiring_PluginReachesTheHostLoader is the reachability proof the
// review's fifth finding asked for: nothing imported plugins/nself, so the
// shipped binary never carried it. This file's own import is what fixes
// that, and this test reads the result through the REAL loader.
func TestNselfWiring_PluginReachesTheHostLoader(t *testing.T) {
	// Load's error is the invalid-example manifest this package's own test
	// fixtures register, asserted by TestBuiltinRegistry_Load; loadedRegistry
	// ignores it for the same reason.
	reg := loadedRegistry(t)
	got, ok := reg.Get("cascade-nself")
	if !ok {
		t.Fatal(`BuiltinRegistry.Get("cascade-nself") = not found, want the wired registration`)
	}
	if len(got.Manifest.Provides.Tools) != 2 {
		t.Fatalf("loaded manifest declares %d tools, want 2", len(got.Manifest.Provides.Tools))
	}
}

// TestNselfWiring_ClassAndTierMirrorTheRealInventory checks the two string
// values the plugin's local mirror must keep identical to the real ones.
// The constants make a divergence a compile error; this makes it legible.
func TestNselfWiring_ClassAndTierMirrorTheRealInventory(t *testing.T) {
	if nselfEgressClass != egress.EgressClassNselfBackend {
		t.Fatalf("class mirror = %q, want %q", nselfEgressClass, egress.EgressClassNselfBackend)
	}
	if nselfEgressTier != egress.TierInternal {
		t.Fatalf("tier mirror = %q, want %q", nselfEgressTier, egress.TierInternal)
	}
}

// TestNselfWiring_RealEngineSubstitutesADetectableSecret proves the
// interceptor is the real firewall and not a pass-through: a payload
// carrying a value the detector recognizes does not come back verbatim.
func TestNselfWiring_RealEngineSubstitutesADetectableSecret(t *testing.T) {
	interceptor, err := newRealNselfInterceptor()
	if err != nil {
		t.Fatalf("newRealNselfInterceptor: %v", err)
	}
	payload := []byte(`{"detected":true,"note":"ghp_0123456789abcdefghijklmnopqrstuvwxyz"}`)
	out, err := interceptor.InterceptClass(context.Background(),
		nselfplugin.EgressClassNselfBackend, nselfplugin.TierInternal, payload)
	if err != nil {
		t.Fatalf("InterceptClass: %v", err)
	}
	if bytes.Equal(out, payload) {
		t.Fatal("the real engine returned the payload byte-for-byte; a detectable secret was not substituted (pass-through)")
	}
	if bytes.Contains(out, []byte("ghp_0123456789abcdefghijklmnopqrstuvwxyz")) {
		t.Fatalf("output still carries the secret: %s", out)
	}
}

func TestNselfWiring_RefusesAnyOtherClass(t *testing.T) {
	interceptor, err := newRealNselfInterceptor()
	if err != nil {
		t.Fatalf("newRealNselfInterceptor: %v", err)
	}
	_, err = interceptor.InterceptClass(context.Background(),
		nselfplugin.EgressClass(egress.EgressClassMCP), nselfplugin.TierInternal, []byte(`{}`))
	if err == nil {
		t.Fatal("InterceptClass on a foreign class = nil error, want a refusal")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindCapabilityDenied {
		t.Fatalf("cascade.KindOf(err) = (%v, %v), want (KindCapabilityDenied, true)", kind, ok)
	}
}

// TestInstallNselfEgressInterceptor_BuildFailureLeavesTheRefusingDefault
// drives init()'s own body with a failing constructor: the plugin keeps
// refusing (fail-closed) and the operator is told once, in a log line —
// never a panic, because an operator disabling this egress class must not
// take the daemon down.
func TestInstallNselfEgressInterceptor_BuildFailureLeavesTheRefusingDefault(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	wantErr := errors.New("class disabled by the operator")

	installNselfEgressInterceptor(func() (nselfplugin.EgressInterceptor, error) {
		return nil, wantErr
	}, log)

	if !strings.Contains(logged.String(), "class disabled by the operator") {
		t.Fatalf("log = %q, want the build failure reported", logged.String())
	}
	if !strings.Contains(logged.String(), string(nselfEgressClass)) {
		t.Fatalf("log = %q, want the class named", logged.String())
	}
}

// TestInstallNselfEgressInterceptor_InstallsWhatItBuilds proves the success
// branch actually reaches the plugin's setter.
func TestInstallNselfEgressInterceptor_InstallsWhatItBuilds(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	installed := &countingNselfInterceptor{}

	installNselfEgressInterceptor(func() (nselfplugin.EgressInterceptor, error) {
		return installed, nil
	}, log)
	t.Cleanup(func() { installNselfEgressInterceptor(newRealNselfInterceptor, log) })

	// The scan root carries a real marker layout, so the plugin answers from
	// the filesystem alone: no test here forks the real nself CLI.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nself"), 0o755); err != nil {
		t.Fatalf("seed marker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nself", "build-version"), []byte("1\n"), 0o600); err != nil {
		t.Fatalf("seed marker file: %v", err)
	}
	got, _ := loadedRegistry(t).Get("cascade-nself")
	if _, err := got.Handlers.DispatchTool(context.Background(), "nself_project_info",
		[]byte(`{"root_dir":"`+root+`"}`)); err != nil {
		t.Fatalf("DispatchTool through the loaded plugin = %v, want nil", err)
	}
	if installed.calls != 1 {
		t.Fatalf("installed interceptor saw %d calls, want 1 (the setter was not reached)", installed.calls)
	}
	if strings.Contains(logged.String(), "not installed") {
		t.Fatalf("log = %q, want no failure line on the success branch", logged.String())
	}
}

// countingNselfInterceptor records how often the plugin wrote through it.
type countingNselfInterceptor struct{ calls int }

func (c *countingNselfInterceptor) InterceptClass(
	_ context.Context,
	_ nselfplugin.EgressClass,
	_ nselfplugin.SensitivityTier,
	content []byte,
) ([]byte, error) {
	c.calls++
	return content, nil
}

// TestInstallNselfEgressInterceptor_SetterRefusalIsLogged covers the third
// branch: the constructor succeeded but handed back nothing usable, so the
// plugin's own setter refuses and the operator is told.
func TestInstallNselfEgressInterceptor_SetterRefusalIsLogged(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	installNselfEgressInterceptor(func() (nselfplugin.EgressInterceptor, error) {
		return nil, nil
	}, log)
	t.Cleanup(func() { installNselfEgressInterceptor(newRealNselfInterceptor, log) })

	if !strings.Contains(logged.String(), "rejected by the plugin") {
		t.Fatalf("log = %q, want the setter refusal reported", logged.String())
	}
}

// TestNselfEmptyVault_IsHonestlyEmpty pins the disclosed gap: the exact-value
// pass has no values, and says so rather than inventing one.
func TestNselfEmptyVault_IsHonestlyEmpty(t *testing.T) {
	names, err := nselfEmptyVault{}.List(context.Background())
	if err != nil || len(names) != 0 {
		t.Fatalf("List = (%v, %v), want (no names, nil)", names, err)
	}
	value, err := nselfEmptyVault{}.Get(context.Background(), "anything")
	if err == nil {
		t.Fatal("Get = nil error, want a not-found refusal")
	}
	if value != nil {
		t.Fatalf("Get returned %q, want nothing", value)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("cascade.KindOf(err) = (%v, %v), want (KindNotFound, true)", kind, ok)
	}
}

// TestNewNselfInterceptor_ARegistryWithoutTheClassRefuses is the operational
// branch that decides whether a disabled egress class takes the daemon down:
// construction fails, the caller logs, and the plugin keeps refusing.
func TestNewNselfInterceptor_ARegistryWithoutTheClassRefuses(t *testing.T) {
	got, err := newNselfInterceptor(egress.NewRegistry())
	if err == nil {
		t.Fatal("newNselfInterceptor over a registry without the class = nil error, want a refusal")
	}
	if got != nil {
		t.Fatalf("newNselfInterceptor returned %v alongside an error, want nil", got)
	}
	if !strings.Contains(err.Error(), string(nselfEgressClass)) {
		t.Fatalf("err = %v, want it to name the class it could not acquire", err)
	}
}
