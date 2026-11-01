package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/retrieval/embedsel"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// recordingDoor is a conductor door that records every Embed it receives
// and refuses the tiers the real door refuses.
type recordingDoor struct {
	mu    sync.Mutex
	tiers []provider.SensitivityTier
	lanes []string
	dims  int
}

func (r *recordingDoor) Embed(_ context.Context, lane, _ string, tier provider.SensitivityTier, inputs []string) ([][]float32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tiers, r.lanes = append(r.tiers, tier), append(r.lanes, lane)
	if tier != provider.SensitivityInternal && tier != provider.SensitivityPublic {
		return nil, cascade.New(cascade.KindPolicyDenied, "recording door: tier refused")
	}
	out := make([][]float32, len(inputs))
	for i := range out {
		out[i] = make([]float32, r.dims)
	}
	return out, nil
}

func (r *recordingDoor) calls() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.tiers) }

func embedTestPaths(t *testing.T) runtime.PathProvider { return doctorTestPaths(t) }

func loadEmbedConfig(t *testing.T, paths runtime.PathProvider, body string) *runtime.Config {
	t.Helper()
	if os.MkdirAll(filepath.Dir(paths.ConfigPath()), 0o700) != nil || os.WriteFile(paths.ConfigPath(), []byte(body), 0o600) != nil {
		t.Fatal("writing config.toml")
	}
	cfg, err := runtime.Load(context.Background(), runtime.LoadOptions{Path: paths.ConfigPath(),
		Getenv: func(string) string { return "" }, Environ: func() []string { return nil }})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func admittedSocket(t *testing.T, path string) runtime.EffectiveConfig {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"local_socket": path})
	return runtime.EffectiveConfig{Families: map[string]json.RawMessage{"retrieval.embedding": raw}}
}

