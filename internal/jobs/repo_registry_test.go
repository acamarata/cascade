package jobs

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	contextgit "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

type registrySigner struct {
	key      ed25519.PrivateKey
	err      error
	wrong    bool
	messages [][]byte
}

type registryShortSigner struct{ *registrySigner }

func (registryShortSigner) PublicKey() ed25519.PublicKey { return make([]byte, 31) }

func (s *registrySigner) PublicKey() ed25519.PublicKey { return s.key.Public().(ed25519.PublicKey) }
func (s *registrySigner) Sign(_ context.Context, msg []byte) ([]byte, error) {
	s.messages = append(s.messages, append([]byte(nil), msg...))
	if s.err != nil {
		return nil, s.err
	}
	if s.wrong {
		return make([]byte, 64), nil
	}
	return ed25519.Sign(s.key, msg), nil
}

type registryFixture struct {
	reg    *RepoRegistry
	db     *sql.DB
	signer *registrySigner
	clock  *runtime.FixedClock
	root   string
}

func newRegistryFixture(t *testing.T) registryFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", home)
	db := openTestDB(t)
	clock := runtime.NewFixedClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err := ApplyRepoRegistrySchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	r, err := NewRepoRegistry(db, scope.NewGraphStore(db), registryGitDirs(), clock)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &registrySigner{key: key}
	r, err = r.WithTrustRecordSigner(s)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "repo")
	registryGit(t, home, "init", "-b", "main", root)
	registryGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "main")
	registryGit(t, root, "branch", "feature")
	registryGit(t, root, "checkout", "feature")
	registryGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "feature")
	registryGit(t, root, "checkout", "main")
	return registryFixture{r, db, s, clock, root}
}

// registryGit runs real git when it can start, else the file-backed
// stand-in (the sandbox may refuse to exec git at all).
func registryGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	if !registryRealGit() {
		return fakeGit(t, root, args)
	}
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_DIR=") && !strings.HasPrefix(value, "GIT_WORK_TREE=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// registryRealGit reports whether a git binary can start here.
var registryRealGit = sync.OnceValue(func() bool { return exec.Command("git", "--version").Run() == nil })

// registryGitDirs is the production probe when git runs, else the stand-in.
func registryGitDirs() scope.GitCommonDirFunc {
	if registryRealGit() {
		return contextgit.GitCommonDir
	}
	return fakeGitCommonDir
}

func registryTips(t *testing.T, root string) []BranchTip {
	t.Helper()
	return []BranchTip{{"refs/heads/feature", registryGit(t, root, "rev-parse", "feature")}, {"refs/heads/main", registryGit(t, root, "rev-parse", "main")}}
}

func fixedRegistryTips(tips []BranchTip) BranchTipsFunc {
	return func(context.Context, string) ([]BranchTip, error) { return tips, nil }
}

