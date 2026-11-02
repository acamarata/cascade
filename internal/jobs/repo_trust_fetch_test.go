package jobs

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeCommitSeq numbers stand-in commits so every object id is unique.
var fakeCommitSeq atomic.Int64

// fakeGitCommonDir models internal/context.GitCommonDir over the stand-in
// layout: <root>/.git directory, a linked worktree's .git file and a bare
// directory whose config says bare = true. The walk stops at the ceiling.
func fakeGitCommonDir(_ context.Context, dir string) (scope.GitDirs, error) {
	cur, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return scope.GitDirs{}, cascade.Wrap(cascade.KindInvalidInput, err, "not a git repository")
	}
	ceiling, _ := filepath.EvalSymlinks(os.Getenv("GIT_CEILING_DIRECTORIES"))
	for cur != ceiling {
		dot := filepath.Join(cur, ".git")
		if st, err := os.Stat(dot); err == nil && st.IsDir() {
			return scope.GitDirs{CommonDir: dot, GitDir: dot, Toplevel: cur}, nil
		} else if err == nil {
			data, _ := os.ReadFile(dot)
			gitDir := strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir:"))
			common, _ := os.ReadFile(filepath.Join(gitDir, "commondir"))
			return scope.GitDirs{CommonDir: strings.TrimSpace(string(common)), GitDir: gitDir, Toplevel: cur}, nil
		}
		if data, err := os.ReadFile(filepath.Join(cur, "config")); err == nil && strings.Contains(string(data), "bare = true") {
			return scope.GitDirs{CommonDir: cur, GitDir: cur, Bare: true}, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return scope.GitDirs{}, cascade.New(cascade.KindInvalidInput, "not a git repository")
}

// fakeGit runs the git subcommands these tests use against the stand-in
// layout. Remote, fetch and update-ref change nothing the registry reads.
func fakeGit(t *testing.T, root string, args []string) string {
	t.Helper()
	for len(args) > 2 && args[0] == "-c" {
		args = args[2:]
	}
	switch args[0] {
	case "init":
		fakeGitInit(t, args[1:])
		return ""
	case "clone":
		fakeGitInit(t, args[2:])
		fakeWriteRefs(t, fakeDirs(t, args[2]).CommonDir, fakeReadRefs(t, fakeDirs(t, args[1]).CommonDir))
		return ""
	case "remote", "fetch", "update-ref":
		return ""
	}
	d := fakeDirs(t, root)
	refs := fakeReadRefs(t, d.CommonDir)
	head, err := os.ReadFile(filepath.Join(d.GitDir, "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	switch args[0] {
	case "commit":
		sum := sha1.Sum([]byte(fmt.Sprint(fakeCommitSeq.Add(1), root)))
		refs["refs/heads/"+string(head)] = hex.EncodeToString(sum[:])
	case "branch":
		refs["refs/heads/"+args[1]] = refs["refs/heads/"+string(head)]
	case "checkout":
		fakeWrite(t, filepath.Join(d.GitDir, "HEAD"), args[1])
	case "rev-parse":
		return refs["refs/heads/"+args[1]]
	case "worktree":
		gitDir := filepath.Join(d.CommonDir, "worktrees", filepath.Base(args[2]))
		fakeWrite(t, filepath.Join(gitDir, "HEAD"), args[3])
		fakeWrite(t, filepath.Join(gitDir, "commondir"), d.CommonDir)
		fakeWrite(t, filepath.Join(args[2], ".git"), "gitdir: "+gitDir)
	default:
		t.Fatalf("git stand-in does not model %v", args)
	}
	fakeWriteRefs(t, d.CommonDir, refs)
	return ""
}

func fakeGitInit(t *testing.T, args []string) {
	t.Helper()
	branch, bare, path := "main", false, args[len(args)-1]
	for i, a := range args {
		if a == "-b" {
			branch = args[i+1]
		}
		bare = bare || a == "--bare"
	}
	if bare {
		fakeWrite(t, filepath.Join(path, "config"), "[core]\n\tbare = true\n")
		fakeWrite(t, filepath.Join(path, "HEAD"), branch)
		return
	}
	fakeWrite(t, filepath.Join(path, ".git", "HEAD"), branch)
}

func fakeDirs(t *testing.T, root string) scope.GitDirs {
	t.Helper()
	d, err := fakeGitCommonDir(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func fakeReadRefs(t *testing.T, common string) map[string]string {
	t.Helper()
	refs := map[string]string{}
	data, err := os.ReadFile(filepath.Join(common, "fake-refs.json"))
	if err == nil {
		err = json.Unmarshal(data, &refs)
	}
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return refs
}

func fakeWriteRefs(t *testing.T, common string, refs map[string]string) {
	t.Helper()
	data, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	fakeWrite(t, filepath.Join(common, "fake-refs.json"), string(data))
}

func fakeWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertRegistryKind(t *testing.T, err error, kind cascade.Kind, message string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), message) || !cascade.HasKind(err, kind) {
		t.Fatalf("error = %v, want %s containing %q", err, kind, message)
	}
}

func TestRepoRegistryTrustFetchRecords(t *testing.T) {
	f := newRegistryFixture(t)
	registerFixture(t, f)
	ctx := context.Background()
	before, err := f.reg.TrustFetches(ctx, f.root)
	if err != nil || before == nil || len(before) != 0 {
		t.Fatalf("before %+v %v", before, err)
	}
	tips := registryTips(t, f.root)
	if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", f.root, tips); err != nil {
		t.Fatal(err)
	}
	want := []TrustFetch{{RemoteID: "origin", URL: f.root, FetchedAt: f.clock.Now(), Tips: tips}}
	got, err := f.reg.TrustFetches(ctx, f.root)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("record %+v %v", got, err)
	}
	registryGit(t, f.root, "remote", "add", "origin", f.root)
	registryGit(t, f.root, "fetch", "origin")
	registryGit(t, f.root, "remote", "set-url", "origin", filepath.Dir(f.root))
	got, err = f.reg.TrustFetches(ctx, f.root)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("user fetch changed trust %+v %v", got, err)
	}
	f.clock.Advance(time.Hour)
	if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "changed", tips); err != nil {
		t.Fatal(err)
	}
	got, err = f.reg.TrustFetches(ctx, f.root)
	if err != nil || len(got) != 1 || got[0].URL != "changed" || !got[0].FetchedAt.Equal(f.clock.Now()) {
		t.Fatalf("replace %+v %v", got, err)
	}
	for _, remote := range []string{"", "../x", "a/b"} {
		assertRegistryKind(t, f.reg.RecordTrustFetch(ctx, f.root, remote, "url", tips), cascade.KindInvalidInput, "remote")
	}
	assertRegistryKind(t, f.reg.RecordTrustFetch(ctx, f.root, "origin", "", tips), cascade.KindInvalidInput, "URL")
	if _, err := f.db.Exec(`DROP TABLE jobs_repo_trust_fetch`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reg.TrustFetches(ctx, f.root); err == nil || err == ErrRepoNotRegistered {
		t.Fatalf("read storage error %v", err)
	}
	if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "url", tips); err == nil || err == ErrRepoNotRegistered {
		t.Fatalf("write storage error %v", err)
	}
}

