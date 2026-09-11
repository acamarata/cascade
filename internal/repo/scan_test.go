package repo

// This file, per internal/build/egress_scan.go's own documented
// exemption ("Test files are excluded on purpose"), is the one place in
// this package allowed to import os/exec: it supplies scan.go's injected
// GitRootFunc/RemoteURLFunc seams with REAL implementations run against a
// REAL `git` binary over a t.TempDir() repository (Art.2), exactly
// mirroring internal/context/discover.go's gitRoot and this ticket's own
// acceptance criterion.

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
)

func realGitRoot(ctx context.Context, cwd string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return cwd
	}
	return filepath.Clean(filepath.FromSlash(strings.TrimSpace(string(out))))
}

func realRemoteURL(ctx context.Context, root string) string {
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// newRealGitRepo runs the actual git binary to init a repository with one
// commit and an origin remote, under t.TempDir().
func newRealGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("remote", "add", "origin", "https://example.com/fixture.git")
	writeFile(t, dir, "go.mod", "module example.com/fixture\n\ngo 1.22\n")
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir
}

type fixedScanClock struct{ v int64 }

func (c fixedScanClock) Now() int64 { return c.v }

func newScanDeps(t *testing.T) (ScanDeps, *scope.GraphStore) {
	t.Helper()
	db := openTestDB(t)
	graph := scope.NewGraphStore(db)
	return ScanDeps{
		Store:     NewStore(db),
		Graph:     graph,
		GitRoot:   realGitRoot,
		RemoteURL: realRemoteURL,
		Clock:     fixedScanClock{v: 1700000000},
	}, graph
}

func TestScanRealGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := newRealGitRepo(t)
	deps, _ := newScanDeps(t)

	inv, err := Scan(context.Background(), deps, dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if inv.Repository.Remote != "https://example.com/fixture.git" {
		t.Errorf("Remote = %q, want the real git remote", inv.Repository.Remote)
	}
	if len(inv.Languages) != 1 || inv.Languages[0].Language != LanguageGo {
		t.Fatalf("Languages = %+v, want exactly [go]", inv.Languages)
	}

	stored, ok, err := deps.Store.Get(context.Background(), inv.Repository.ID)
	if err != nil || !ok {
		t.Fatalf("Get after Scan: ok=%v err=%v", ok, err)
	}
	if stored.Repository.ID != inv.Repository.ID {
		t.Errorf("stored id = %q, want %q", stored.Repository.ID, inv.Repository.ID)
	}
}

// TestScanDeterminism proves two scans of an unchanged tree produce
// identical inventories (this ticket's determinism acceptance criterion).
func TestScanDeterminism(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := newRealGitRepo(t)
	deps, _ := newScanDeps(t)
	ctx := context.Background()

	first, err := Scan(ctx, deps, dir)
	if err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	second, err := Scan(ctx, deps, dir)
	if err != nil {
		t.Fatalf("second Scan: %v", err)
	}
	if first.Repository.ID != second.Repository.ID {
		t.Errorf("repository id changed across scans: %q vs %q", first.Repository.ID, second.Repository.ID)
	}
	if !reflect.DeepEqual(first.Languages, second.Languages) {
		t.Errorf("Languages differ across scans: %+v vs %+v", first.Languages, second.Languages)
	}
	if !reflect.DeepEqual(first.Layout, second.Layout) {
		t.Errorf("Layout differs across scans: %+v vs %+v", first.Layout, second.Layout)
	}
}

func TestResolveRepositoryGraphError(t *testing.T) {
	db := openTestDB(t)
	graph := scope.NewGraphStore(db)
	_ = db.Close()
	deps := ScanDeps{Store: NewStore(db), Graph: graph, GitRoot: realGitRoot, RemoteURL: realRemoteURL, Clock: fixedScanClock{}}
	if _, err := resolveRepository(context.Background(), deps, "/tmp/x"); err == nil {
		t.Fatal("resolveRepository against a closed db: want error, got nil")
	}
}

func TestScanRequiresDeps(t *testing.T) {
	if _, err := Scan(context.Background(), ScanDeps{}, "/tmp"); err == nil {
		t.Fatal("Scan with zero ScanDeps: want error, got nil")
	}
}

func TestValidateScanDepsEachField(t *testing.T) {
	db := openTestDB(t)
	full := ScanDeps{
		Store: NewStore(db), Graph: scope.NewGraphStore(db),
		GitRoot: realGitRoot, RemoteURL: realRemoteURL, Clock: fixedScanClock{},
	}
	cases := []struct {
		name string
		deps ScanDeps
	}{
		{"nilStore", func() ScanDeps { d := full; d.Store = nil; return d }()},
		{"nilGraph", func() ScanDeps { d := full; d.Graph = nil; return d }()},
		{"nilGitRoot", func() ScanDeps { d := full; d.GitRoot = nil; return d }()},
		{"nilRemoteURL", func() ScanDeps { d := full; d.RemoteURL = nil; return d }()},
		{"nilClock", func() ScanDeps { d := full; d.Clock = nil; return d }()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateScanDeps(c.deps); err == nil {
				t.Fatalf("validateScanDeps(%s): want error, got nil", c.name)
			}
		})
	}
}

func TestScanMalformedManifestPropagates(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	writeFile(t, dir, "go.mod", "not { valid")
	run("add", ".")
	run("commit", "-q", "-m", "x")

	deps, _ := newScanDeps(t)
	if _, err := Scan(context.Background(), deps, dir); err == nil {
		t.Fatal("Scan over a repo with a malformed go.mod: want error, got nil")
	}
}

func TestResolveMembershipUnresolvedRepo(t *testing.T) {
	deps, graph := newScanDeps(t)
	if err := graph.PutRepository(context.Background(), scope.RepositoryRecord{ID: "solo", Remote: "", PathHash: "h"}); err != nil {
		t.Fatal(err)
	}
	members, err := resolveMembership(context.Background(), deps, RepositoryRef{ID: "solo"})
	if err != nil {
		t.Fatalf("resolveMembership: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("members = %v, want empty for a repository with no scope-graph parents", members)
	}
}

func TestHashPathAndRepositoryIDDeterministic(t *testing.T) {
	first := hashPath("/a/b")
	second := hashPath("/a/b")
	if first != second {
		t.Error("hashPath is not deterministic")
	}
	idFirst := repositoryID("r", "h")
	idSecond := repositoryID("r", "h")
	if idFirst != idSecond {
		t.Error("repositoryID is not deterministic")
	}
	if repositoryID("r1", "h") == repositoryID("r2", "h") {
		t.Error("repositoryID collided for different remotes")
	}
}
