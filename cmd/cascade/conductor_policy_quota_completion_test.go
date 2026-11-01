//go:build !windows

// Purpose: the conductor wiring takes its quota and deny store from the one
//
//	daemon policy (no nil-config quota, no nil deny store), start refuses a
//	value it cannot read, and the conductor guarded family reports a deny
//	removal or a ceiling raise as loosening.
//
// SPORT: cmd/cascade/daemon:conductor-quota-deny (tests, P1-CAP-07).
package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// productionSources returns every non-test Go file under cmd/.
func productionSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		out[path] = string(b)
		return rerr
	})
	if err != nil {
		t.Fatalf("walk cmd/: %v", err)
	}
	return out
}

func TestP1RuntimeQuotaAndOwnerPolicy(t *testing.T) {
	counts := map[string]int{}
	for path, src := range productionSources(t) {
		for _, banned := range []string{"ParseQuotaConfig(nil)", "NewOwnerPolicy(nil)"} {
			if strings.Contains(src, banned) {
				t.Errorf("%s still calls %s", path, banned)
			}
		}
		counts["conductor.NewQuotaPolicy("] += strings.Count(src, "conductor.NewQuotaPolicy(")
		counts["ownerDenyStore{"] += strings.Count(src, "ownerDenyStore{")
	}
	for k, n := range counts {
		if n != 1 {
			t.Errorf("%s constructed %d times in daemon code, want exactly 1", k, n)
		}
	}
	deps := policyDeps(t, "[conductor]\ndeny_task_classes = [\"code\"]\n[conductor.quota.ceiling]\nlaneA = 2\n")
	c := mustComposePolicy(t, deps)
	got, err := conductorPolicyFor(deps.Paths, deps.Clock)
	if err != nil || got != c.policy {
		t.Fatalf("the conductor wiring's policy %p (err %v) is not the daemon's %p", got, err, c.policy)
	}
	if c.live.reloader == nil || c.live.reloader.Current() == nil {
		t.Fatal("the policy has no live reloader")
	}
	if c.policy.Deny.(*ownerDenyStore).live().Extra == nil {
		t.Fatal("the deny store does not read the reloader's current config")
	}
}

func TestConductorConfigFailsClosed(t *testing.T) {
	for name, body := range map[string]string{
		"string":        "[conductor]\ndeny_task_classes = \"code\"\n",
		"non-string":    "[conductor]\ndeny_task_classes = [\"code\", 3]\n",
		"unknown class": "[conductor]\ndeny_task_classes = [\"no-such-class\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := composePolicy(t, policyDeps(t, body))
			if err == nil || !strings.Contains(err.Error(), "deny_task_classes") {
				t.Fatalf("composeDaemon = %v, want a refusal naming deny_task_classes", err)
			}
		})
	}
	t.Run("malformed quota", func(t *testing.T) {
		_, err := composePolicy(t, policyDeps(t, "[conductor.quota]\nceiling = \"lots\"\n"))
		if err == nil || !strings.Contains(err.Error(), "quota") {
			t.Fatalf("composeDaemon = %v, want a refusal naming the quota", err)
		}
	})
	t.Run("unreadable live value", checkUnreadableLiveValueDenies)
	t.Run("absent key", checkAbsentKeyAllows)
}

// checkUnreadableLiveValueDenies proves an unreadable live deny value refuses
// every class, naming the cause, instead of allowing it.
func checkUnreadableLiveValueDenies(t *testing.T) {
	s := &ownerDenyStore{live: func() *runtime.Config {
		return &runtime.Config{Extra: map[string]interface{}{"conductor": map[string]interface{}{"deny_task_classes": 7}}}
	}}
	for _, class := range []string{"chat", "code", ""} {
		reason, denied := s.DeniedTaskClass(context.Background(), class)
		if !denied || !strings.HasPrefix(reason, "deny configuration unavailable: ") || !strings.Contains(reason, runtime.ConductorDenyKey) {
			t.Fatalf("class %q: (%q, %v), want denied with the cause", class, reason, denied)
		}
	}
	if d := denyDetail(s); d != "deny_task_classes: unavailable (refusing)" {
		t.Fatalf("detail = %q", d)
	}
	nilLive := &ownerDenyStore{live: func() *runtime.Config { return nil }}
	if _, denied := nilLive.DeniedTaskClass(context.Background(), "chat"); !denied {
		t.Fatal("no live config allowed a class")
	}
}

// checkAbsentKeyAllows proves a config with no deny key refuses nothing and
// reports that on the conductor policy status.
func checkAbsentKeyAllows(t *testing.T) {
	c := mustComposePolicy(t, policyDeps(t, ""))
	for _, class := range []string{"chat", "code"} {
		if err := c.authorize(class); err != nil {
			t.Fatalf("absent key refused %q: %v", class, err)
		}
	}
	var detail string
	for _, s := range c.live.manifest.Snapshot() {
		if s.Name == conductorPolicyStatus {
			detail = s.Detail
		}
	}
	if detail != "deny_task_classes: none configured" {
		t.Fatalf("status detail = %q", detail)
	}
}

func TestConductorFamilyLoosening(t *testing.T) {
	const deny = "conductor.deny_task_classes"
	one, two := "[conductor]\ndeny_task_classes = [\"code\"]\n", "[conductor]\ndeny_task_classes = [\"code\", \"chat\"]\n"
	expectLoosening(t, one, "", deny)
	expectLoosening(t, two, one, deny)
	ceil := func(n string) string { return "[conductor.quota.ceiling]\nlaneA = " + n + "\n" }
	expectLoosening(t, ceil("2"), ceil("5"), "conductor.quota.ceiling.laneA")
	if out := reload(t, ceil("2"), ""); !out.ElevationRequired || !slices.ContainsFunc(out.LooseningPaths, func(p runtime.LooseningPath) bool {
		return p.Family == "conductor" && strings.HasPrefix(p.Key, "conductor.quota")
	}) {
		t.Fatalf("removing a ceiling: %+v, want a conductor loosening row", out)
	}
	for _, p := range reload(t, two, one).LooseningPaths {
		if p.Family != "conductor" {
			t.Fatalf("deny removal reported under family %q", p.Family)
		}
	}
	if reason := conductorExempt["spill_order"]; reason == "" {
		t.Fatal("spill_order has no tightening-only exemption reason")
	}
	spill := func(s string) string { return "[conductor.quota]\nspill_order = [" + s + "]\n" }
	if out := reload(t, spill(`"laneA"`), spill(`"laneB", "laneA"`)); !out.Accepted {
		t.Fatalf("a spill_order change needed elevation: %+v", out)
	}
}
