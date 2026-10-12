package main

// Purpose (this file): the process firewall's contract, on every OS. A
//   custody failure is refused with its cause and not cached; the first
//   success is memoized and bound to its data directory; the engine's value
//   source is the firewall's own EgressVault; every host vault selection
//   asks for the same custody, QA switch included; provider health names
//   the firewall's cause. The [secrets] child-process and daemon tests are
//   in egress_firewall_detector_test.go.
// Constraints: custody is the file vault under temp dirs (TestMain's
//   selector, or an explicit failing one).

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestProcessFirewallRefusesWithoutCustody(t *testing.T) {
	resetProcessFirewall(t)
	working := selectProcessCustody
	cause := secrets.ErrNoCustodyAvailable()
	calls := 0
	selectProcessCustody = func(secrets.Config) (secrets.Custody, error) { calls++; return nil, cause }

	fw, err := processFirewallFor(firewallTestPaths(t))
	if fw != nil || err != cause { //nolint:errorlint // identity: the custody error is returned unchanged
		t.Fatalf("processFirewallFor = (%v, %v), want (nil, the custody error unchanged)", fw, err)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err kind = %v, want KindUnavailable", err)
	}
	engine, err := egress.ProcessEngine(egress.DefaultRegistry())
	if engine != nil || err != cause { //nolint:errorlint // identity through the process factory
		t.Fatalf("egress.ProcessEngine = (%v, %v), want (nil, the custody error unchanged)", engine, err)
	}
	if calls != 2 {
		t.Fatalf("selector ran %d times; each call must retry, not reuse the failure", calls)
	}

	// The failure was not cached: a working selector now succeeds.
	selectProcessCustody = working
	if fw, err = processFirewallFor(firewallTestPaths(t)); err != nil || fw == nil {
		t.Fatalf("after custody became available: (%v, %v), want a firewall", fw, err)
	}
	if engine, err = egress.ProcessEngine(egress.DefaultRegistry()); err != nil || engine == nil {
		t.Fatalf("egress.ProcessEngine after recovery = (%v, %v)", engine, err)
	}
}

func TestProcessFirewallForMemoized(t *testing.T) {
	resetProcessFirewall(t)
	paths := firewallTestPaths(t)
	first, err := processFirewallFor(paths)
	if err != nil {
		t.Fatal(err)
	}
	second, err := processFirewallFor(paths)
	if err != nil || second != first {
		t.Fatalf("second call = (%p, %v), want the same firewall %p", second, err, first)
	}
	// Another data directory is refused, naming no path: its grant store
	// must never be paired with this firewall's broker.
	other := firewallTestPaths(t)
	fw, err := processFirewallFor(other)
	if fw != nil || !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("processFirewallFor(another data dir) = (%v, %v), want a KindConflict refusal", fw, err)
	}
	if strings.Contains(err.Error(), other.DataDir()) || strings.Contains(err.Error(), paths.DataDir()) {
		t.Fatalf("the conflict names a path: %v", err)
	}
	// The daemon's composition root owns the binding and rebinds it.
	owned, err := bindDaemonFirewall(other)
	if err != nil || owned == first {
		t.Fatalf("bindDaemonFirewall(other) = (%v, %v), want a new firewall", owned, err)
	}
	if fw, err := processFirewallFor(other); err != nil || fw != owned {
		t.Fatalf("after the daemon rebound: (%v, %v), want its firewall", fw, err)
	}
	// The path-less factory's binding over the default paths is provisional:
	// the first caller naming a directory takes it over, and is then held.
	resetProcessFirewall(t)
	provisional, err := boundProcessFirewall()
	if err != nil {
		t.Fatal(err)
	}
	if named, err := processFirewallFor(paths); err != nil || named == provisional {
		t.Fatalf("a named dir did not take over the provisional binding: (%v, %v)", named, err)
	}
	if _, err := processFirewallFor(other); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("after takeover another dir = %v, want KindConflict", err)
	}
	// A caller with no paths is refused even with a firewall memoized.
	for _, paths := range []runtime.PathProvider{nil, unresolvedFirewallPaths{}} {
		if fw, err := processFirewallFor(paths); fw != nil || !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("processFirewallFor(%T) = (%v, %v), want a KindUnavailable refusal", paths, fw, err)
		}
	}
}

