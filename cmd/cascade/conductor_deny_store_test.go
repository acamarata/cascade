//go:build !windows

// Purpose: the daemon's conductor quota policy and owner deny store, proven
//
//	over a composed daemon in a fresh HOME: the configured deny refuses, the
//	configured ceiling holds, both survive a restart, a hot reload changes
//	them live, and a value that cannot be read refuses start or every class.
//
// SPORT: cmd/cascade/daemon:conductor-quota-deny (tests, P1-CAP-07).
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// composedPolicy is one composed daemon's conductor policy and reloader.
type composedPolicy struct {
	policy *conductorPolicy
	live   *liveConductor
	stop   func()
}

// writeDaemonConfig writes body as the daemon's config.toml.
func writeDaemonConfig(t *testing.T, deps daemonDeps, body string) {
	t.Helper()
	path := deps.Paths.ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// policyDeps is a fresh HOME and CASCADE_HOME with body as config.toml.
func policyDeps(t *testing.T, body string) daemonDeps {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CASCADE_TEST_FILE_VAULT", filepath.Join(t.TempDir(), "vault"))
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	writeDaemonConfig(t, deps, body)
	return deps
}

// composePolicy runs composeDaemon over deps and returns its policy.
func composePolicy(t *testing.T, deps daemonDeps) (composedPolicy, error) {
	t.Helper()
	_, cleanups, err := composeDaemon(context.Background(), deps, nil)
	stop := func() { runCleanupsLIFO(cleanups); cleanups = nil }
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil {
		stop()
		return composedPolicy{}, err
	}
	livePolicies.mu.Lock()
	p, owner := livePolicies.byConfig[deps.Paths.ConfigPath()], livePolicies.owner[deps.Paths.ConfigPath()]
	livePolicies.mu.Unlock()
	if p == nil || owner == nil {
		t.Fatal("composeDaemon published no conductor policy for its config path")
	}
	return composedPolicy{policy: p, live: owner, stop: stop}, nil
}

func mustComposePolicy(t *testing.T, deps daemonDeps) composedPolicy {
	t.Helper()
	c, err := composePolicy(t, deps)
	if err != nil {
		t.Fatalf("composeDaemon: %v", err)
	}
	return c
}

// authorize runs the production OwnerPolicy over c's deny store.
func (c composedPolicy) authorize(class string) error {
	return conductor.NewOwnerPolicy(c.policy.Deny).Authorize(context.Background(), provider.ModelRequest{TaskID: "t", TaskClass: class})
}

// reload triggers the daemon's hot reload and fails unless it was accepted.
func (c composedPolicy) reload(t *testing.T) {
	t.Helper()
	if out := c.live.reloader.Reload(context.Background()); !out.Accepted {
		t.Fatalf("hot reload refused: %+v", out)
	}
}

func assertClassDenied(t *testing.T, err error, class string) {
	t.Helper()
	if err == nil {
		t.Fatalf("task class %q was allowed", class)
	}
	if k, _ := cascade.KindOf(err); k != cascade.KindPolicyDenied {
		t.Fatalf("kind = %v, want policy_denied: %v", k, err)
	}
	for _, want := range []string{"[conductor].deny_task_classes", `"` + class + `"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err, want)
		}
	}
}

func TestP1ConductorUsesDurableOwnerDeny(t *testing.T) {
	deps := policyDeps(t, "[conductor]\ndeny_task_classes = [\"code\"]\n")
	c := mustComposePolicy(t, deps)
	assertClassDenied(t, c.authorize("code"), "code")
	if err := c.authorize("chat"); err != nil {
		t.Fatalf("chat refused: %v", err)
	}
	c.stop()

	c = mustComposePolicy(t, deps) // restart: read from the file again
	assertClassDenied(t, c.authorize("code"), "code")
	if err := c.authorize("chat"); err != nil {
		t.Fatalf("chat refused after restart: %v", err)
	}
	writeDaemonConfig(t, deps, "[conductor]\ndeny_task_classes = [\"code\", \"chat\"]\n")
	c.reload(t)
	assertClassDenied(t, c.authorize("chat"), "chat")
}

func TestOwnerDenyPrecedesEveryDoor(t *testing.T) {
	c := mustComposePolicy(t, policyDeps(t, "[conductor]\ndeny_task_classes = [\"code\"]\n"))
	// The request view each door verb hands Policy.Authorize: the Execute
	// verbs carry the task class, the leaf verbs carry none (door.go).
	for _, verb := range []conductor.DoorVerb{conductor.VerbExecute, conductor.VerbExecuteStream} {
		t.Run(string(verb), func(t *testing.T) { assertClassDenied(t, c.authorize("code"), "code") })
	}
	// An unreadable deny value refuses every door, whatever the class.
	broken := &ownerDenyStore{live: func() *runtime.Config {
		return &runtime.Config{Extra: map[string]interface{}{"conductor": map[string]interface{}{"deny_task_classes": "code"}}}
	}}
	pol := conductor.NewOwnerPolicy(broken)
	for _, verb := range []conductor.DoorVerb{conductor.VerbExecute, conductor.VerbExecuteStream, conductor.VerbChat, conductor.VerbEmbed, conductor.VerbCount, conductor.VerbStream} {
		t.Run("unreadable/"+string(verb), func(t *testing.T) {
			class := ""
			if verb == conductor.VerbExecute || verb == conductor.VerbExecuteStream {
				class = "chat"
			}
			err := pol.Authorize(context.Background(), provider.ModelRequest{TaskClass: class})
			if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindPolicyDenied || !strings.Contains(err.Error(), "deny configuration unavailable") {
				t.Fatalf("unreadable deny did not refuse %s: %v", verb, err)
			}
		})
	}
}

// quotaLanesReader is a two-lane registry the real router selects over.
type quotaLanesReader struct{ permitsReader }

func (quotaLanesReader) ListProviders(context.Context) ([]provider.ProviderInfo, error) {
	return []provider.ProviderInfo{{Name: "prov-a", BaseURL: "https://a.example.com", HealthStatus: "healthy"},
		{Name: "prov-b", BaseURL: "https://b.example.com", HealthStatus: "healthy"}}, nil
}

func (quotaLanesReader) ListLanes(context.Context) ([]provider.LaneInfo, error) {
	return []provider.LaneInfo{{LaneName: "laneA", ProviderName: "prov-a", State: "active"},
		{LaneName: "laneB", ProviderName: "prov-b", State: "active"}}, nil
}

// selectQuotaLane asks a real router over q for the next lane, the way the
// conductor does (the router is the quota's sole caller).
func selectQuotaLane(ctx context.Context, q *conductor.QuotaPolicy) (string, error) {
	sel, err := conductor.NewRouter(quotaLanesReader{}, q, nil, nil).Select(ctx, provider.ModelRequest{
		TaskID: "t1", TaskClass: "chat", Sensitivity: provider.SensitivityPublic,
		Inputs: []provider.ChatMessage{{Role: "user", Content: "hi"}},
	})
	return sel.LaneID, err
}

func TestP1ConductorUsesConfiguredQuota(t *testing.T) {
	body := "[conductor.quota]\nspill_order = [\"laneA\"]\n[conductor.quota.ceiling]\nlaneA = 2\n"
	deps := policyDeps(t, body)
	c := mustComposePolicy(t, deps)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if lane, err := selectQuotaLane(ctx, c.policy.Quota); err != nil || lane != "laneA" {
			t.Fatalf("admission %d = %q, %v", i+1, lane, err)
		}
	}
	_, err := selectQuotaLane(ctx, c.policy.Quota)
	if !errors.Is(err, conductor.ErrAllLanesExhausted) || !strings.Contains(err.Error(), "laneA") {
		t.Fatalf("third admission = %v, want exhausted naming laneA", err)
	}
	if k, _ := cascade.KindOf(err); k != cascade.KindQuotaExhausted {
		t.Fatalf("kind = %v, want quota_exhausted", k)
	}
	writeDaemonConfig(t, deps, "[conductor.quota]\nspill_order = [\"laneB\", \"laneA\"]\n[conductor.quota.ceiling]\nlaneA = 2\n")
	c.reload(t)
	if lane, err := selectQuotaLane(ctx, c.policy.Quota); err != nil || lane != "laneB" {
		t.Fatalf("after spill_order reload = %q, %v, want laneB", lane, err)
	}
	c.stop()

	writeDaemonConfig(t, deps, body)
	c = mustComposePolicy(t, deps) // restart: the ceiling comes from the file
	for i := 0; i < 2; i++ {
		if err := c.policy.Quota.AdmitLane("laneA"); err != nil {
			t.Fatalf("admission %d after restart: %v", i+1, err)
		}
	}
	err = c.policy.Quota.AdmitLane("laneA")
	var ce *cascade.Error
	if !errors.As(err, &ce) || err != conductor.ErrLaneCeilingReached {
		t.Fatalf("third admission after restart = %v, want ErrLaneCeilingReached", err)
	}
}