// socketFixture supplies metadata without requiring a network listener.
func socketFixture(t *testing.T, path string) {
	t.Helper()
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		t.Fatal("mkdir")
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

type socketFileInfo struct{ os.FileInfo }

func (s socketFileInfo) Mode() os.FileMode { return os.ModeSocket | s.FileInfo.Mode().Perm() }

func socketLstat(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err == nil && info.Mode().IsRegular() && strings.HasSuffix(path, ".sock") {
		return socketFileInfo{info}, nil
	}
	return info, err
}

func testDeps(paths runtime.PathProvider, door laneDoor) embedderDeps {
	engine := func(r *egress.Registry) (*egress.Engine, error) {
		fw, err := bindDaemonFirewall(paths) // own the binding, as a daemon does
		if err != nil {
			return nil, err
		}
		return fw.Engine(r)
	}
	return embedderDeps{door: door, uid: os.Getuid(), goos: goruntime.GOOS, engine: engine, lstat: socketLstat}
}

const laneBody = "[retrieval.embedding]\nlane = \"emb\"\nmodel = \"m\"\ndimensions = 3\n"

func wantKind(t *testing.T, err error, kind cascade.Kind, substr string) {
	t.Helper()
	if k, _ := cascade.KindOf(err); err == nil || k != kind || !strings.Contains(err.Error(), substr) {
		t.Fatalf("err = %v (kind %s), want kind %s naming %q", err, k, kind, substr)
	}
}

func embedOne(ctx context.Context, sel *embedsel.Selection) error {
	_, err := sel.Embedder().Embed(ctx, []provider.EmbedInput{{Text: "hello"}})
	return err
}

func TestComposeEmbedderPrefersLocalSidecarSocket(t *testing.T) {
	paths := embedTestPaths(t)
	sock := filepath.Join(t.TempDir(), "s", "bgem3.sock")
	socketFixture(t, sock)
	sel := composeEmbedderWith(loadEmbedConfig(t, paths, laneBody), admittedSocket(t, sock), testDeps(paths, &recordingDoor{}))
	if sel.Err() != nil || sel.Source() != embedsel.SourceLocalSidecar || sel.Model() != "bge-m3" || sel.Dimensions() != 1024 {
		t.Fatalf("selection = %s %s/%d err %v, want local-sidecar bge-m3/1024", sel.Source(), sel.Model(), sel.Dimensions(), sel.Err())
	}
	if !sel.Admits(provider.SensitivityLocalOnly) || !sel.Admits(provider.SensitivityRestricted) || !sel.Admits(provider.SensitivityInternal) {
		t.Fatal("local sidecar must admit every tier")
	}
}

func TestComposeEmbedderConductorLane(t *testing.T) {
	paths := embedTestPaths(t)
	door := &recordingDoor{dims: 3}
	sel := composeEmbedderWith(loadEmbedConfig(t, paths, laneBody), runtime.EffectiveConfig{}, testDeps(paths, door))
	if sel.Err() != nil || sel.Source() != embedsel.SourceConductorLane || sel.Model() != "m" || sel.Dimensions() != 3 {
		t.Fatalf("selection = %s %s/%d err %v", sel.Source(), sel.Model(), sel.Dimensions(), sel.Err())
	}
	if !sel.Admits(provider.SensitivityInternal) || !sel.Admits(provider.SensitivityPublic) ||
		sel.Admits(provider.SensitivityRestricted) || sel.Admits(provider.SensitivityLocalOnly) {
		t.Fatal("a lane must admit internal and public only")
	}
	ctx := conductor.ContextWithDoorSensitivity(context.Background(), provider.SensitivityInternal)
	vecs, err := sel.FusionEmbedder().Embed(ctx, []string{"hello"})
	if err != nil || len(vecs) != 1 || len(vecs[0]) != 3 {
		t.Fatalf("Embed = %v, %v", vecs, err)
	}
	if door.calls() != 1 || door.tiers[0] != provider.SensitivityInternal || door.lanes[0] != "emb" {
		t.Fatalf("door saw tiers %v lanes %v, want one internal call on emb", door.tiers, door.lanes)
	}
	want := "embedding: conductor-lane emb m/3 (internal and public content only)"
	if line := embeddingStatusLine(sel, "emb"); line != want {
		t.Fatalf("status = %q, want %q", line, want)
	}
}

func TestComposeEmbedderNotConfigured(t *testing.T) {
	paths := embedTestPaths(t)
	door := &recordingDoor{dims: 3}
	sel := composeEmbedderWith(loadEmbedConfig(t, paths, ""), runtime.EffectiveConfig{}, testDeps(paths, door))
	if sel.Source() != embedsel.SourceNotConfigured || sel.Configured() {
		t.Fatalf("source = %s, want not-configured", sel.Source())
	}
	err := embedOne(context.Background(), sel)
	if err != sel.Err() {
		t.Fatalf("Embed err %v is not the selection's err %v", err, sel.Err())
	}
	wantKind(t, err, cascade.KindUnavailable, "local_socket is unset")
	wantKind(t, err, cascade.KindUnavailable, "lane is unset")
	if door.calls() != 0 || embeddingHealth(sel) != nil {
		t.Fatalf("door calls %d, health %v", door.calls(), embeddingHealth(sel))
	}
}

func TestComposeEmbedderNoRuntimeFallback(t *testing.T) {
	paths := embedTestPaths(t)
	sock := filepath.Join(t.TempDir(), "s", "bgem3.sock")
	socketFixture(t, sock)
	door := &recordingDoor{dims: 3}
	cfg := loadEmbedConfig(t, paths, laneBody)
	sel := composeEmbedderWith(cfg, admittedSocket(t, sock), testDeps(paths, door))
	if sel.Source() != embedsel.SourceLocalSidecar || sel.Err() != nil {
		t.Fatalf("source = %s, err = %v, want usable local-sidecar", sel.Source(), sel.Err())
	}
	ctx := conductor.ContextWithDoorSensitivity(context.Background(), provider.SensitivityInternal)
	if err := embedOne(ctx, sel); err == nil {
		t.Fatal("a dead sidecar embedded")
	}
	missing := composeEmbedderWith(cfg, admittedSocket(t, sock+".gone"), testDeps(paths, door))
	wantKind(t, embedOne(ctx, missing), cascade.KindPolicyDenied, "refused")
	if door.calls() != 0 {
		t.Fatalf("a failing sidecar was re-routed to the lane: %d door calls", door.calls())
	}
}

func TestDaemonEmbedderSharedInstance(t *testing.T) {
	paths := embedTestPaths(t)
	loadEmbedConfig(t, paths, laneBody)
	first := daemonEmbedderFor(paths, runtime.EffectiveConfig{})
	if first.Source() != embedsel.SourceConductorLane {
		t.Fatalf("source = %s", first.Source())
	}
	doctorSel, _ := doctorEmbedder(context.Background(), paths)
	for name, got := range map[string]*embedsel.Selection{
		"vector leg / recall.what / recall.index": processEmbedder(paths),
		"doctor": doctorSel,
	} {
		if got != first {
			t.Fatalf("%s holds a different Selection", name)
		}
	}
	if other := processEmbedder(embedTestPaths(t)); other == first {
		t.Fatal("two config paths share one Selection")
	}
}

func TestDoorEmbedExecutorNoTierRefused(t *testing.T) {
	door := &recordingDoor{dims: 3}
	exec := doorEmbedExecutor{door: door, lane: "emb"}
	req := provider.ModelEmbedRequest{Inputs: []string{"x"}, Model: "m"}
	for _, ctx := range []context.Context{context.Background(),
		conductor.ContextWithDoorSensitivity(context.Background(), provider.SensitivityRestricted)} {
		_, err := exec.Embed(ctx, req)
		wantKind(t, err, cascade.KindPolicyDenied, "internal and public content only")
	}
	if door.calls() != 0 {
		t.Fatalf("an unstamped or restricted call reached the door: %v", door.tiers)
	}
	if _, err := exec.Embed(conductor.ContextWithDoorSensitivity(context.Background(), provider.SensitivityPublic), req); err != nil ||
		door.calls() != 1 || door.tiers[0] != provider.SensitivityPublic {
		t.Fatalf("public call: err %v, tiers %v", err, door.tiers)
	}
}

func TestLaneEmbedderNeverSendsRestricted(t *testing.T) {
	paths := embedTestPaths(t)
	door := &recordingDoor{dims: 3}
	sel := composeEmbedderWith(loadEmbedConfig(t, paths, laneBody), runtime.EffectiveConfig{}, testDeps(paths, door))
	for _, tier := range []provider.SensitivityTier{provider.SensitivityLocalOnly, provider.SensitivityRestricted} {
		if sel.Admits(tier) {
			t.Fatalf("lane admits %s", tier)
		}
		wantKind(t, embedOne(conductor.ContextWithDoorSensitivity(context.Background(), tier), sel), cascade.KindPolicyDenied, "refused")
	}
	wantKind(t, embedOne(context.Background(), sel), cascade.KindPolicyDenied, "refused")
	if door.calls() != 0 {
		t.Fatalf("door saw %d calls for local-only/restricted/unstamped content", door.calls())
	}
}

func TestComposeEmbedderNoDefaultSocketAutoSelect(t *testing.T) {
	paths := embedTestPaths(t)
	socketFixture(t, filepath.Join(paths.DataDir(), "embeddings", "bgem3.sock"))
	cfg := loadEmbedConfig(t, paths, "")
	sel := composeEmbedderWith(cfg, runtime.EffectiveConfigOf(cfg), testDeps(paths, &recordingDoor{}))
	if sel.Source() != embedsel.SourceNotConfigured {
		t.Fatalf("a default-path socket was auto-selected: %s", sel.Source())
	}
}

func TestComposeEmbedderRefusesForeignOwnedOrSymlinkSocket(t *testing.T) {
	paths := embedTestPaths(t)
	dir := filepath.Join(t.TempDir(), "s")
	sock := filepath.Join(dir, "bgem3.sock")
	socketFixture(t, sock)
	link := filepath.Join(t.TempDir(), "link.sock")
	if os.Symlink(sock, link) != nil {
		t.Fatal("symlink")
	}
	cfg := loadEmbedConfig(t, paths, laneBody)
	door := &recordingDoor{dims: 3}
	foreign := testDeps(paths, door)
	foreign.uid = os.Getuid() + 1
	check := func(name string, eff runtime.EffectiveConfig, deps embedderDeps, why string) {
		sel := composeEmbedderWith(cfg, eff, deps)
		if sel.Source() != embedsel.SourceLocalSidecar {
			t.Fatalf("%s: source %s, want local-sidecar (no lane fallback)", name, sel.Source())
		}
		wantKind(t, sel.Err(), cascade.KindPolicyDenied, why)
	}
	check("symlink", admittedSocket(t, link), testDeps(paths, door), "symlink")
	check("foreign owner", admittedSocket(t, sock), foreign, "not owned")
	if os.Chmod(dir, 0o770) != nil {
		t.Fatal("chmod")
	}
	check("group-writable parent", admittedSocket(t, sock), testDeps(paths, door), "group- or world-writable")
	if door.calls() != 0 {
		t.Fatalf("refused sidecar fell back to the lane: %d calls", door.calls())
	}
}

func TestComposeEmbedderUsesAdmittedLocalSocket(t *testing.T) {
	paths := embedTestPaths(t)
	sock := filepath.Join(t.TempDir(), "s", "bgem3.sock")
	socketFixture(t, sock)
	cfg := loadEmbedConfig(t, paths, "[retrieval.embedding]\nlocal_socket = \""+filepath.ToSlash(sock)+"\"\n")
	if sel := composeEmbedderWith(cfg, runtime.EffectiveConfig{}, testDeps(paths, &recordingDoor{})); sel.Source() != embedsel.SourceNotConfigured {
		t.Fatalf("an unadmitted on-disk local_socket selected %s", sel.Source())
	}
	if sel := composeEmbedderWith(cfg, admittedSocket(t, sock), testDeps(paths, &recordingDoor{})); sel.Source() != embedsel.SourceLocalSidecar || sel.Err() != nil {
		t.Fatalf("admitted socket: %s err %v", sel.Source(), sel.Err())
	}
}
