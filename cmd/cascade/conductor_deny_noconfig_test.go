//go:build !windows

// Purpose: a daemon with no config file runs on the default configuration,
//
//	so an absent [conductor].deny_task_classes denies no class, while a
//	deny value that is present but unreadable still refuses every class
//	and a wiring with no configuration at all stays closed.
//
// SPORT: cmd/cascade/daemon:conductor-quota-deny (tests).
package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// fileConductorPolicy is the conductor policy a test wiring gets when it has
// no composed daemon: the deny store reads the configuration loaded from
// paths' config file, exactly as the daemon's reloader does at start. With
// no file that is the default configuration, so no class is denied.
func fileConductorPolicy(ctx context.Context, t *testing.T, paths runtime.PathProvider, clock runtime.Clock) *conductorPolicy {
	t.Helper()
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{
		Path:    paths.ConfigPath(),
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	})
	if err != nil {
		t.Fatalf("load config %s: %v", paths.ConfigPath(), err)
	}
	policy, err := buildConductorPolicy(func() *runtime.Config { return cfg }, clock)
	if err != nil {
		t.Fatalf("buildConductorPolicy: %v", err)
	}
	return policy
}

// fileDenyPolicy is fileConductorPolicy's owner policy.
func fileDenyPolicy(ctx context.Context, t *testing.T, paths runtime.PathProvider, clock runtime.Clock) conductor.PolicyEvaluator {
	t.Helper()
	return conductor.NewOwnerPolicy(fileConductorPolicy(ctx, t, paths, clock).Deny)
}

// prepareDaemonDir makes paths' data directory and publishes
// fileConductorPolicy for paths, the way a composed daemon does, so a
// registration wired by hand over paths finds a live deny store; the entry
// is dropped at cleanup.
func prepareDaemonDir(ctx context.Context, t *testing.T, paths runtime.PathProvider, clock runtime.Clock) {
	t.Helper()
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	owner := &liveConductor{}
	livePolicies.set(paths.ConfigPath(), owner, fileConductorPolicy(ctx, t, paths, clock))
	t.Cleanup(func() { livePolicies.drop(paths.ConfigPath(), owner) })
}

func TestDaemonWithoutConfigFileDispatchesChat(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CASCADE_TEST_FILE_VAULT", t.TempDir()+"/vault")
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	if _, err := os.Stat(deps.Paths.ConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("config file present before the daemon starts: %v", err)
	}
	c := mustComposePolicy(t, deps)
	if c.live.reloader.Current() == nil {
		t.Fatal("a daemon with no config file has no effective configuration")
	}
	for _, class := range []string{"chat", "code", ""} {
		if err := c.authorize(class); err != nil {
			t.Fatalf("no config file refused %q: %v", class, err)
		}
	}
}

func TestFileDenyPolicyAbsentFileAllowsAndInvalidRefuses(t *testing.T) {
	ctx, clock := context.Background(), runtime.NewSystemClock()
	paths := fakeMemoryPaths{root: t.TempDir()}
	absent := fileDenyPolicy(ctx, t, paths, clock)
	if err := absent.Authorize(ctx, provider.ModelRequest{TaskID: "t", TaskClass: "chat"}); err != nil {
		t.Fatalf("an absent config file denied chat: %v", err)
	}
	deps := policyDeps(t, "[conductor]\ndeny_task_classes = \"chat\"\n")
	if _, err := composePolicy(t, deps); err == nil || !strings.Contains(err.Error(), "deny_task_classes") {
		t.Fatalf("composeDaemon over an invalid deny value = %v, want a refusal naming it", err)
	}
	broken := &ownerDenyStore{live: func() *runtime.Config {
		return &runtime.Config{Extra: map[string]interface{}{"conductor": map[string]interface{}{"deny_task_classes": "chat"}}}
	}}
	if reason, denied := broken.DeniedTaskClass(ctx, "chat"); !denied || !strings.Contains(reason, "deny configuration unavailable") {
		t.Fatalf("invalid deny value: (%q, %v), want denied as unavailable", reason, denied)
	}
	none := &ownerDenyStore{live: func() *runtime.Config { return nil }}
	if _, denied := none.DeniedTaskClass(ctx, "chat"); !denied {
		t.Fatal("a wiring with no configuration allowed chat")
	}
}
