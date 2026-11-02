//go:build !windows

// Purpose: `cascade plugin update <id> --reverify` end to end (P1-PLG-12):
// the real cascade-github executable is trusted by an attested plugin.add,
// its sibling is replaced by a new build, LaunchInstalled refuses naming the
// reverify command, and that exact printed command, run through the CLI
// against an in-test signed index whose manifest lists the new digest,
// re-pins so the next launch succeeds. The index is signed in-test with a
// test key injected as the daemon's registry key; custody is the injected
// platform-tier test custody of newElevatedDaemon; no network.
// SPORT: cmd/cascade plugin reverify tests (P1-PLG-12).
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// indexServer serves one signed index and one signed manifest artifact.
type indexServer struct{ index, manifest []byte }

func (s indexServer) FetchIndex(context.Context) ([]byte, error) { return s.index, nil }

func (s indexServer) FetchArtifact(context.Context, plugin.RegistryVersionEntry) ([]byte, error) {
	return s.manifest, nil
}

// signReverifyIndex signs an index listing manifest as id's latest version.
func signReverifyIndex(t *testing.T, priv ed25519.PrivateKey, id, version string, manifest []byte) []byte {
	t.Helper()
	entries, err := json.Marshal([]plugin.RegistryIndexEntry{{ID: id, LatestVersion: version, Versions: []plugin.RegistryVersionEntry{{
		Version: version, Checksum: hexSum(manifest), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))}}}})
	if err != nil {
		t.Fatal(err)
	}
	const published = "2026-11-01T00:00:00Z"
	payload, _ := json.Marshal(struct {
		SchemaVersion string          `json:"schema_version"`
		Entries       json.RawMessage `json:"entries"`
		Sequence      uint64          `json:"sequence,omitempty"`
		PublishedAt   string          `json:"published_at,omitempty"`
	}{plugin.SchemaVersionCurrent, entries, 1, published})
	doc, err := json.Marshal(map[string]any{"schema_version": plugin.SchemaVersionCurrent, "entries": json.RawMessage(entries),
		"sequence": 1, "published_at": published, "signature": base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// rebuildGithub replaces dir/cascade-github with a differently linked build.
func rebuildGithub(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "cascade-github")
	if b, err := exec.Command("go", "build", "-ldflags=-s -w", "-o", out, "../../plugins/github").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	if err := os.Chmod(out, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return hexSum(b)
}

// printedCommand extracts the backquoted command from a launch refusal.
func printedCommand(t *testing.T, err error) []string {
	t.Helper()
	msg := err.Error()
	i := strings.Index(msg, "run `")
	if i < 0 {
		t.Fatalf("refusal %q prints no command", msg)
	}
	rest := msg[i+len("run `"):]
	fields := strings.Fields(rest[:strings.Index(rest, "`")])
	if len(fields) < 2 || fields[0] != "cascade" {
		t.Fatalf("printed command %v does not start with cascade", fields)
	}
	return fields[1:]
}

// installReverifyKey swaps in a fresh registry signing key for the test.
func installReverifyKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	prevKey := pluginReverifyKey
	pluginReverifyKey = func(*daemonWiring) []byte { return pub }
	t.Cleanup(func() { pluginReverifyKey = prevKey })
	return pub, priv
}

// reverifyDeps wires the CLI to the signed index and routes the elevated
// update call to the test daemon d.
func reverifyDeps(t *testing.T, d elevatedDaemon, pub ed25519.PublicKey, index, manifest []byte) pluginDeps {
	t.Helper()
	deps := testPluginDeps(t)
	deps.UpdateRegistryClient = func(context.Context) (*plugin.RegistryClient, error) {
		return plugin.NewRegistryClient(plugin.RegistryConfig{PublicKey: pub}, indexServer{index: index, manifest: manifest},
			plugin.Ed25519Verifier{PublicKey: pub}, nil, runtime.NewSystemClock()), nil
	}
	prevBytes := pluginReverifyIndexBytes
	pluginReverifyIndexBytes = func(context.Context, pluginDeps) ([]byte, error) { return index, nil }
	t.Cleanup(func() { pluginReverifyIndexBytes = prevBytes })
	swapUpdateElevatedCall(t, func(ctx context.Context, _ pluginDeps, _ io.Writer, req plugins.UpdateParams, out any) error {
		return d.Call(ctx, plugins.UpdateRPCMethod, req, out)
	})
	return deps
}

func TestRepinAfterExecutableReplaced(t *testing.T) {
	exeDir := t.TempDir()
	goBuild(t, "../../plugins/github", exeDir, "cascade-github")
	t.Cleanup(plugins.SetExecutableDirForTest(exeDir))
	pub, priv := installReverifyKey(t)
	d := newElevatedDaemon(t, filepath.Join(t.TempDir(), "data"), true)
	useTestLaunchEnv(t, d.Store)
	base, err := os.ReadFile("../../plugins/github/manifest.toml")
	if err != nil {
		t.Fatal(err)
	}
	attestedAdd(t, d.Call, "cascade-github", string(base)) // the real witnessed grant
	ctx := context.Background()
	if h, err := plugins.LaunchInstalled(ctx, "cascade-github"); err != nil {
		t.Fatalf("launch before the rebuild: %v", err)
	} else if err := h.Close(ctx); err != nil {
		t.Fatal(err)
	}

	newDigest := rebuildGithub(t, exeDir)
	_, err = plugins.LaunchInstalled(ctx, "cascade-github")
	if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), "executable changed since it was trusted") {
		t.Fatalf("launch after the rebuild = %v, want the changed-executable refusal", err)
	}
	args := printedCommand(t, err)
	if strings.Join(args, " ") != "plugin update cascade-github --reverify" {
		t.Fatalf("printed command = %v", args)
	}

	manifest := []byte(string(base) + "\n[exec_sha256]\n\"" + goruntime.GOOS + "/" + goruntime.GOARCH + "\" = \"" + newDigest + "\"\n")
	index := signReverifyIndex(t, priv, "cascade-github", "0.1.0", manifest)
	deps := reverifyDeps(t, d, pub, index, manifest)

	// runPluginCLIState roots the tree at `plugin`, so the printed `cascade plugin ...` loses its first word.
	stdout, _, err := runPluginCLIState(t, deps, runtime.DaemonlessState{Embedded: false}, args[1:]...)
	if err != nil {
		t.Fatalf("the printed command %v: %v", args, err)
	}
	if !strings.Contains(stdout, "re-verified cascade-github v0.1.0") {
		t.Fatalf("stdout = %q", stdout)
	}
	rec, ok, err := plugins.LoadMetadata(ctx, d.Store, "cascade-github")
	if err != nil || !ok || rec.ExecDigest != newDigest || rec.TrustTier != plugins.TrustTrusted {
		t.Fatalf("record after reverify = %+v (ok %v, err %v), want digest %s trusted", rec, ok, err, newDigest)
	}
	h, err := plugins.LaunchInstalled(ctx, "cascade-github")
	if err != nil {
		t.Fatalf("launch after the reverify: %v", err)
	}
	if err := h.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPluginUpdateReverifyFlagCombinations(t *testing.T) {
	deps := testPluginDeps(t)
	for _, extra := range []string{"--from=x.toml", "--checksum=abc", "--accept-grant=net", "--all"} {
		_, _, err := runPluginCLIState(t, deps, runtime.DaemonlessState{Embedded: false}, "update", "demo", "--reverify", extra)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("--reverify %s = %v, want KindInvalidInput", extra, err)
		}
	}
	_, _, err := runPluginCLIState(t, deps, runtime.DaemonlessState{Embedded: true}, "update", "demo", "--reverify")
	if err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("daemonless --reverify = %v, want the daemon-required refusal", err)
	}
}