// TestProcessFirewallNamedConfirmEndsProvisional: a named caller confirming
// the provisional dir keeps that firewall and ends the provisional state, so
// no later caller may take it over.
func TestProcessFirewallNamedConfirmEndsProvisional(t *testing.T) {
	resetProcessFirewall(t)
	provisional, err := boundProcessFirewall()
	if err != nil {
		t.Fatal(err)
	}
	if named, err := processFirewallFor(lazyPaths{}); err != nil || named != provisional {
		t.Fatalf("naming the provisional dir = (%v, %v), want the same firewall", named, err)
	}
	if _, err := processFirewallFor(firewallTestPaths(t)); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("after a named confirm another dir = %v, want KindConflict", err)
	}
}

// unresolvedFirewallPaths resolves nothing, as lazyPaths does when the home
// directory cannot be found.
type unresolvedFirewallPaths struct{}

func (unresolvedFirewallPaths) Root() string                       { return "" }
func (unresolvedFirewallPaths) ConfigPath() string                 { return "" }
func (unresolvedFirewallPaths) SocketPath() string                 { return "" }
func (unresolvedFirewallPaths) DataDir() string                    { return "" }
func (unresolvedFirewallPaths) LogDir() string                     { return "" }
func (unresolvedFirewallPaths) StorageRoot(runtime.Profile) string { return "" }

func TestProcessFirewallEngineUsesRealVault(t *testing.T) {
	resetProcessFirewall(t)
	fw, err := processFirewallFor(firewallTestPaths(t))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := fw.Engine(egress.DefaultRegistry())
	if err != nil {
		t.Fatalf("Engine: %v", err)
	}
	vault := reflect.ValueOf(engine).Elem().FieldByName("vault")
	if !vault.IsValid() || vault.IsNil() {
		t.Fatal("the engine has no vault field, or it is nil")
	}
	if got := vault.Elem().Type(); got != reflect.TypeOf(fw.vault) {
		t.Fatalf("engine vault type = %v, want %T", got, fw.vault)
	}
	if vault.Elem().Pointer() != reflect.ValueOf(fw.vault).Pointer() {
		t.Fatal("the engine's vault is not the firewall's *secrets.EgressVault")
	}
	if (*processFirewall)(nil).Broker() != nil {
		t.Fatal("a nil firewall reported a broker")
	}
	if e, err := (*processFirewall)(nil).Engine(egress.DefaultRegistry()); e != nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("nil firewall Engine = (%v, %v), want a KindUnavailable refusal", e, err)
	}
}

// TestProcessFirewallRedactsVaultValue: the engine every package gets from
// egress.ProcessEngine reads the process vault, so a shapeless stored value
// of eight bytes or more leaves only as its vault reference.
func TestProcessFirewallRedactsVaultValue(t *testing.T) {
	resetProcessFirewall(t)
	selectProcessCustody = testProcessCustody(t.TempDir())
	const name, value = "PROCESS_CANARY", "plain-canary-9f"
	fw, err := processFirewallFor(lazyPaths{})
	if err != nil {
		t.Fatalf("processFirewallFor: %v", err)
	}
	if _, err := fw.Broker().Set(context.Background(), name, []byte(value), secrets.SetUpdate); err != nil {
		t.Fatalf("Set: %v", err)
	}
	engine, err := egress.ProcessEngine(egress.DefaultRegistry())
	if err != nil {
		t.Fatalf("egress.ProcessEngine: %v", err)
	}
	out, err := engine.InterceptClass(context.Background(), egress.EgressClassMCP, egress.TierInternal,
		[]byte(`{"output":"`+value+`"}`))
	if err != nil {
		t.Fatalf("InterceptClass: %v", err)
	}
	if bytes.Contains(out, []byte(value)) || !bytes.Contains(out, []byte(name)) {
		t.Fatalf("the stored value was not replaced by its vault reference: %s", out)
	}
}

// TestProcessFirewallQAFileVaultOnlyInDevkeys: the QA switch reaches the
// custody config only in a devkeys build (the one devkeys constant), and it
// keeps the data directory, so the store does not move.
func TestProcessFirewallQAFileVaultOnlyInDevkeys(t *testing.T) {
	dir := t.TempDir()
	on := func(key string) string {
		if key == "CASCADE_TEST_FILE_VAULT" {
			return "1"
		}
		return ""
	}
	if cfg := hostCustodyConfigFor(dir, false, on); cfg.ForceFileVault || cfg.Dir != dir || cfg.Service != vaultService {
		t.Fatalf("release build honoured the QA switch: %+v", cfg)
	}
	if cfg := hostCustodyConfigFor(dir, true, on); !cfg.ForceFileVault || cfg.Dir != dir || cfg.Service != vaultService {
		t.Fatalf("devkeys build ignored the QA switch or moved the store: %+v", cfg)
	}
}

