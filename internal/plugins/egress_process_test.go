package plugins

// Purpose (this file): the plugin wiring's outbound paths write through the
//   process firewall, so a stored value with no credential shape leaves only
//   as its vault reference. TestMain installs the package default: a process
//   firewall over a temp-dir file vault, the role the cascade binary's own
//   firewall plays in production. Without it every path here refuses, which
//   is the fail-closed answer and not one a test of the happy path can use.
// Constraints: no test reaches the host keychain or secret service: every
//   custody here forces the file vault under a temp dir, and HOME and
//   USERPROFILE point at a throwaway directory for the whole run.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	nselfplugin "github.com/acamarata/cascade/plugins/nself"
)

// processFirewallDefault is the factory TestMain installs; tests that plant
// a canary swap it and put this one back.
var processFirewallDefault func(*egress.Registry) (*egress.Engine, error)

func TestMain(m *testing.M) {
	os.Exit(runWithProcessFirewall(m))
}

// runWithProcessFirewall builds the package default firewall, runs the
// suite, and removes the temp directory whatever the outcome.
func runWithProcessFirewall(m *testing.M) int {
	dir, err := os.MkdirTemp("", "cascade-plugins-egress")
	if err != nil {
		fmt.Fprintln(os.Stderr, "plugins tests: temp dir:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if err := os.Setenv(key, dir); err != nil {
			fmt.Fprintln(os.Stderr, "plugins tests: redirect", key, err)
			return 1
		}
	}
	vault, err := fileEgressVault(context.Background(), dir, "", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "plugins tests: process vault:", err)
		return 1
	}
	processFirewallDefault = engineOver(vault)
	egress.SetProcessEngineFactory(processFirewallDefault)
	return m.Run()
}

// fileEgressVault opens a file vault under dir, optionally storing one value.
func fileEgressVault(ctx context.Context, dir, name, value string) (*secrets.EgressVault, error) {
	custody, err := secrets.SelectCustody(secrets.Config{
		Service: "cascade-plugins-egress-test", Dir: dir, ForceFileVault: true, Passphrase: "plugins-egress-pass",
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("test: no platform keychain helper")
		},
	})
	if err != nil {
		return nil, err
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		return nil, err
	}
	if name != "" {
		if _, err := broker.Set(ctx, name, []byte(value), secrets.SetUpdate); err != nil {
			return nil, err
		}
	}
	return secrets.NewEgressVault(broker)
}

// engineOver is a process firewall factory over vault and the process
// detector.
func engineOver(vault *secrets.EgressVault) func(*egress.Registry) (*egress.Engine, error) {
	return func(r *egress.Registry) (*egress.Engine, error) {
		detector, err := secrets.ProcessDetector()
		if err != nil {
			return nil, err
		}
		return egress.NewEngine(r, vault, detector)
	}
}

// plantCanary makes the process firewall one whose vault holds the canary,
// for the rest of the test.
func plantCanary(t *testing.T) {
	t.Helper()
	vault, err := fileEgressVault(context.Background(), t.TempDir(), pluginCanaryName, pluginCanaryValue)
	if err != nil {
		t.Fatal(err)
	}
	egress.SetProcessEngineFactory(engineOver(vault))
	t.Cleanup(func() { egress.SetProcessEngineFactory(processFirewallDefault) })
}

// remoteEnabledRegistry is a registry holding the plugin-remote class with
// its shipped policy, enabled (the default registry ships it disabled).
func remoteEnabledRegistry(t *testing.T) *egress.Registry {
	t.Helper()
	cfg, ok := egress.DefaultRegistry().Lookup(egress.EgressClassPluginRemote)
	if !ok {
		t.Fatal("the plugin-remote class is not registered")
	}
	cfg.Enabled = true
	reg := egress.NewRegistry()
	if err := reg.Register(egress.EgressClassPluginRemote, cfg); err != nil {
		t.Fatal(err)
	}
	return reg
}

// The canary: at least eight bytes, no credential shape, so only the
// exact-value pass can catch it.
const (
	pluginCanaryName  = "PLUGIN_CANARY"
	pluginCanaryValue = "plain-canary-9f"
)

// assertCanaryRedacted fails unless out carries the vault reference and not
// the value.
func assertCanaryRedacted(t *testing.T, out []byte) {
	t.Helper()
	if bytes.Contains(out, []byte(pluginCanaryValue)) {
		t.Fatalf("the stored value crossed verbatim: %s", out)
	}
	if !bytes.Contains(out, []byte(pluginCanaryName)) {
		t.Fatalf("the stored value was not replaced by its vault reference: %s", out)
	}
}

func TestNselfEgressRedactsVaultValue(t *testing.T) {
	plantCanary(t)
	interceptor, err := newRealNselfInterceptor()
	if err != nil {
		t.Fatalf("newRealNselfInterceptor: %v", err)
	}
	out, err := interceptor.InterceptClass(context.Background(), nselfplugin.EgressClassNselfBackend,
		nselfplugin.TierInternal, []byte(`{"note":"`+pluginCanaryValue+`"}`))
	if err != nil {
		t.Fatalf("InterceptClass: %v", err)
	}
	assertCanaryRedacted(t, out)
}

func TestRemoteDispatchEgressRedactsVaultValue(t *testing.T) {
	plantCanary(t)
	interceptor, err := newDispatchRemoteInterceptorFrom(remoteEnabledRegistry(t))
	if err != nil {
		t.Fatalf("newDispatchRemoteInterceptorFrom: %v", err)
	}
	out, err := interceptor.Intercept(context.Background(), []byte(`{"abi":1,"note":"`+pluginCanaryValue+`"}`))
	if err != nil {
		t.Fatalf("Intercept: %v", err)
	}
	assertCanaryRedacted(t, out)
}

// TestPluginEgressRefusesWithoutAProcessFirewall: with no firewall
// installed, both paths refuse rather than write through an engine with no
// value source.
func TestPluginEgressRefusesWithoutAProcessFirewall(t *testing.T) {
	egress.SetProcessEngineFactory(nil)
	t.Cleanup(func() { egress.SetProcessEngineFactory(processFirewallDefault) })
	interceptor, err := newRealNselfInterceptor()
	if err == nil {
		_, err = interceptor.InterceptClass(context.Background(), nselfplugin.EgressClassNselfBackend,
			nselfplugin.TierInternal, []byte(`{}`))
	}
	if err == nil {
		t.Fatal("the nself path wrote with no process firewall installed")
	}
	if _, err := newDispatchRemoteInterceptorFrom(remoteEnabledRegistry(t)); err == nil {
		t.Fatal("the remote-dispatch path built an interceptor with no process firewall installed")
	}
}

// nselfEmptyVault and dispatchRemoteEmptyVault are TEST fixtures: value
// sources holding nothing, for the adapter tests that drive an engine over
// an empty store. Shipping code no longer declares an empty vault (the
// architecture gate forbids one); both paths use the process firewall.
type (
	nselfEmptyVault          struct{}
	dispatchRemoteEmptyVault struct{}
)

func (nselfEmptyVault) List(context.Context) ([]string, error) { return nil, nil }

func (nselfEmptyVault) Get(context.Context, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "plugin: test vault has no entries")
}

func (dispatchRemoteEmptyVault) List(context.Context) ([]string, error) { return nil, nil }

func (dispatchRemoteEmptyVault) Get(context.Context, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "plugin: test vault has no entries")
}
