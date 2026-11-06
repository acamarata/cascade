package generate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/runtime"
)

// acceptanceRepo is a fresh git repository holding go.mod and main.go.
func acceptanceRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required: %v", err)
	}
	root, _ := newRepo(t)
	if out, err := git(t, root, "init", "-q"); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	put(t, root, "go.mod", "module example.com/acceptance\n\ngo 1.22\n")
	put(t, root, "main.go", "package main\n\nfunc main() {}\n")
	return root
}

// acceptanceRegister is a real Register: git rev-parse plus the repo
// scanners.
func acceptanceRegister(t *testing.T) func(context.Context, string) (repo.Inventory, error) {
	return func(ctx context.Context, root string) (repo.Inventory, error) {
		top, err := git(t, root, "rev-parse", "--show-toplevel")
		if err != nil {
			return repo.Inventory{}, fmt.Errorf("rev-parse: %v: %s", err, top)
		}
		top = strings.TrimSpace(top)
		inv := repo.Inventory{Repository: repo.RepositoryRef{ID: "repo-acceptance"}}
		if inv.Languages, err = repo.DetectAll(ctx, top); err != nil {
			return inv, err
		}
		if inv.Layout, err = repo.ScanLayout(top); err != nil {
			return inv, err
		}
		if inv.CI, err = repo.ScanCI(top); err != nil {
			return inv, err
		}
		inv.Harness, err = repo.ScanHarness(top)
		return inv, err
	}
}

// acceptanceRun runs generation over root with the unsupported replay probe.
func acceptanceRun(t *testing.T, root string, now time.Time, rec *recorder) Result {
	t.Helper()
	var calls []runCall
	res, err := Run(context.Background(), root, RunDeps{
		Clock:     runtime.NewFixedClock(now),
		Attention: rec.sink(),
		Register:  acceptanceRegister(t),
		Facts:     &sourceDouble{},
		Probe:     fixtureProbe(t, "unsupported-fixture.json", &calls),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// wantManagedSet is the closed set a fresh repository gets, in leg order.
func wantManagedSet() []string {
	out := []string{".claude/CLAUDE.md", ".claude/rules/root.md", ".cascade/policy-kernel.md"}
	out = append(append(out, wantAgentPaths...), wantSkillPaths...)
	return append(out, "AGENTS.md", ".codex/config.toml", "opencode.json")
}

// hashes maps each path and the manifest to the sha256 of its bytes.
func hashes(t *testing.T, root string, paths []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range append(append([]string(nil), paths...), ManifestRel) {
		sum := sha256.Sum256([]byte(slurp(t, root, p)))
		out[p] = hex.EncodeToString(sum[:])
	}
	return out
}

func TestGenerateAcceptanceManagedSet(t *testing.T) {
	root := acceptanceRepo(t)
	rec := &recorder{}
	res := acceptanceRun(t, root, fixedNow, rec)
	want := wantManagedSet()
	if !reflect.DeepEqual(res.Written, want) {
		t.Fatalf("Written = %v\nwant %v", res.Written, want)
	}
	if len(res.Adopted) != 0 || len(res.Refused) != 0 || len(rec.calls) != 0 {
		t.Fatalf("adopted %v refused %+v sink %+v", res.Adopted, res.Refused, rec.calls)
	}
	if d := res.Deferral; d == nil || d.Generator != genWorkflows || d.Owner != workflowsDeferralOwner || d.Expiry != workflowsDeferralExpiry {
		t.Fatalf("Deferral = %+v", d)
	}
	if !missing(root, ".claude/workflows") {
		t.Fatal("an unsupported probe still wrote .claude/workflows")
	}
	m, ok, err := ReadManifest(root)
	if err != nil || !ok {
		t.Fatalf("ReadManifest: %v %v", ok, err)
	}
	var paths []string
	for _, e := range m.Entries {
		paths = append(paths, e.Path)
	}
	if !sameSet(paths, want) {
		t.Fatalf("manifest paths = %v\nwant %v", paths, want)
	}
	if len(m.Deferrals) != 1 || !reflect.DeepEqual(m.Deferrals[0], *res.Deferral) {
		t.Fatalf("manifest deferrals = %+v, want %+v", m.Deferrals, res.Deferral)
	}
}

func TestGenerateByteStable(t *testing.T) {
	root := acceptanceRepo(t)
	first := acceptanceRun(t, root, fixedNow, &recorder{})
	before := hashes(t, root, first.Written)
	rec := &recorder{}
	second := acceptanceRun(t, root, fixedNow.Add(time.Hour), rec)
	if len(second.Written) != 0 || len(second.Refused) != 0 || len(rec.calls) != 0 {
		t.Fatalf("a rerun over an unchanged tree did work: %+v sink %+v", second, rec.calls)
	}
	if !reflect.DeepEqual(second.Adopted, first.Written) {
		t.Fatalf("rerun Adopted = %v, want the first run's files %v", second.Adopted, first.Written)
	}
	if after := hashes(t, root, first.Written); !reflect.DeepEqual(before, after) {
		t.Fatalf("hashes changed on rerun:\nbefore %v\nafter  %v", before, after)
	}
}

// sameSet reports whether a and b hold the same strings, each once.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		if seen[s] != 1 {
			return false
		}
		seen[s]--
	}
	return true
}
