// Purpose: the clean-room contract of the local executor (R18 M6, R18b N6):
// a poisoned ambient HOME, go env file, GOCACHE and [ci.local] env keys are
// invisible to a run, and a run can never add a module to the shared module
// cache because the run has no proxy. Real git, real go, a file:// module
// proxy (no network).
//
// SPORT: internal.ci.cleanRoomEnv/TESTED (P1-CI-01).
package ci

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// runDirEnv is what OnEnvironment recorded for the last run.
type runDirEnv struct{ env Environment }

func envValues(env []string, name string) []string {
	var out []string
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			out = append(out, v)
		}
	}
	return out
}

func TestDefaultPopulateUsesIsolatedModuleCache(t *testing.T) {
	dir, cache := t.TempDir(), filepath.Join(t.TempDir(), "modules")
	env, err := cleanRoomEnv([]string{"PATH=" + os.Getenv("PATH")}, nil, t.TempDir(), cache)
	if err != nil {
		t.Fatal(err)
	}
	l := localExecutor{d: LocalExecutorDeps{ModCache: cache, Environ: env}}
	if err := l.populate(context.Background(), dir); err != nil {
		t.Fatalf("tree without go.mod: %v", err)
	}
	writeRepoFile(t, dir, "go.mod", "module example.test/standalone\n\ngo 1.26\n")
	if err := l.populate(context.Background(), dir); err != nil {
		t.Fatalf("download and verify dependency-free module: %v", err)
	}
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatalf("module cache was not created: %v, %v", info, err)
	}
	if got := listTree(t, cache); got != "" {
		t.Fatalf("dependency-free module populated unexpected files: %s", got)
	}
}

// poisonedHome builds the poisoned ambient: a HOME whose .gitconfig defines
// an alias and whose go env files set GOFLAGS and GOPROXY, and a GOCACHE
// holding a poisoned entry.
func poisonedHome(t *testing.T) (home, cache string) {
	t.Helper()
	home = t.TempDir()
	writeRepoFile(t, home, ".gitconfig", "[alias]\n\tpz = !echo POISONED-ALIAS\n")
	for _, p := range []string{".config/go/env", "Library/Application Support/go/env"} {
		writeRepoFile(t, home, p, "GOFLAGS=-poisoned-flag\nGOPROXY=https://poison.invalid\n")
	}
	cache = filepath.Join(t.TempDir(), "gocache")
	writeRepoFile(t, cache, "poison.txt", "poisoned cache entry")
	return home, cache
}

func assertCleanEnv(t *testing.T, rec Environment) {
	t.Helper()
	if rec.IsolationClass != "clean-checkout-same-uid" {
		t.Fatalf("IsolationClass = %q, want clean-checkout-same-uid", rec.IsolationClass)
	}
	want := map[string]string{"GOFLAGS": "-mod=readonly", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}
	for name, value := range want {
		if got := envValues(rec.Env, name); len(got) != 1 || got[0] != value {
			t.Fatalf("env %s = %v, want exactly [%s]", name, got, value)
		}
	}
	for _, name := range []string{"HOME", "TMPDIR", "GOCACHE"} {
		got := envValues(rec.Env, name)
		if len(got) != 1 || !strings.HasPrefix(got[0], rec.RunDir) {
			t.Fatalf("env %s = %v, want one per-run path under %s", name, got, rec.RunDir)
		}
	}
}

func assertCleanOutput(t *testing.T, out string, rec Environment, poison ...string) {
	t.Helper()
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the run wrote no output: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, "POISONED-ALIAS") || !strings.Contains(text, "not a git command") {
		t.Fatalf("the poisoned git alias leaked into the run (or git did not run):\n%s", text)
	}
	for _, line := range []string{"GOFLAGS=-mod=readonly", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local"} {
		if !strings.Contains(text, line+"\n") {
			t.Fatalf("go env did not report %q:\n%s", line, text)
		}
	}
	for _, p := range append(poison, "poison.txt") {
		if strings.Contains(text, p) {
			t.Fatalf("the run saw the poisoned %q:\n%s", p, text)
		}
	}
	if !strings.Contains(text, "GOCACHE="+envValues(rec.Env, "GOCACHE")[0]) {
		t.Fatalf("go env GOCACHE is not the per-run cache:\n%s", text)
	}
}

