package ci

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

func requireBaseline(t *testing.T, b *Baseline, root string, names ...string) {
	t.Helper()
	tips, err := b.Tips(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if tips == nil || len(tips) != len(names) {
		t.Fatalf("tips: %v, want %v", tips, names)
	}
	for i, name := range names {
		if tips[i].Ref != "refs/heads/"+name || tips[i].Commit != baselineGitTest(t, root, "rev-parse", name) {
			t.Fatalf("tip %v", tips[i])
		}
	}
}

func TestRegistrationBaselineExcludesForeignUpstreams(t *testing.T) {
	root := baselineRepo(t)
	baselineCommit(t, root)
	for _, name := range []string{"local", "pr-7", "fork-b", "dot"} {
		baselineGitTest(t, root, "checkout", "-b", name, "main")
		baselineCommit(t, root)
	}
	baselineGitTest(t, root, "config", "remote.origin.url", "owned")
	baselineGitTest(t, root, "config", "remote.fork.url", "foreign")
	for _, row := range [][3]string{{"main", "origin", "refs/heads/main"}, {"pr-7", "origin", "refs/pull/7/head"}, {"fork-b", "fork", "refs/heads/b"}, {"dot", ".", "refs/heads/main"}} {
		baselineGitTest(t, root, "config", "branch."+row[0]+".remote", row[1])
		baselineGitTest(t, root, "config", "branch."+row[0]+".merge", row[2])
	}
	baselineGitTest(t, root, "checkout", "main")
	b := RegistrationBaseline(BaselinePolicy{Owned: func(u string) bool { return u == "owned" }})
	requireBaseline(t, b, root, "main")
	if len(b.Excluded()) != 4 {
		t.Fatal(b.Excluded())
	}
	for name, reason := range map[string]string{"local": "", "pr-7": ExcludedPullRef, "fork-b": ExcludedNotOwned, "dot": ExcludedLocalUpstream} {
		baselineGitTest(t, root, "checkout", name)
		if reason == "" {
			requireBaseline(t, b, root, name)
		} else {
			requireBaseline(t, b, root)
			found := false
			for _, e := range b.Excluded() {
				if e.Tip.Ref == "refs/heads/"+name && e.Reason == reason {
					found = true
				}
			}
			if !found {
				t.Fatal(b.Excluded())
			}
		}
	}
	b = RegistrationBaseline(BaselinePolicy{Explicit: []string{"main", "pr-7"}})
	requireBaseline(t, b, root, "main", "pr-7")
	testBaselineDetached(t, root)
	testBaselineErrors(t)
	testBaselineHeadErrors(t)
}

func testBaselineDetached(t *testing.T, root string) {
	t.Helper()
	baselineGitTest(t, root, "checkout", "--detach", "main")
	b := RegistrationBaseline(BaselinePolicy{})
	requireBaseline(t, b, root)
	if !b.Detached() {
		t.Fatal("detached not reported")
	}
	baselineCommit(t, root)
	requireBaseline(t, b, root)
	if !b.Detached() {
		t.Fatal("detached commit not reported")
	}
	b = RegistrationBaseline(BaselinePolicy{Explicit: []string{"main"}})
	requireBaseline(t, b, root, "main")
	if b.Detached() {
		t.Fatal("explicit reported detached")
	}
}

func testBaselineErrors(t *testing.T) {
	t.Helper()
	failure := cascade.New(cascade.KindUnavailable, "injected read error")
	tips := []jobs.BranchTip{{Ref: "refs/heads/main", Commit: strings.Repeat("a", 40)}, {Ref: "refs/heads/other", Commit: strings.Repeat("b", 40)}}
	deps := baselineDeps{tips: func(context.Context, string) ([]jobs.BranchTip, error) { return tips, nil },
		head: func(context.Context, string) (string, bool, error) { return "refs/heads/main", false, nil },
		config: func(context.Context, string) ([]byte, error) {
			return []byte("branch.main.remote\norigin\x00branch.main.merge\nrefs/heads/main\x00remote.origin.url\nowned\x00"), nil
		}}
	calls := 0
	deps.effectiveURL = func(context.Context, string, string) (string, error) {
		calls++
		if calls > 1 {
			return "", failure
		}
		return "owned", nil
	}
	b := registrationBaselineWith(BaselinePolicy{Owned: func(string) bool { return true }}, deps)
	if _, err := b.Tips(context.Background(), ""); err != nil || len(b.Excluded()) != 1 {
		t.Fatalf("positive: %v %v", b.Excluded(), err)
	}
	if got, err := b.Tips(context.Background(), ""); err != failure || got != nil || b.Excluded() == nil || len(b.Excluded()) != 0 {
		t.Fatalf("stale results: %v %v %v", got, err, b.Excluded())
	}
	for _, bad := range []string{"broken", "branch.main.remote"} {
		deps.config = func(context.Context, string) ([]byte, error) { return []byte(bad), nil }
		b = registrationBaselineWith(BaselinePolicy{}, deps)
		if _, err := b.Tips(context.Background(), ""); err == nil {
			t.Fatal("malformed config accepted")
		}
	}
	deps.config = func(context.Context, string) ([]byte, error) { return nil, failure }
	b = registrationBaselineWith(BaselinePolicy{}, deps)
	if _, err := b.Tips(context.Background(), ""); err != failure {
		t.Fatal(err)
	}
}

func TestRegistrationBaselineOwnedPredicate(t *testing.T) {
	root, owned, foreign := ownedBaselineFixture(t)
	var seen []string
	b := RegistrationBaseline(BaselinePolicy{Owned: func(u string) bool { seen = append(seen, u); return u == owned }})
	requireBaseline(t, b, root, "main")
	if !reflect.DeepEqual(seen, []string{owned, owned}) {
		t.Fatal(seen)
	}
	b = RegistrationBaseline(BaselinePolicy{})
	requireBaseline(t, b, root)
	if b.Excluded()[0].Reason != ExcludedNoOwnedTest {
		t.Fatal(b.Excluded())
	}
	baselineGitTest(t, root, "config", "url."+foreign+".insteadOf", owned)
	b = RegistrationBaseline(BaselinePolicy{Owned: func(u string) bool { return u == owned }})
	requireBaseline(t, b, root)
	if b.Excluded()[0].Reason != ExcludedNotOwned {
		t.Fatal(b.Excluded())
	}
	baselineGitTest(t, root, "config", "--unset", "url."+foreign+".insteadOf")
	baselineGitTest(t, root, "config", "remote.origin.url", foreign)
	baselineGitTest(t, root, "config", "url."+owned+".insteadOf", foreign)
	requireBaseline(t, b, root)
	if b.Excluded()[0].Reason != ExcludedNotOwned {
		t.Fatal(b.Excluded())
	}
	for _, remote := range []string{"ghost", owned} {
		baselineGitTest(t, root, "config", "branch.main.remote", remote)
		requireBaseline(t, b, root)
	}
}

func ownedBaselineFixture(t *testing.T) (string, string, string) {
	t.Helper()
	source := baselineRepo(t)
	baselineCommit(t, source)
	owned := filepath.Join(t.TempDir(), "owned.git")
	foreign := filepath.Join(t.TempDir(), "foreign.git")
	baselineGitTest(t, source, "clone", "--bare", source, owned)
	baselineGitTest(t, source, "clone", "--bare", source, foreign)
	clone := filepath.Join(t.TempDir(), "clone")
	baselineGitTest(t, source, "clone", owned, clone)
	return clone, owned, foreign
}

func testBaselineHeadErrors(t *testing.T) {
	t.Helper()
	failure := cascade.New(cascade.KindUnavailable, "injected HEAD failure")
	tips := []jobs.BranchTip{{Ref: "refs/heads/main", Commit: strings.Repeat("a", 40)}}
	d := baselineDeps{tips: func(context.Context, string) ([]jobs.BranchTip, error) { return tips, nil },
		config: func(context.Context, string) ([]byte, error) { return nil, nil },
		head:   func(context.Context, string) (string, bool, error) { return "", false, failure }}
	b := registrationBaselineWith(BaselinePolicy{}, d)
	if got, err := b.Tips(context.Background(), ""); err != failure || got != nil || len(b.Excluded()) != 0 || b.Detached() {
		t.Fatalf("HEAD failure treated as facts: %v %v", got, err)
	}
	d.head = func(context.Context, string) (string, bool, error) { return "refs/tags/x", false, nil }
	b = registrationBaselineWith(BaselinePolicy{}, d)
	if _, err := b.Tips(context.Background(), ""); err == nil {
		t.Fatal("tag HEAD accepted")
	}
	d.tips = func(context.Context, string) ([]jobs.BranchTip, error) { return nil, failure }
	b = registrationBaselineWith(BaselinePolicy{}, d)
	if _, err := b.Tips(context.Background(), ""); err != failure {
		t.Fatal(err)
	}
	d.tips = func(context.Context, string) ([]jobs.BranchTip, error) { return tips, nil }
	b = registrationBaselineWith(BaselinePolicy{Explicit: []string{"main", "nope"}}, d)
	if got, err := b.Tips(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "nope") || got != nil || len(b.Excluded()) != 0 {
		t.Fatalf("unknown explicit branch: %v %v", got, err)
	}
}