func TestRepoRegistryTrustFetchTips(t *testing.T) {
	f := newRegistryFixture(t)
	id := registerFixture(t, f)
	ctx := context.Background()
	tips := registryTips(t, f.root)
	if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "url", tips); err != nil {
		t.Fatal(err)
	}
	before, err := f.reg.TrustFetches(ctx, f.root)
	if err != nil || len(before) != 1 || !reflect.DeepEqual(before[0].Tips, tips) {
		t.Fatalf("tips %+v %v", before, err)
	}
	registryGit(t, f.root, "update-ref", "refs/cascade/trust/origin/main", tips[0].Commit)
	registryGit(t, f.root, "update-ref", "-d", "refs/cascade/trust/origin/main")
	after, err := f.reg.TrustFetches(ctx, f.root)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("refs affected record %+v %v", after, err)
	}
	t.Run("validation", testTrustFetchValidation)
	f.clock.Advance(time.Hour)
	if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "next", tips[1:]); err != nil {
		t.Fatal(err)
	}
	after, err = f.reg.TrustFetches(ctx, f.root)
	if err != nil || len(after) != 1 || !reflect.DeepEqual(after[0].Tips, tips[1:]) || !after[0].FetchedAt.Equal(f.clock.Now()) {
		t.Fatalf("replace %+v %v", after, err)
	}
	if _, err := f.db.Exec(`DELETE FROM jobs_repo_trust_fetch_tip WHERE repo_id=?`, id); err != nil {
		t.Fatal(err)
	}
	_, err = f.reg.TrustFetches(ctx, f.root)
	assertRegistryKind(t, err, cascade.KindIntegrity, "jobs_repo_trust_fetch_tip")
	if _, err := f.db.Exec(`INSERT INTO jobs_repo_trust_fetch_tip(repo_id,remote_id,ref,commit_id) VALUES(?,?,?,?)`, id, "origin", "refs/heads/bad", ""); err == nil {
		t.Fatal("empty commit passed table constraint")
	}
	if reflect.TypeOf(TrustFetch{}).NumField() != 4 {
		t.Fatal("trust fetch shape changed")
	}
	t.Run("rollback", testTrustFetchRollback)
}