// TestHostCustodySitesShareOneSelector: the firewall, `cascade vault`,
// `provider add` and doctor select through selectProcessCustody with the
// same config for the same data directory, so under the QA switch they all
// read and write one store.
func TestHostCustodySitesShareOneSelector(t *testing.T) {
	resetProcessFirewall(t)
	t.Setenv("CASCADE_TEST_FILE_VAULT", "1")
	var seen []secrets.Config
	selectProcessCustody = func(c secrets.Config) (secrets.Custody, error) {
		seen = append(seen, c)
		return nil, errors.New("stop")
	}
	paths := firewallTestPaths(t)
	_, _ = processFirewallFor(paths)
	_, _ = providerDepsFor(paths).NewCustody()
	_, _ = newDoctorCustody(paths.DataDir())
	_, _ = productionVaultDeps().NewCustody()
	if len(seen) != 4 {
		t.Fatalf("%d selections went through selectProcessCustody, want 4", len(seen))
	}
	for i, c := range seen {
		dir := paths.DataDir()
		if i == 3 {
			dir = (lazyPaths{}).DataDir()
		}
		if c.Service != vaultService || c.Dir != dir || c.ForceFileVault != elevation.DevkeysBuild() || c.Runner != nil {
			t.Fatalf("selection %d = %+v, want {%s %s ForceFileVault=%v}", i, c, vaultService, dir, elevation.DevkeysBuild())
		}
	}
}

// TestProviderHealthEgressNamesFirewallCause: when the firewall cannot be
// built, provider health's gate refuses with the custody cause's Kind and
// message, never the wrapped path, and never a nil gate.
func TestProviderHealthEgressNamesFirewallCause(t *testing.T) {
	resetProcessFirewall(t)
	const vaultPath = "/vault-dir-must-not-leak"
	cause := cascade.Wrap(cascade.KindUnavailable, &os.PathError{Op: "mkdir", Path: vaultPath, Err: os.ErrPermission},
		"secrets: no custody backend is available")
	selectProcessCustody = func(secrets.Config) (secrets.Custody, error) { return nil, cause }
	gate := egressEngineForHealth()
	if gate == nil {
		t.Fatal("health got a nil gate; the cause would be lost")
	}
	_, err := gate.Capability(egress.EgressClassProviderIntake)
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "no custody backend is available") {
		t.Fatalf("Capability = %v, want the custody cause's Kind and message", err)
	}
	if strings.Contains(err.Error(), vaultPath) {
		t.Fatalf("the cause leaked a path: %v", err)
	}
	if got := firewallCause(errors.New("plain")); !cascade.HasKind(got, cascade.KindUnavailable) {
		t.Fatalf("firewallCause(non-cascade) = %v, want KindUnavailable", got)
	}
}

// TestProcessDetectionSourceIsInstalled: the cascade binary installs the
// [secrets] source at init, so no second source can be installed.
func TestProcessDetectionSourceIsInstalled(t *testing.T) {
	err := secrets.SetProcessDetectionConfigSource(func() (secrets.DetectionConfig, error) {
		return secrets.DetectionConfig{}, nil
	})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("installing a second source = %v, want KindConflict (init installs one)", err)
	}
}

func TestDetectionConfigFromSection(t *testing.T) {
	got, err := detectionConfigFrom(map[string]interface{}{})
	if err != nil || got.ConfidenceThreshold != secrets.DefaultConfidenceThreshold {
		t.Fatalf("no section = (%+v, %v), want the shipped defaults", got, err)
	}
	if _, err := detectionConfigFrom(map[string]interface{}{"secrets": "x"}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("non-table section = %v, want KindInvalidInput", err)
	}
	got, err = detectionConfigFrom(map[string]interface{}{"secrets": map[string]interface{}{"confidence_threshold": 0.9}})
	if err != nil || got.ConfidenceThreshold != 0.9 {
		t.Fatalf("section = (%+v, %v), want threshold 0.9", got, err)
	}
}
