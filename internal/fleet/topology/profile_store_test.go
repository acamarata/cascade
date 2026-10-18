package topology

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

func seedEndpointProfile(t *testing.T, s *Store, id RuntimeProfileID, kind RuntimeKind, persistent bool) {
	t.Helper()
	rec := RuntimeProfile{ID: id, Runtime: kind, Persistent: persistent, ConfigHome: "seed-home", EnvVar: "SEED_VAR"}
	if err := s.UpsertRuntimeProfile(context.Background(), rec); err != nil {
		t.Fatalf("seed profile %q: %v", id, err)
	}
}

// makeHome creates the profile home directly (EnsureHome refuses to create on
// windows) so store tests have a real directory to resolve.
func makeHome(t *testing.T, root string, id RuntimeProfileID) string {
	t.Helper()
	home, err := HomePath(root, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

type endpointCase struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	WantHost  string `json:"want_host"`
	WantPort  int    `json:"want_port"`
	WantLocal bool   `json:"want_local"`
}

func loadEndpointGolden(t *testing.T) []endpointCase {
	t.Helper()
	data, err := os.ReadFile("testdata/profile_endpoints.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var cases []endpointCase
	if err := json.Unmarshal(data, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("golden: %v (%d cases)", err, len(cases))
	}
	return cases
}

// reopen closes db and opens the same sqlite file again.
func reopen(t *testing.T, dir string, db *sql.DB) *Store {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	again, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "topology.db")+"?_journal_mode=WAL&_foreign_keys=1")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
	return NewStore(again)
}

func newReopenableStore(t *testing.T) (*Store, *sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "topology.db")+"?_journal_mode=WAL&_foreign_keys=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewStore(db), db, dir
}

func TestSetEndpointPersistentOnly(t *testing.T) {
	ctx := context.Background()
	isolatedRoot(t)
	for _, c := range loadEndpointGolden(t) {
		t.Run(c.Name, func(t *testing.T) {
			s, db, dir := newReopenableStore(t)
			seedEndpointProfile(t, s, "pers", RuntimeOllama, true)
			if err := s.SetEndpoint(ctx, "pers", Endpoint{Host: c.Host, Port: c.Port}); err != nil {
				t.Fatalf("SetEndpoint: %v", err)
			}
			got, err := reopen(t, dir, db).GetRuntimeProfile(ctx, "pers")
			if err != nil {
				t.Fatalf("Get after reopen: %v", err)
			}
			if got.Endpoint.Host != c.WantHost || got.Endpoint.Port != c.WantPort {
				t.Errorf("stored endpoint = %+v, golden wants %s:%d", got.Endpoint, c.WantHost, c.WantPort)
			}
			if got.EnvVar != "SEED_VAR" || got.ConfigHome != "seed-home" || !got.Persistent {
				t.Errorf("SetEndpoint touched other columns: %+v", got)
			}
			if EndpointIsLocal(got) != c.WantLocal {
				t.Errorf("EndpointIsLocal = %v, golden wants %v", !c.WantLocal, c.WantLocal)
			}
		})
	}
	t.Run("refusals", setEndpointRefusals)
	t.Run("closed_db", setEndpointClosedDB)
}

// setEndpointRefusals is the refusal half of TestSetEndpointPersistentOnly.
func setEndpointRefusals(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	s := newTestStore(t)
	seedEndpointProfile(t, s, "pers", RuntimeAPI, true)
	seedEndpointProfile(t, s, "spawn", RuntimeClaudeCLI, false)
	ep := Endpoint{Host: "127.0.0.1", Port: 9}

	before, _ := s.GetRuntimeProfile(ctx, "spawn")
	requireSentinel(t, s.SetEndpoint(ctx, "spawn", ep), ErrTopologyInvariant, "spawn")
	after, _ := s.GetRuntimeProfile(ctx, "spawn")
	if before != after || after.Endpoint != (Endpoint{}) {
		t.Errorf("non-persistent row changed: %+v -> %+v", before, after)
	}
	requireSentinel(t, s.SetEndpoint(ctx, "ghost", ep), ErrTopologyNotFound, "ghost")

	bad := []Endpoint{
		{Host: "127.0.0.1"}, {Host: "", Port: 80}, {Host: "unix:/x", Port: 1},
		{Host: "h", Port: -1}, {Host: "h", Port: 65536}, {Host: "a b", Port: 1}, {Host: "a\x00b", Port: 1},
		{Host: "unix:"}, {Host: "unix:x.sock"}, {Host: "unix:evil.com:443"}, {Host: "unix://evil.com/x"},
		{Host: "127.0.0.1:99", Port: 1}, {Host: "evil.com:443", Port: 1}, {Host: "1:2", Port: 1}, {Host: "::1:zz", Port: 1},
		{Host: "localhost/x", Port: 1}, {Host: "127.0.0.1#x", Port: 1}, {Host: "127.0.0.1@evil.com", Port: 1}, {Host: "a?b", Port: 1},
	}
	for _, e := range bad {
		requireSentinel(t, s.SetEndpoint(ctx, "pers", e), ErrTopologyInvariant, "pers")
	}
	if got, _ := s.GetRuntimeProfile(ctx, "pers"); got.Endpoint != (Endpoint{}) {
		t.Errorf("a refused endpoint was stored: %+v", got.Endpoint)
	}
}

