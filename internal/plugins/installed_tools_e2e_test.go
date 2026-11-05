//go:build !windows

// Purpose: end to end over the real cascade-github executable, built by
// import path into t.TempDir() (plugintest.BuildGithub) and trusted only
// through a witnessed plugin.add (plugintest.InstallTrusted): the captured
// claude-code frames list every shipped tool; untrusted or with one grant
// revoked, none; a tools/call reaches the plugin through LaunchInstalled,
// records its effects, carries the untrusted-source provenance and honours
// a params digest override. No network is reached: the call's arguments
// are refused by the plugin before any request is built.
// SPORT: internal/plugins installed process tools e2e (P1-PLG-06).
package plugins_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/plugintest"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

const goldenIn = "../mcp/testdata/goldens/claude-code/2.1.273/in.jsonl"

// e2eFix is the store, the effect log and the launch source over them.
type e2eFix struct {
	store provider.Store
	log   *audit.Log
	m     plugin.Manifest
	raw   []byte
}

func newE2EFix(t *testing.T) e2eFix {
	t.Helper()
	t.Cleanup(plugins.SetExecutableDirForTest(plugintest.BuildGithub(t)))
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("CASCADE_HOME", dir)
	store, err := sqlite.Open(context.Background(), filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log := audit.New(store, testkit.NewFrozenClock(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)), nil)
	effects, err := audit.NewEffectLog(log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.RecoverPluginEffects(context.Background(), effects, filepath.Join(dir, "plugin-effects")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plugins.SetLaunchEnvSource(func(context.Context) (plugins.LaunchEnv, error) {
		return plugins.LaunchEnv{Store: store, Effects: effects, Registrar: e2eRegistrar{},
			Getenv: func(string) string { return "" }}, nil
	}))
	fp := plugins.FirstPartyManifests()[0]
	return e2eFix{store: store, log: log, m: fp.Manifest, raw: fp.Raw}
}

// e2eRegistrar accepts the plugin-process egress class registration.
type e2eRegistrar struct{}

func (e2eRegistrar) Register(process.EgressClass, process.EgressRegistrationConfig) error { return nil }

func (f e2eFix) server() *mcp.Server {
	return mcp.NewServer(mcp.NewToolRegistryWithProcess(plugin.Builtins, plugins.InstalledToolSource(), mcp.DenyAllFilter{}))
}

// replayList replays initialize, notifications/initialized and tools/list
// from the captured frames and returns the plugin's listed tools, sorted.
func (f e2eFix) replayList(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(goldenIn)
	if err != nil {
		t.Fatal(err)
	}
	srv := f.server()
	var names []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		frame, perr := mcp.ParseFrame(sc.Bytes())
		if perr != nil {
			t.Fatalf("captured frame does not parse: %v", perr)
		}
		if frame.Method != mcp.MethodInitialize && frame.Method != "notifications/initialized" && frame.Method != mcp.MethodToolsList {
			continue
		}
		resp := srv.Dispatch(context.Background(), frame)
		if frame.Method != mcp.MethodToolsList {
			continue
		}
		out, _ := json.Marshal(resp)
		var body struct {
			Result struct{ Tools []struct{ Name string } }
		}
		if err := json.Unmarshal(out, &body); err != nil {
			t.Fatal(err)
		}
		for _, tool := range body.Result.Tools {
			if strings.HasPrefix(tool.Name, f.m.ID+".") {
				names = append(names, tool.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func (f e2eFix) records(t *testing.T) []audit.Record {
	t.Helper()
	var all []audit.Record
	filter := audit.Filter{}
	for {
		page, err := f.log.Query(context.Background(), filter)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Records...)
		if page.NextCursor == "" {
			return all
		}
		filter.Cursor = page.NextCursor
	}
}

// phases counts records by phase for keys with prefix.
func (f e2eFix) phases(t *testing.T, prefix string) map[string]int {
	t.Helper()
	got := map[string]int{}
	for _, r := range f.records(t) {
		if strings.HasPrefix(r.EffectKey, prefix) {
			got[r.EffectPhase]++
		}
	}
	return got
}

func TestInstalledGithubToolsListedToRealClient(t *testing.T) {
	f := newE2EFix(t)
	if got := f.replayList(t); len(got) != 0 {
		t.Fatalf("listed %v before any install", got)
	}
	ref := plugintest.InstallTrusted(t, f.store, f.m.ID, f.raw)
	rec := ref.Load(t)
	if g := plugins.EffectiveGrants(rec); strings.Join(g, ",") != strings.Join(f.m.Requires, ",") {
		t.Fatalf("stored grants %v, want the shipped requires %v", g, f.m.Requires)
	}
	var want []string
	for _, tool := range f.m.Provides.Tools {
		want = append(want, tool.Name)
	}
	sort.Strings(want)
	if got := f.replayList(t); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools/list = %v, want every shipped tool %v", got, want)
	}
	revoked := rec
	revoked.Grants = nil
	for _, g := range rec.Grants {
		if g != "oauth:repo" {
			revoked.Grants = append(revoked.Grants, g)
		}
	}
	if err := plugins.SaveMetadata(context.Background(), f.store, revoked); err != nil {
		t.Fatal(err)
	}
	if got := f.replayList(t); len(got) != 0 {
		t.Fatalf("listed %v with one grant revoked", got)
	}
	untrusted := plugins.PluginMetadata{Name: rec.Name, InstalledVersion: rec.InstalledVersion, Enabled: true,
		RuntimeMode: rec.RuntimeMode, Grants: rec.Grants, TrustGrants: rec.TrustGrants}
	if err := plugins.SaveMetadata(context.Background(), f.store, untrusted); err != nil {
		t.Fatal(err)
	}
	if got := f.replayList(t); len(got) != 0 {
		t.Fatalf("listed %v for an untrusted record", got)
	}
}

// callFrame is a tools/call frame for tool with args.
func callFrame(t *testing.T, tool, args string) *mcp.Frame {
	t.Helper()
	line := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
	frame, perr := mcp.ParseFrame([]byte(line))
	if perr != nil {
		t.Fatal(perr)
	}
	return frame
}

// callText dispatches a tools/call and returns the result's text block.
func callText(t *testing.T, srv *mcp.Server, frame *mcp.Frame) string {
	t.Helper()
	out, _ := json.Marshal(srv.Dispatch(context.Background(), frame))
	var body struct {
		Result struct{ Content []struct{ Text string } }
		Error  json.RawMessage
	}
	if err := json.Unmarshal(out, &body); err != nil || len(body.Error) > 0 || len(body.Result.Content) != 1 {
		t.Fatalf("tools/call = %s, want one content block", out)
	}
	return body.Result.Content[0].Text
}

func TestInstalledProcessToolCallRoutesToHandle(t *testing.T) {
	f := newE2EFix(t)
	plugintest.InstallTrusted(t, f.store, f.m.ID, f.raw)
	text := callText(t, f.server(), callFrame(t, f.m.ID+".repos.get", `{"repo":"demo"}`))
	if !strings.Contains(text, `"error"`) {
		t.Fatalf("result %s, want the plugin's own refusal of the missing owner", text)
	}
	if got := f.phases(t, "plugin.launch:"+f.m.ID+":"); got["intent"] != 1 || got["confirmed"] != 1 || len(got) != 2 {
		t.Fatalf("launch records %v, want one intent and one confirm", got)
	}
	if got := f.phases(t, "plugin.call:"+f.m.ID+":"); got["intent"] != 1 || got["failed"] != 1 || len(got) != 2 {
		t.Fatalf("call records %v, want one intent and its terminal record", got)
	}
}

func TestProcessToolResultMarkedUntrusted(t *testing.T) {
	f := newE2EFix(t)
	plugintest.InstallTrusted(t, f.store, f.m.ID, f.raw)
	core := mcp.CoreRegistration{Tool: mcp.Tool{Name: "core_echo"}, Grants: []string{"read"},
		Handler: func(context.Context, []byte) ([]byte, error) { return []byte(`{"core":true}`), nil }}
	srv := mcp.NewServer(mcp.NewToolRegistryWithProcess(plugin.Builtins, plugins.InstalledToolSource(), mcp.DenyAllFilter{}, core))
	var env struct {
		Meta map[string]string `json:"_meta"`
	}
	text := callText(t, srv, callFrame(t, f.m.ID+".repos.get", `{"repo":"demo"}`))
	if err := json.Unmarshal([]byte(text), &env); err != nil || env.Meta[plugins.ProvenanceMetaKey] != string(provider.ProvenanceUntrustedSource) {
		t.Fatalf("process result %s carries no untrusted-source provenance", text)
	}
	if got := callText(t, srv, callFrame(t, "core_echo", `{}`)); got != `{"core":true}` {
		t.Fatalf("core result = %s, want it unchanged", got)
	}
}

func sha256Text(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestProcessToolCallHonoursParamsDigest(t *testing.T) {
	f := newE2EFix(t)
	plugintest.InstallTrusted(t, f.store, f.m.ID, f.raw)
	plantedValue := strings.Join([]string{"planted", "rehydrated", "value"}, "-")
	tagged := sha256Text([]byte(`{"repo":"tagged-placeholder"}`))
	args := `{"repo":"` + plantedValue + `"}`
	ctx := plugins.WithParamsDigest(context.Background(), tagged)
	if resp := f.server().Dispatch(ctx, callFrame(t, f.m.ID+".repos.get", args)); resp == nil || resp.Error != nil {
		t.Fatalf("tools/call under a digest override = %+v", resp)
	}
	found := false
	for _, r := range f.records(t) {
		if r.Action == "plugin.call" && r.EffectPhase == "intent" {
			found = r.ParamsHash == tagged
		}
	}
	if !found {
		t.Fatal("the call intent does not record the supplied digest")
	}
	it, err := f.store.Scan(context.Background(), string(storage.DomainAudit), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	rows := 0
	for it.Next(context.Background()) {
		rows++
		for _, banned := range []string{plantedValue, sha256Text([]byte(args))} {
			if strings.Contains(string(it.Value()), banned) {
				t.Fatalf("audit row %s holds a banned value", it.Key())
			}
		}
	}
	if it.Err() != nil || rows < 4 {
		t.Fatalf("swept %d audit rows (err %v), want the launch and call records", rows, it.Err())
	}
}