func testTrustFetchValidation(t *testing.T) {
	f := newRegistryFixture(t)
	registerFixture(t, f)
	ctx := context.Background()
	good := BranchTip{"refs/heads/main", strings.Repeat("a", 40)}
	for _, bad := range [][]BranchTip{nil, {}, {{good.Ref, ""}}, {{good.Ref, "abc"}}, {{good.Ref, strings.Repeat("a", 41)}}, {{"", good.Commit}}, {{"refs/cascade/trust/x/main", good.Commit}}, {{"refs/heads/../x", good.Commit}}, {good, good}} {
		assertRegistryKind(t, f.reg.RecordTrustFetch(ctx, f.root, "origin", "url", bad), cascade.KindInvalidInput, "")
	}
	for _, table := range []string{"jobs_repo_trust_fetch", "jobs_repo_trust_fetch_tip"} {
		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s count %d %v", table, n, err)
		}
	}
	indexed := newRegistryFixture(t)
	if _, err := indexed.reg.graph.EnsureRepository(ctx, indexed.root, "", indexed.reg.git); err != nil {
		t.Fatal(err)
	}
	if err := indexed.reg.RecordTrustFetch(ctx, indexed.root, "origin", "url", []BranchTip{good}); err != ErrRepoNotRegistered {
		t.Fatalf("indexed write %v", err)
	}
}

func testTrustFetchRollback(t *testing.T) {
	f := newRegistryFixture(t)
	registerFixture(t, f)
	ctx := context.Background()
	tips := registryTips(t, f.root)
	if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "before", tips); err != nil {
		t.Fatal(err)
	}
	var stamp string
	if err := f.db.QueryRow(`SELECT fetched_at FROM jobs_repo_trust_fetch`).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`DROP TABLE jobs_repo_trust_fetch_tip`); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Hour)
	if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "after", tips); err == nil {
		t.Fatal("missing tip table accepted")
	}
	var url, after string
	if err := f.db.QueryRow(`SELECT url,fetched_at FROM jobs_repo_trust_fetch`).Scan(&url, &after); err != nil || url != "before" || stamp != after {
		t.Fatalf("parent changed %q %q %v", url, after, err)
	}
}
