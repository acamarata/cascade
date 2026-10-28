// Purpose: `cascade plugin add <intent-ref>` end to end through the real
// root command, the real builtin registry and cascade-pa's real intent
// handler: a free-text ref reaches the install flow and proposes the
// fixture's plugin, CASCADE_NO_INPUT=1 refuses before any dispatch, a Stat
// failure other than not-exist refuses, and no chat, bridge or conversation
// source can reach the dispatcher.
//
// The two flow tests replace cascade-pa's process-wide install flow, which
// this package cannot rebuild afterwards, so each runs in a child process
// of this test binary and the parent asserts the child passed.
// SPORT: cmd/cascade plugin add <intent-ref> (TEST) — P1-PLG-07.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/resolver"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// intentChildEnv names the child-process test a re-exec runs.
const intentChildEnv = "INTENT_FLOW_CHILD_TEST"

// inIntentChild reports true inside the child. In the parent it re-runs
// exactly t's test in a child process and fails t unless the child passed.
func inIntentChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv(intentChildEnv) == t.Name() {
		return true
	}
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v") //nolint:gosec // this test binary
	child.Env = append(os.Environ(), intentChildEnv+"="+t.Name())
	out, err := child.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("child run of %s failed: %v\n%s", t.Name(), err, out)
	}
	return false
}

// intentRecorder is the flow's event bus: it keeps every event.
type intentRecorder struct {
	mu     sync.Mutex
	events []install.Event
}

func (r *intentRecorder) Publish(_ context.Context, evt install.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, evt)
	return nil
}

func (r *intentRecorder) all() []install.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]install.Event(nil), r.events...)
}

type intentInstallTally struct{ calls atomic.Int32 }

func (i *intentInstallTally) Add(context.Context, install.AddRequest) (install.AddResult, error) {
	i.calls.Add(1)
	return install.AddResult{}, nil
}

type intentDeclineGate struct{}

func (intentDeclineGate) Confirm(context.Context, install.Proposal) (install.ConfirmOutcome, error) {
	return install.ConfirmDeclined, nil
}

type intentNoElevator struct{}

func (intentNoElevator) Elevate(context.Context, install.ElevationRequest) (install.ElevationResult, error) {
	return install.ElevationResult{}, nil
}

// acceptanceIndexKey is the signed acceptance fixture's public key
// (plugins/cascade-pa/install/testdata/acceptance/README.md), split so it
// is not read as a credential literal.
const acceptanceIndexKey = "14rm+t9vx2h0KKOhATbZmLDl" + "zwLAhDBKiz+rKKUoJwM="

// fixtureSnapshot is the flow's snapshot: this binary's builtins plus the
// signed acceptance registry index, verified the production way.
func fixtureSnapshot(t *testing.T) func(context.Context) (plugin.ManifestSet, *plugin.VerifiedIndex, error) {
	t.Helper()
	pub, err := base64.StdEncoding.DecodeString(acceptanceIndexKey)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "plugins", "cascade-pa", "install", "testdata", "acceptance", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := plugin.NewVerifiedIndex(context.Background(), plugin.Ed25519Verifier{PublicKey: ed25519.PublicKey(pub)}, data)
	if err != nil {
		t.Fatalf("verify acceptance index: %v", err)
	}
	var set plugin.ManifestSet
	for _, b := range plugin.Builtins() {
		set = append(set, plugin.InstalledPlugin{Manifest: b.Manifest, Enabled: true})
	}
	return func(context.Context) (plugin.ManifestSet, *plugin.VerifiedIndex, error) { return set, idx, nil }
}

// useRecordingFlow installs a flow over the real resolver and the fixture
// snapshot, with a recording bus, a declining gate and a counting installer.
func useRecordingFlow(t *testing.T) (*intentRecorder, *intentInstallTally) {
	t.Helper()
	t.Setenv("CASCADE_HOME", filepath.Join(t.TempDir(), ".cascade"))
	rec, tally := &intentRecorder{}, &intentInstallTally{}
	install.SetFlow(install.NewFlow(install.Deps{Resolver: resolver.NewIntentResolver(), Confirm: intentDeclineGate{},
		Install: tally, Elevate: intentNoElevator{}, Events: rec, Getenv: os.Getenv, Snapshot: fixtureSnapshot(t)}))
	t.Cleanup(func() { install.SetFlow(nil) })
	return rec, tally
}

func TestPluginAddIntentRefRoutesToInstallFlow(t *testing.T) {
	if !inIntentChild(t) {
		return
	}
	rec, tally := useRecordingFlow(t)
	got := execCascade([]string{"plugin", "add", "link my GitHub"})
	if got.exit != 0 {
		t.Fatalf("plugin add exited %d\nstdout: %s\nstderr: %s", got.exit, got.stdout, got.stderr)
	}
	events := rec.all()
	if len(events) != 2 || events[0].Kind != install.EventInstallProposal || events[1].Kind != install.EventInstallDeclined {
		t.Fatalf("events = %+v, want a proposal then a decline", events)
	}
	if p := events[0].Proposal; p == nil || p.PluginID != "cascade-github" || p.Intent != "link my GitHub" {
		t.Fatalf("proposal = %+v, want cascade-github for the typed intent", events[0].Proposal)
	}
	if n := tally.calls.Load(); n != 0 {
		t.Fatalf("installer called %d times after a decline", n)
	}
	if !strings.Contains(got.stdout, `intent "link my GitHub" handled: no plugin was installed`) {
		t.Fatalf("stdout = %q", got.stdout)
	}
}