func TestLocalCleanRoomIgnoresPoisonedHome(t *testing.T) {
	skipWithoutPOSIXShell(t)
	r := newRig(t)
	poison, poisonCache := poisonedHome(t)
	out := filepath.Join(t.TempDir(), "run.out")
	cmd := fmt.Sprintf(`{ git pz; echo "GOFLAGS=$(go env GOFLAGS)"; echo "GOPROXY=$(go env GOPROXY)"; `+
		`echo "GOSUMDB=$(go env GOSUMDB)"; echo "GOTOOLCHAIN=$(go env GOTOOLCHAIN)"; echo "GOCACHE=$(go env GOCACHE)"; `+
		`echo "HOME=$HOME"; echo "LS<$(ls "$(go env GOCACHE)" | tr '\n' ' ')>"; } > '%s' 2>&1; true`, out)
	var rec runDirEnv
	ex, err := NewLocalSubJobExecutor(LocalExecutorDeps{
		CIDB: r.ciDB, Clock: newTestClock(), Exec: ShellExecutor{}, Commands: map[RequirementKind][]string{RequirementUnit: {cmd}},
		Environ: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + poison, "TMPDIR=" + t.TempDir(), "GOCACHE=" + poisonCache,
			"GOFLAGS=-poisoned-flag", "GOPROXY=https://poison.invalid", "GOSUMDB=sum.poison.invalid", "GOTOOLCHAIN=auto"},
		ExtraEnvKeys: []string{"GOPROXY", "GOFLAGS", "HOME", "GOTOOLCHAIN", "GOSUMDB"},
		RunRoot:      quietTemp(t), ModCache: filepath.Join(t.TempDir(), "mod"),
		Populate:      func(context.Context, string, []string) error { return nil },
		OnEnvironment: func(e Environment) { rec.env = e },
	})
	if err != nil {
		t.Fatalf("NewLocalSubJobExecutor: %v", err)
	}
	r.commit(map[string]string{"docs/a.md": "a"})
	sj := SubJob{Ref: r.ref, Kind: RequirementUnit, Snapshot: CandidateSnapshot{TreeHash: treeOf(t, r.repo, r.ref.CheckpointCommit), AttemptID: "clean"},
		Plan: CIRequirementPlan{Selection: TargetSelectionFull}}
	if res, err := ex.Run(context.Background(), sj); err != nil || !res.Passed {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	assertCleanEnv(t, rec.env)
	assertCleanOutput(t, out, rec.env, poison, poisonCache)
}