// setEndpointClosedDB is the database-failure half of TestSetEndpointPersistentOnly.
func setEndpointClosedDB(t *testing.T) {
	t.Helper()
	s, db, _ := newReopenableStore(t)
	seedEndpointProfile(t, s, "pers", RuntimeAPI, true)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	err := s.SetEndpoint(context.Background(), "pers", Endpoint{Host: "127.0.0.1", Port: 9})
	if err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("closed DB: err = %v, want KindUnavailable", err)
	}
	err = s.SetRuntimeConfig(context.Background(), "pers", "X_HOME", filepath.Join(t.TempDir(), "profiles", "pers"), t.TempDir())
	if err == nil {
		t.Error("closed DB or outside path must fail")
	}
}

func TestSetRuntimeConfigInsideHome(t *testing.T) {
	ctx := context.Background()
	root := isolatedRoot(t)
	s := newTestStore(t)
	seedEndpointProfile(t, s, "acct-1", RuntimeClaudeCLI, true)
	home := makeHome(t, root, "acct-1")
	inside := filepath.Join(home, "cfg", "nested")
	if err := s.SetRuntimeConfig(ctx, "acct-1", "RUNTIME_CONFIG_HOME", inside, root); err != nil {
		t.Fatalf("inside home: %v", err)
	}
	got, _ := s.GetRuntimeProfile(ctx, "acct-1")
	if got.EnvVar != "RUNTIME_CONFIG_HOME" || got.ConfigHome != inside || !got.Persistent || got.Endpoint != (Endpoint{}) {
		t.Errorf("stored profile = %+v", got)
	}
	if err := s.SetRuntimeConfig(ctx, "acct-1", "RUNTIME_CONFIG_HOME", home, root); err != nil {
		t.Errorf("the home itself must be accepted: %v", err)
	}
	ghostCfg := filepath.Join(makeHome(t, root, "ghost"), "cfg")
	requireSentinel(t, s.SetRuntimeConfig(ctx, "ghost", "X_HOME", ghostCfg, root), ErrTopologyNotFound, "ghost")
	t.Run("refusals", setRuntimeConfigRefusals)
	t.Run("home_links", setRuntimeConfigHomeLinks)
	t.Run("dangling_links", setRuntimeConfigDanglingLinks)
	t.Run("home_kinds", setRuntimeConfigHomeKinds)
}

// setRuntimeConfigRefusals is the refusal half of TestSetRuntimeConfigInsideHome.
func setRuntimeConfigRefusals(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	root := isolatedRoot(t)
	s := newTestStore(t)
	seedEndpointProfile(t, s, "acct-1", RuntimeClaudeCLI, true)
	home := makeHome(t, root, "acct-1")
	other := makeHome(t, root, "acct-2")
	elsewhere := t.TempDir()
	linkOut := filepath.Join(home, "escape")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(elsewhere, linkOut); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name, env, cfg string
	}{
		{"absolute elsewhere", "X_HOME", elsewhere},
		{"sibling profile", "X_HOME", filepath.Join(other, "cfg")},
		{"prefix sibling", "X_HOME", home + "-evil"},
		{"dotdot escape", "X_HOME", filepath.Join(home, "..", "acct-2", "cfg")},
		{"symlink out", "X_HOME", filepath.Join(linkOut, "cfg")},
		{"relative", "X_HOME", "cfg"},
		{"empty config", "X_HOME", ""},
		{"empty env", "", filepath.Join(home, "cfg")},
		{"env with equals", "X=1", filepath.Join(home, "cfg")},
		{"env with space", "X HOME", filepath.Join(home, "cfg")},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases[:4], cases[5:]...)
	}
	before, _ := s.GetRuntimeProfile(ctx, "acct-1")
	for _, c := range cases {
		requireSentinel(t, s.SetRuntimeConfig(ctx, "acct-1", c.env, c.cfg, root), ErrTopologyInvariant, "acct-1")
		if now, _ := s.GetRuntimeProfile(ctx, "acct-1"); now != before {
			t.Errorf("%s: row changed %+v -> %+v", c.name, before, now)
		}
	}
	requireSentinel(t, s.SetRuntimeConfig(ctx, "Bad/ID", "X_HOME", home, root), ErrTopologyInvariant, "Bad/ID")
}

func TestResolveExistingErrors(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if _, err := resolveExisting(filepath.Join(file, "child")); err == nil {
			t.Error("a path below a regular file must not resolve as absence")
		}
	}
	got, err := resolveExisting(filepath.Join(root, "a", "b"))
	want, _ := filepath.EvalSymlinks(root)
	if err != nil || got != filepath.Join(want, "a", "b") {
		t.Errorf("resolveExisting(missing tail) = %q, %v; want %q", got, err, filepath.Join(want, "a", "b"))
	}
}
