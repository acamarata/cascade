package main

// Purpose (this file): the in-run half of the host-custody guard TestMain
//   enforces. It builds a fresh process firewall through the installed
//   selector, checks that the selection passed through the guard, and that
//   no platform helper has been run so far. TestMain repeats the exec check
//   after the whole run and fails it on any attempt. Also: the guard records
//   a non-file-vault backend, and the bridge vault follows the QA switch.
// Constraints: the memoized firewall is saved and restored, so the test
//   leaves the process as it found it.

import (
	"context"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
)

func TestNoHostKeychainInvocation(t *testing.T) {
	if reflect.ValueOf(selectProcessCustody).Pointer() == reflect.ValueOf(secrets.SelectCustody).Pointer() {
		t.Fatal("the process firewall selects custody with secrets.SelectCustody itself; TestMain must replace it")
	}
	resetProcessFirewall(t)
	paths := firewallTestPaths(t)
	before := interceptedHostSelections.Load()
	firewall, err := processFirewallFor(paths)
	if err != nil {
		t.Fatalf("processFirewallFor: %v", err)
	}
	if firewall.Broker() == nil {
		t.Fatal("the firewall has no broker")
	}
	// Non-vacuous: the production config (host custody unless the QA
	// switch forces the file vault) reached the guard, so the spy sat on
	// the path a host probe would take.
	if !hostCustodyConfig(paths.DataDir()).ForceFileVault && interceptedHostSelections.Load() == before {
		t.Fatal("the firewall's selection did not pass through TestMain's guard")
	}
	if n, seen := hostCustodyAttempts(); n != 0 {
		t.Fatalf("%d attempt(s) to reach host custody so far: %v", n, seen)
	}
}

// resetProcessFirewall clears the memoized firewall for one test and
// restores it afterwards, with the selector the test found.
func resetProcessFirewall(t *testing.T) {
	t.Helper()
	processFirewallState.mu.Lock()
	fw, dir, provisional := processFirewallState.fw, processFirewallState.dataDir, processFirewallState.provisional
	processFirewallState.fw, processFirewallState.dataDir, processFirewallState.provisional = nil, "", false
	processFirewallState.mu.Unlock()
	selector := selectProcessCustody
	t.Cleanup(func() {
		selectProcessCustody = selector
		processFirewallState.mu.Lock()
		processFirewallState.fw, processFirewallState.dataDir, processFirewallState.provisional = fw, dir, provisional
		processFirewallState.mu.Unlock()
	})
}

// firewallTestPaths is a path provider rooted at a temp dir.
func firewallTestPaths(t *testing.T) runtime.PathProvider {
	t.Helper()
	root := t.TempDir()
	paths, err := runtime.NewPathProvider(func(key string) string {
		if key == "CASCADE_HOME" {
			return root
		}
		return ""
	}, func() (string, error) { return root, nil })
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// TestHostCustodyGuardRecordsHostBackend: a guarded selection that returns
// any backend but the file vault is an attempt, even with no exec (linux's
// secret service connects over D-Bus, which the runner spy cannot see).
func TestHostCustodyGuardRecordsHostBackend(t *testing.T) {
	hostCustodySpy.mu.Lock()
	saved := append([]string(nil), hostCustodySpy.seen...)
	hostCustodySpy.mu.Unlock()
	t.Cleanup(func() {
		hostCustodySpy.mu.Lock()
		hostCustodySpy.seen = saved
		hostCustodySpy.mu.Unlock()
	})
	var got secrets.Config
	guard := guardHostCustody(func(cfg secrets.Config) (secrets.Custody, error) {
		got = cfg
		return namedCustody{name: "secret-service"}, nil
	})
	before, _ := hostCustodyAttempts()
	if _, err := guard(secrets.Config{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if !got.ForceFileVault || got.Runner == nil {
		t.Fatalf("the guard passed %+v, want the file vault forced with the spy runner", got)
	}
	if after, seen := hostCustodyAttempts(); after != before+1 {
		t.Fatalf("attempts %d -> %d (%v), want the host backend recorded", before, after, seen)
	}
}

// namedCustody is a custody that only reports its backend name.
type namedCustody struct {
	secrets.Custody
	name string
}

func (c namedCustody) Name() string { return c.name }

// TestBridgeCustodyFollowsQASwitch: the bridge token vault forces the file
// vault exactly when hostCustodyConfig does, is the plugin's own config
// otherwise, and is what the daemon hands the bridge.
func TestBridgeCustodyFollowsQASwitch(t *testing.T) {
	dir := t.TempDir()
	on := func(key string) string {
		if key == "CASCADE_TEST_FILE_VAULT" {
			return "1"
		}
		return ""
	}
	base := plugins.BridgeVaultConfig(dir)
	forced := base
	forced.ForceFileVault = true
	for _, tc := range []struct {
		devkeys bool
		getenv  func(string) string
		want    secrets.Config
	}{{true, func(string) string { return "" }, base}, {false, on, base}, {true, on, forced}} {
		if cfg := bridgeCustodyConfigFor(dir, tc.devkeys, tc.getenv); !reflect.DeepEqual(cfg, tc.want) {
			t.Fatalf("devkeys=%v: %+v, want %+v", tc.devkeys, cfg, tc.want)
		}
	}
	t.Setenv("CASCADE_TEST_FILE_VAULT", "1")
	prev := daemonCustodyHook
	t.Cleanup(func() { daemonCustodyHook = prev })
	var handed []secrets.Config
	daemonCustodyHook = func(site string, cfg secrets.Config) secrets.Config {
		if site == custodySiteBridge {
			handed = append(handed, cfg)
		}
		return prev(site, cfg)
	}
	paths := firewallTestPaths(t)
	clock := runtime.NewSystemClock()
	wireCascadePABridge(context.Background(), rpc.NewRegistry(), daemon.NewManifest(nil, clock), paths, clock, nil, nil)
	want := hostCustodyConfig(paths.DataDir())
	if len(handed) != 1 || handed[0].ForceFileVault != want.ForceFileVault || handed[0].Dir != want.Dir {
		t.Fatalf("the daemon handed the bridge %+v, want ForceFileVault=%v in %s", handed, want.ForceFileVault, want.Dir)
	}
}