func writeProxyModule(t *testing.T, proxy string) {
	t.Helper()
	dir := filepath.Join(proxy, "example.test", "m", "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mod := "module example.test/m\n\ngo 1.21\n"
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"example.test/m@v1.0.0/go.mod": mod, "example.test/m@v1.0.0/m.go": "package m\n\nfunc M() int { return 1 }\n"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip: %v", err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	for name, content := range map[string][]byte{
		"list": []byte("v1.0.0\n"), "v1.0.0.info": []byte(`{"Version":"v1.0.0","Time":"2020-01-01T00:00:00Z"}`),
		"v1.0.0.mod": []byte(mod), "v1.0.0.zip": buf.Bytes(),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func goEnv(path, proxyURL, modcache, home string, extra ...string) []string {
	return append([]string{"PATH=" + path, "HOME=" + home, "GOCACHE=" + filepath.Join(home, "gocache"), "GOPROXY=" + proxyURL,
		"GOSUMDB=off", "GOTOOLCHAIN=local", "GOMODCACHE=" + modcache}, extra...)
}

// listTree lists every FILE under root except go's zero-byte `.lock` files.
// The go command creates empty directories and a lock under cache/download
// even for a module lookup it then refuses (measured with go1.26.6), so the
// packet's byte-identical listing is asserted over files that are not such
// locks; any module content at all fails it.
func listTree(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".lock") {
			if info, ierr := d.Info(); ierr != nil || info.Size() != 0 {
				t.Errorf("lock file %s is not a zero-byte lock", p)
			}
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	sort.Strings(paths)
	return strings.Join(paths, "\n")
}

// seedModuleProject commits a project that requires the proxy's module M
// (with a go.sum produced against the file proxy) and returns the files.
func seedModuleProject(t *testing.T, r *streamRig, proxyURL string) (dir string) {
	t.Helper()
	files := map[string]string{
		"go.mod":     "module example.test/app\n\ngo 1.21\n\nrequire example.test/m v1.0.0\n",
		"app/app.go": "package app\n\nimport \"example.test/m\"\n\nfunc App() int { return m.M() }\n",
	}
	seed := t.TempDir()
	for name, body := range files {
		writeRepoFile(t, seed, name, body)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir, tidy.Env = seed, goEnv(os.Getenv("PATH"), proxyURL, quietTemp(t), quietTemp(t), "GOFLAGS=-mod=mod")
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy against the file proxy: %v\n%s", err, out)
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		raw, err := os.ReadFile(filepath.Join(seed, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		files[name] = string(raw)
	}
	r.commit(files)
	return seed
}

// assertAmbientProxyDownloads is the positive control: the same build with
// the ambient proxy downloads M into a scratch module cache.
func assertAmbientProxyDownloads(t *testing.T, seed, proxyURL string) {
	t.Helper()
	scratch := quietTemp(t)
	build := exec.Command("go", "build", "./...")
	build.Dir, build.Env = seed, goEnv(os.Getenv("PATH"), proxyURL, scratch, quietTemp(t), "GOFLAGS=-mod=readonly")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("positive control build: %v\n%s", err, out)
	}
	if entries, err := os.ReadDir(filepath.Join(scratch, "cache", "download", "example.test", "m", "@v")); err != nil || len(entries) == 0 {
		t.Fatalf("positive control: M was not downloaded into the scratch cache (%v)", err)
	}
	if !strings.Contains(listTree(t, scratch), "v1.0.0.zip") {
		t.Fatal("positive control: the listing must see the downloaded module content")
	}
}

func TestRunCannotAddModuleToSharedCache(t *testing.T) {
	skipWithoutPOSIXShell(t)
	r := newRig(t)
	proxy := t.TempDir()
	writeProxyModule(t, proxy)
	proxyURL := "file://" + filepath.ToSlash(proxy)
	seed := seedModuleProject(t, r, proxyURL)
	assertAmbientProxyDownloads(t, seed, proxyURL)

	shared := filepath.Join(quietTemp(t), "mod")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	before := listTree(t, shared)
	out := filepath.Join(t.TempDir(), "build.out")
	ex, err := NewLocalSubJobExecutor(LocalExecutorDeps{
		CIDB: r.ciDB, Clock: newTestClock(), Exec: ShellExecutor{},
		Commands: map[RequirementKind][]string{RequirementCompile: {fmt.Sprintf("go build ./... > '%s' 2>&1", out)}},
		Environ:  goEnv(os.Getenv("PATH"), proxyURL, "", quietTemp(t)), RunRoot: quietTemp(t), ModCache: shared,
		// The operator even names GOPROXY under [ci.local] env: the forced value still wins.
		ExtraEnvKeys: []string{"GOPROXY"},
		Populate:     func(context.Context, string, []string) error { return nil }, // the controller populate step is a no-op
	})
	if err != nil {
		t.Fatalf("NewLocalSubJobExecutor: %v", err)
	}
	sj := SubJob{Ref: r.ref, Kind: RequirementCompile, Snapshot: CandidateSnapshot{TreeHash: treeOf(t, r.repo, r.ref.CheckpointCommit), AttemptID: "nocache"},
		Plan: CIRequirementPlan{Selection: TargetSelectionFull}}
	res, err := ex.Run(context.Background(), sj)
	if err != nil || res.Passed {
		t.Fatalf("Run = %+v, %v; want a failed run (module missing from the cache)", res, err)
	}
	raw, _ := os.ReadFile(out)
	if !strings.Contains(string(raw), "module lookup disabled by GOPROXY=off") {
		t.Fatalf("build output = %q, want the GOPROXY=off refusal", raw)
	}
	if after := listTree(t, shared); after != before {
		t.Fatalf("the shared cache changed during the run:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if got := listTree(t, filepath.Join(shared, "cache", "download", "example.test", "m")); strings.Contains(got, "v1.0.0") {
		t.Fatalf("cache/download/example.test/m holds module content:\n%s", got)
	}
}