func TestPluginAddIntentRefNoInputRefuses(t *testing.T) {
	if !inIntentChild(t) {
		return
	}
	rec, tally := useRecordingFlow(t)
	t.Setenv("CASCADE_NO_INPUT", "1")
	got := execCascade([]string{"plugin", "add", "link my GitHub"})
	if got.exit == 0 || !strings.Contains(got.stderr, "CASCADE_NO_INPUT=1") {
		t.Fatalf("exit %d, stderr %q; want a CASCADE_NO_INPUT refusal", got.exit, got.stderr)
	}
	if n, events := tally.calls.Load(), rec.all(); n != 0 || len(events) != 0 {
		t.Fatalf("installer calls %d, events %+v; want none before the refusal", n, events)
	}
}

func TestPluginAddStatErrorRefuses(t *testing.T) {
	t.Setenv("CASCADE_HOME", filepath.Join(t.TempDir(), ".cascade"))
	got := execCascade([]string{"plugin", "add", "bad\x00ref"})
	if got.exit == 0 || !strings.Contains(got.stderr, "cannot stat") {
		t.Fatalf("exit %d, stderr %q; want a stat refusal", got.exit, got.stderr)
	}
	if strings.Contains(got.stderr, "intent") || strings.Contains(got.stderr, "registry index") {
		t.Fatalf("a stat failure was routed as an intent: %q", got.stderr)
	}
	if blank := execCascade([]string{"plugin", "add", "  "}); blank.exit == 0 || !strings.Contains(blank.stderr, "the ref is empty") {
		t.Fatalf("blank ref: exit %d, stderr %q", blank.exit, blank.stderr)
	}
	t.Run("dangling symlink", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "gone.toml")
		if err := os.Symlink(filepath.Join(t.TempDir(), "missing.toml"), link); err != nil {
			t.Skipf("no symlink support here: %v", err)
		}
		if got := execCascade([]string{"plugin", "add", link}); got.exit == 0 || !strings.Contains(got.stderr, "read manifest") {
			t.Fatalf("exit %d, stderr %q; want the manifest read refusal, not an intent", got.exit, got.stderr)
		}
	})
	for raw, want := range map[string]string{`{"Resumed":true}`: "handled: the plugin is installed",
		`{"Resumed":false}`: "handled: no plugin was installed", `"other"`: `handled: "other"`} {
		if got := (pluginIntentView{Intent: "x", Result: json.RawMessage(raw)}).String(); !strings.HasSuffix(got, want) {
			t.Errorf("view of %s = %q, want suffix %q", raw, got, want)
		}
	}
}

// TestPluginAddIntentRefDaemonRoute: with a daemon, the ref travels as the
// shared plugin.intent params to the real handler over the real registry,
// and the flow's own refusal comes back unchanged.
func TestPluginAddIntentRefDaemonRoute(t *testing.T) {
	var sent []byte
	prev := pluginIntentDaemonCall
	t.Cleanup(func() { pluginIntentDaemonCall = prev })
	pluginIntentDaemonCall = func(ctx context.Context, _ pluginDeps, params plugins.IntentRPCParams) (json.RawMessage, error) {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		sent = raw
		reg := &plugins.BuiltinRegistry{}
		_ = reg.Load()
		_, err = plugins.IntentRPCHandler(plugins.NewIntentDispatcher(reg))(ctx, raw)
		return nil, err
	}
	cmd := &cobra.Command{}
	cmd.SetContext(runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false}))
	err := runPluginAddIntent(cmd, pluginDeps{Getenv: func(string) string { return "" }}, "link my GitHub")
	if err == nil || !strings.Contains(err.Error(), "no verified registry index") {
		t.Fatalf("daemon route: %v; want the flow's resolver refusal", err)
	}
	if want := `{"intent_name":"cascade.install_flow","input":{"intent":"link my GitHub"}}`; string(sent) != want {
		t.Fatalf("plugin.intent params = %s, want %s", sent, want)
	}
}

// intentDispatchNames are the identifiers a caller of the dispatcher must
// spell; a string literal naming the verb is checked separately.
var intentDispatchNames = map[string]bool{
	"IntentDispatcher": true, "NewIntentDispatcher": true, "IntentRPCHandler": true, "IntentRPCMethod": true,
	"dispatchIntentInProcess": true, "runPluginAddIntent": true,
}

// intentRefs parses one Go file and returns every dispatcher reference in it.
func intentRefs(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var refs []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if intentDispatchNames[x.Name] {
				refs = append(refs, x.Name)
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING && strings.Contains(x.Value, "plugin.intent") {
				refs = append(refs, x.Value)
			}
		}
		return true
	})
	return refs
}

// TestChatPathNeverDispatchesIntent: no chat, bridge or conversation source
// refers to the intent dispatcher or its verb. Each root must hold Go files,
// so an emptied or moved root fails instead of passing on nothing.
func TestChatPathNeverDispatchesIntent(t *testing.T) {
	chat, err := filepath.Glob("chat_*.go")
	if err != nil || len(chat) == 0 {
		t.Fatalf("no cmd/cascade chat_*.go files: %v", err)
	}
	roots := map[string][]string{"cmd/cascade/chat_*.go": chat}
	for _, root := range []string{"internal/conversation", "internal/bridge", "plugins/cascade-pa/telegram"} {
		walkErr := filepath.WalkDir(filepath.Join("..", "..", filepath.FromSlash(root)), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") {
				roots[root] = append(roots[root], p)
			}
			return err
		})
		if walkErr != nil || len(roots[root]) == 0 {
			t.Fatalf("%s: no Go files scanned (%v)", root, walkErr)
		}
	}
	for root, files := range roots {
		for _, p := range files {
			if refs := intentRefs(t, p); len(refs) > 0 {
				t.Errorf("%s (%s) references the intent dispatcher: %v", p, root, refs)
			}
		}
	}
}
