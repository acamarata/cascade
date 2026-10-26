//go:build !windows

// Purpose: proves the elevation middleware is mounted on every production
// registry, over a real composed daemon: an unattested elevated verb is
// challenged and never reaches its handler, an attested one runs exactly once,
// and a registry built any other way cannot pass the sweep.
// Inputs: an in-memory ed25519 custody injected through daemonElevationSelector
// and a fresh HOME / CASCADE_HOME; no host keystore, keychain or secret service.
// Outputs: newElevatedTestRegistry, the helper later elevated-RPC tests reuse.
// Constraints: every test restores the selector it replaced.
// SPORT: cmd/cascade elevation wiring tests (P1-SEC-03).
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// The sweep's uninstall.purge_data canary needs the access marking a real
// verb has, or the access policy would refuse it before the middleware ran.
// A production declaration, when one lands, wins: this one then panics and is
// ignored.
func init() {
	defer func() { _ = recover() }()
	rpc.DeclareAccess("uninstall.purge_data", rpc.AccessOwnerOnly)
}

// grantingBuiltinManifest asks for a grant, so installing it is an elevated act.
const grantingBuiltinManifest = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "builtin"
requires = ["net.http"]
`

// unelevatedProblems dispatches every elevated verb the registry serves, plus
// a canary for uninstall.purge_data, without an attestation, and reports each
// way the elevation gate failed: a success, or any error other than the one
// this kind of registry owes (a challenge with a nonce when wantChallenge,
// else the custody refusal). It also returns how often the canary ran.
func unelevatedProblems(reg *rpc.Registry, wantChallenge bool) (problems []string, canaryRuns int) {
	reg.Register("uninstall.purge_data", func(context.Context, json.RawMessage) (any, error) { canaryRuns++; return "purged", nil })
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	for _, name := range rpc.ElevatedVerbNames() {
		if !reg.Registered(name) {
			continue
		}
		// `[]` is not a JSON object, so every conditional rule elevates it.
		_, eo := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: name, Params: json.RawMessage(`[]`)})
		switch {
		case eo == nil:
			problems = append(problems, name+": ran unattested")
		case wantChallenge && (eo.Code != cascade.RPCCodeElevationRequired || nonceOf(eo) == ""):
			problems = append(problems, name+": want a challenge with a nonce, got "+eo.Message)
		case !wantChallenge && eo.Code == cascade.RPCCodeElevationRequired:
			problems = append(problems, name+": a refuse-all registry offered a challenge")
		case !wantChallenge:
			if data, _ := eo.Data.(map[string]string); data["reason"] != rpc.ReasonCustodyTier {
				problems = append(problems, name+": want the custody refusal, got "+eo.Message)
			}
		}
	}
	return problems, canaryRuns
}

func TestNoUnelevatedRegistryServesElevatedVerb(t *testing.T) {
	clock := runtime.NewSystemClock()
	daemonReg := composeElevated(t, true).Registry
	paths := fakeDaemonPaths{root: t.TempDir()}
	wiring := buildMCPToolWiring(context.Background(), paths, clock)
	t.Cleanup(wiring.Close)
	socketReg, err := buildSocketMCPRegistry(mcp.NewToolRegistry(plugin.Builtins, mcp.AllowAllFilter{}), daemon.RefuseAllElevation(clock))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		reg           *rpc.Registry
		wantChallenge bool
	}{
		"daemon":     {daemonReg, true},
		"mcp tools":  {wiring.Methods, false},
		"mcp socket": {socketReg, false},
		"node serve": {nodes.BuildServeRegistry(nodes.ServeDeps{Clock: clock}), false},
	} {
		t.Run(name, func(t *testing.T) {
			problems, canary := unelevatedProblems(tc.reg, tc.wantChallenge)
			if len(problems) != 0 {
				t.Errorf("elevated verbs served without the gate:\n  %s", strings.Join(problems, "\n  "))
			}
			if canary != 0 {
				t.Errorf("the uninstall.purge_data canary ran %d times unattested", canary)
			}
		})
	}
}

// The sweep must fail on a registry built without the middleware, or it
// proves nothing: this is the seeded stub-passing input.
func TestUnelevatedSweepCatchesARegistryWithoutTheMiddleware(t *testing.T) {
	bare := rpc.NewRegistry()
	problems, canary := unelevatedProblems(bare, true)
	if canary != 1 || len(problems) == 0 {
		t.Fatalf("a registry with no Use went undetected: canary runs=%d problems=%v", canary, problems)
	}
}

// Every non-test rpc.NewRegistry() call site is one the sweep above builds.
func TestEveryProductionRegistrySiteIsSwept(t *testing.T) {
	want := []string{"../../cmd/cascade/daemon_unix_run.go", "../../cmd/cascade/mcp.go", "../../cmd/cascade/mcp_tools.go", "../../internal/nodes/serve_registry.go"}
	var got []string
	for _, root := range []string{"../../cmd", "../../internal", "../../pkg", "../../plugins", "../../providers"} {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if src, rerr := os.ReadFile(path); rerr == nil && strings.Contains(string(src), "rpc.NewRegistry()") {
				got = append(got, path)
			}
			return nil
		})
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rpc.NewRegistry() is called from\n  %v\nthe sweep covers\n  %v\nadd the new site to TestNoUnelevatedRegistryServesElevatedVerb", got, want)
	}
}

// foreignKey returns a key pair unrelated to any custody.
func foreignKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv, base64.StdEncoding.EncodeToString(pub)
}

// rewriteTrust replaces the pubkey inside the stored trust record at path.
func rewriteTrust(t *testing.T, path, pubB64 string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trust record %s: %v", path, err)
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	rec["pubkey_b64"] = pubB64
	delete(rec, "fingerprint_sha256")
	out, _ := json.Marshal(rec)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The helper later elevated-RPC tests reuse: a registered elevated verb is
// challenged, then runs once with a witness for its own name after the
// helper signs. vault.rotate is declared and unregistered, so the canary stands in.
func TestNewElevatedTestRegistryAnswersAChallenge(t *testing.T) {
	reg, call := newElevatedTestRegistry(t, filepath.Join(t.TempDir(), "data"))
	runs := 0
	var witness rpc.VerifiedElevation
	reg.Register("vault.rotate", func(ctx context.Context, _ json.RawMessage) (any, error) {
		runs++
		witness, _ = rpc.VerifiedElevationFrom(ctx)
		return map[string]string{"ok": "yes"}, nil
	})
	var out map[string]string
	if err := call(context.Background(), "vault.rotate", map[string]string{"from": "x"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if runs != 1 || out["ok"] != "yes" || witness.Method() != "vault.rotate" {
		t.Fatalf("runs=%d out=%v witness=%+v, want one run with a vault.rotate witness", runs, out, witness)
	}
}