func registerFixture(t *testing.T, f registryFixture) string {
	t.Helper()
	id, err := f.reg.Register(context.Background(), f.root, "", fixedRegistryTips(registryTips(t, f.root)))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRepoRegistryUsesRepoIdentity(t *testing.T) {
	f := newRegistryFixture(t)
	ctx := context.Background()
	rec, err := f.reg.graph.EnsureRepository(ctx, f.root, "", f.reg.git)
	if err != nil {
		t.Fatal(err)
	}
	id := registerFixture(t, f)
	if id != rec.ID {
		t.Fatalf("identity %q != %q", id, rec.ID)
	}
	link := filepath.Join(filepath.Dir(f.root), "link")
	if err := os.Symlink(f.root, link); err != nil {
		t.Fatal(err)
	}
	if got, err := f.reg.Register(ctx, link, "", fixedRegistryTips(registryTips(t, f.root))); err != nil || got != id {
		t.Fatalf("symlink = %q %v", got, err)
	}
	clone := filepath.Join(filepath.Dir(f.root), "clone")
	registryGit(t, filepath.Dir(f.root), "clone", f.root, clone)
	other, err := f.reg.Register(ctx, clone, "", fixedRegistryTips(nil))
	if err != nil || other == id {
		t.Fatalf("clone identity %q %v", other, err)
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='jobs_repo_registry'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("parallel registry %d %v", count, err)
	}
	unregistered := filepath.Join(filepath.Dir(f.root), "new")
	registryGit(t, filepath.Dir(f.root), "init", unregistered)
	if _, err := f.reg.ByPath(ctx, unregistered); err != ErrRepoNotRegistered {
		t.Fatalf("unregistered = %v", err)
	}
}

func TestRepoRegistryCanonicalPath(t *testing.T) {
	f := newRegistryFixture(t)
	id := registerFixture(t, f)
	ctx := context.Background()
	work := filepath.Join(filepath.Dir(f.root), "work")
	registryGit(t, f.root, "worktree", "add", work, "feature")
	if got, err := f.reg.ByPath(ctx, work); err != nil || got != id {
		t.Fatalf("worktree = %s %v", got, err)
	}
	calls := 0
	git := f.reg.git
	f.reg.git = func(ctx context.Context, p string) (scope.GitDirs, error) { calls++; return git(ctx, p) }
	if err := f.reg.BindGate(ctx, work, id); err != nil {
		t.Fatal(err)
	}
	if got, err := f.reg.GateRepoID(ctx, work); err != nil || got != id {
		t.Fatalf("gate %s %v", got, err)
	}
	if calls != 2 {
		t.Fatalf("git calls = %d", calls)
	}
	var path string
	if err := f.db.QueryRow(`SELECT gate_path FROM jobs_repo_gate`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(f.root, ".git"))
	if err != nil || path != want {
		t.Fatalf("gate_path %q want %q: %v", path, want, err)
	}
	if err := f.reg.BindGate(ctx, work, "absent"); err != ErrRepoNotRegistered {
		t.Fatalf("absent id = %v", err)
	}
	for _, name := range []string{"A.git", "B.git"} {
		p := filepath.Join(filepath.Dir(f.root), name)
		registryGit(t, filepath.Dir(f.root), "init", "--bare", p)
		bid, err := f.reg.Register(ctx, p, "", fixedRegistryTips(nil))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.reg.BindGate(ctx, p, bid); err != nil {
			t.Fatal(err)
		}
		if got, err := f.reg.GateRepoID(ctx, p); err != nil || got != bid {
			t.Fatalf("bare gate %q %v", got, err)
		}
		if err := f.reg.BindGate(ctx, p, id); err == nil {
			t.Fatal("rebind accepted")
		}
	}
}

func TestRepoRegistryOperatorRegistered(t *testing.T) {
	f := newRegistryFixture(t)
	ctx := context.Background()
	rec, err := f.reg.graph.EnsureRepository(ctx, f.root, "", f.reg.git)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := f.reg.Registration(ctx, f.root)
	if err != nil || reg.RepoID != rec.ID || reg.OperatorRegistered || len(reg.Baseline) != 0 {
		t.Fatalf("indexed registration %+v %v", reg, err)
	}
	if _, err := f.reg.OperatorRegistered(ctx, f.root); err != ErrRepoNotRegistered {
		t.Fatalf("indexed trusted: %v", err)
	}
	tips := registryTips(t, f.root)
	registerFixture(t, f)
	reg, err = f.reg.Registration(ctx, f.root)
	if err != nil || !reg.OperatorRegistered || !reflect.DeepEqual(reg.Baseline, tips) || !reg.RegisteredAt.Equal(f.clock.Now()) {
		t.Fatalf("registration %+v %v", reg, err)
	}
	registryGit(t, f.root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "next")
	registerFixture(t, f)
	again, err := f.reg.Registration(ctx, f.root)
	if err != nil || !reflect.DeepEqual(reg, again) {
		t.Fatalf("registration changed %+v %v", again, err)
	}
	t.Run("refusals", testRegistrationRefusals)
	if _, err := f.db.Exec(`DROP TABLE jobs_repo_registration`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reg.OperatorRegistered(ctx, f.root); err == nil || err == ErrRepoNotRegistered {
		t.Fatalf("missing table = %v", err)
	}
}

func testRegistrationRefusals(t *testing.T) {
	f := newRegistryFixture(t)
	ctx := context.Background()
	failure := cascade.New(cascade.KindUnavailable, "tips unavailable")
	if _, err := f.reg.Register(ctx, f.root, "", func(context.Context, string) ([]BranchTip, error) { return nil, failure }); err != failure {
		t.Fatalf("tips failure %v", err)
	}
	for _, tips := range []BranchTipsFunc{nil, fixedRegistryTips([]BranchTip{{"refs/tags/main", strings.Repeat("a", 40)}}), fixedRegistryTips([]BranchTip{{"refs/heads/main", "abc"}})} {
		if _, err := f.reg.Register(ctx, f.root, "", tips); err == nil {
			t.Fatal("bad tips accepted")
		}
	}
	for _, table := range []string{"jobs_repo_registration", "jobs_repo_baseline"} {
		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s count %d %v", table, n, err)
		}
	}
}
