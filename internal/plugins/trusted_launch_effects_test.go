//go:build !windows

// Purpose: every launch and every call of a trusted plugin writes exactly
// one intent and one terminal record under the contracted keys; a params
// digest override replaces sha256(params) and no row ever holds a planted
// secret or the hash of its plaintext; a recorder killed between intent and
// confirm (P1-SEC-33 re-exec recipe, CASCADE_EFFECT_CHILD) leaves exactly
// one intent that the next recovery marks unknown-outcome.
// SPORT: internal/plugins trusted launch effect tests (P1-PLG-03).
package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	effectChildEnv = "CASCADE_EFFECT_CHILD"
	childCrashCode = 3
	helloMethod    = "cascade.hello"
)

// liveGitHub installs and trusts the real cascade-github and launches it.
func liveGitHub(t *testing.T, f *launchFix) *TrustedHandle {
	t.Helper()
	f.installGitHub(t)
	f.grant(t, cascadeGitHubPluginID, githubManifest(t))
	h, err := LaunchInstalled(context.Background(), cascadeGitHubPluginID)
	if err != nil {
		t.Fatalf("LaunchInstalled: %v", err)
	}
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	return h
}

func sha256OfHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// requireOnePair asserts key has exactly one intent and one terminal record
// in phase, and returns the intent.
func requireOnePair(t *testing.T, f *launchFix, key, phase string) audit.Record {
	t.Helper()
	if got := f.effectPhases(t, key); got["intent"] != 1 || got[phase] != 1 || len(got) != 2 {
		t.Fatalf("records for %s = %v, want one intent and one %s", key, got, phase)
	}
	for _, r := range f.allRecords(t) {
		if r.EffectKey == key && r.EffectPhase == "intent" {
			return r
		}
	}
	t.Fatal("unreachable")
	return audit.Record{}
}

func TestLaunchWritesIntentThenConfirm(t *testing.T) {
	f := newLaunchFix(t)
	h := liveGitHub(t, f)
	key := launchEffectPrefix + cascadeGitHubPluginID + ":" + h.launchID
	intent := requireOnePair(t, f, key, "confirmed")
	var explain map[string]string
	_ = json.Unmarshal(intent.Explain, &explain)
	if intent.Actor != "plugin:cascade-github" || intent.Action != "plugin.launch" || explain["recorder"] != h.token ||
		intent.Kind != audit.KindExternalEffect || !hexToken.MatchString(h.launchID) {
		t.Fatalf("launch intent = %+v (explain %v), want plugin.launch by plugin:cascade-github naming recorder %s", intent, explain, h.token)
	}
}

func TestCallWritesIntentThenConfirm(t *testing.T) {
	f := newLaunchFix(t)
	h := liveGitHub(t, f)
	ctx := context.Background()
	params := []byte(`{"min_protocol_version":"1.0.0"}`)
	if _, err := h.Call(ctx, helloMethod, params); err != nil {
		t.Fatalf("Call(%s): %v", helloMethod, err)
	}
	prefix := callEffectPrefix + cascadeGitHubPluginID + ":" + h.launchID + ":"
	intent := requireOnePair(t, f, prefix+"1", "confirmed")
	if intent.Action != "plugin.call" || intent.ParamsHash != sha256OfHex(params) {
		t.Fatalf("call intent = %+v, want plugin.call with params_hash sha256(params)", intent)
	}
	if _, err := h.Call(ctx, unknownMethod, nil); !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("Call(%s) = %v, want the plugin's typed error", unknownMethod, err)
	}
	requireOnePair(t, f, prefix+"2", "failed")
}

// plantedSecret is a rehydrated value a caller's params may carry.
const plantedSecret = "planted-rehydrated-value-7f3a"

func TestCallParamsDigestOverride(t *testing.T) {
	f := newLaunchFix(t)
	h := liveGitHub(t, f)
	tagged := sha256OfHex([]byte(`{"token":"{{secret:github}}"}`))
	params := []byte(`{"token":"` + plantedSecret + `"}`)
	ctx := WithParamsDigest(context.Background(), tagged)
	if _, err := h.Call(ctx, helloMethod, params); err != nil {
		t.Fatalf("Call with a digest override: %v", err)
	}
	key := callEffectPrefix + cascadeGitHubPluginID + ":" + h.launchID + ":1"
	if intent := requireOnePair(t, f, key, "confirmed"); intent.ParamsHash != tagged {
		t.Fatalf("params_hash = %s, want the supplied digest %s", intent.ParamsHash, tagged)
	}
	before := len(f.allRecords(t))
	for _, bad := range []string{"", "zz", strings.Repeat("a", 63)} {
		if _, err := h.Call(WithParamsDigest(context.Background(), bad), helloMethod, params); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("override %q = %v, want KindInvalidInput", bad, err)
		}
	}
	if after := len(f.allRecords(t)); after != before {
		t.Fatalf("refused overrides wrote %d records", after-before)
	}
}

func TestAuditSweepNoPlaintextHash(t *testing.T) {
	f := newLaunchFix(t)
	h := liveGitHub(t, f)
	params := []byte(`{"token":"` + plantedSecret + `"}`)
	ctx := WithParamsDigest(context.Background(), sha256OfHex([]byte(`{"token":"{{secret:github}}"}`)))
	if _, err := h.Call(ctx, helloMethod, params); err != nil {
		t.Fatalf("Call: %v", err)
	}
	it, err := f.store.Scan(context.Background(), string(storage.DomainAudit), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	rows := 0
	for it.Next(context.Background()) {
		rows++
		v := string(it.Value())
		for _, banned := range []string{plantedSecret, sha256OfHex(params)} {
			if strings.Contains(v, banned) {
				t.Fatalf("audit row %s holds %q", it.Key(), banned)
			}
		}
	}
	if it.Err() != nil || rows < 4 {
		t.Fatalf("swept %d audit rows (err %v), want the launch and call records", rows, it.Err())
	}
}

// runCrashChild re-execs this test binary as the recorder for test, with
// mode and the fixture's paths, and returns its exit code. The store takes
// a cross-process exclusive lock, so the parent closes it for the child's
// run and reopens it (a fresh log and effect log) afterwards.
func runCrashChild(t *testing.T, f *launchFix, test, mode string) int {
	t.Helper()
	if err := f.store.(interface{ Close() error }).Close(); err != nil {
		t.Fatal(err)
	}
	defer f.reopen(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+test+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), effectChildEnv+"="+strings.Join([]string{mode, f.dbPath, f.lockDir, f.exeDir}, "|"))
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child = %v, want an exit status; output:\n%s", err, out)
	}
	if exitErr.ExitCode() == 1 {
		t.Logf("child output:\n%s", out)
	}
	return exitErr.ExitCode()
}

// reopen opens the fixture's store again with a fresh log and effect log.
func (f *launchFix) reopen(t *testing.T) {
	t.Helper()
	f.store = openLaunchStore(t, f.dbPath)
	f.log = audit.New(f.store, testkit.NewFrozenClock(launchTestInstant), nil)
	f.effects, _ = audit.NewEffectLog(f.log)
}

// childFix is the recorder process's view of the parent's fixture.
func childFix(t *testing.T, spec string) (*launchFix, string) {
	parts := strings.Split(spec, "|")
	f := &launchFix{dbPath: parts[1], lockDir: parts[2], exeDir: parts[3], env: map[string]string{"PATH": "/usr/bin:/bin"}}
	SetExecutableDirForTest(f.exeDir)
	f.store = openLaunchStore(t, f.dbPath)
	f.log = audit.New(f.store, testkit.NewFrozenClock(launchTestInstant), nil)
	f.effects, _ = audit.NewEffectLog(f.log)
	n, err := RecoverPluginEffects(context.Background(), f.effects, f.lockDir)
	if err != nil {
		os.Exit(4)
	}
	SetLaunchEnvSource(f.source)
	return f, strconv.Itoa(n)
}

// requireRecoveredOnce asserts recovery marks exactly one intent, which is
// key's, and a second recovery marks none.
func requireRecoveredOnce(t *testing.T, f *launchFix, keyPrefix string) {
	t.Helper()
	pending, err := f.effects.PendingEffects(context.Background())
	if err != nil || len(pending) != 1 || !strings.HasPrefix(pending[0].Key, keyPrefix) {
		t.Fatalf("pending after the crash = %+v, %v; want one %s* intent", pending, err, keyPrefix)
	}
	for i, want := range []int{1, 0} {
		if n, err := RecoverPluginEffects(context.Background(), f.effects, f.lockDir); err != nil || n != want {
			t.Fatalf("recovery %d marked %d (%v), want %d", i+1, n, err, want)
		}
	}
	requireOnePair(t, f, pending[0].Key, "unknown-outcome")
}

func TestLaunchEffectCrashBetweenIntentAndConfirm(t *testing.T) {
	if spec := os.Getenv(effectChildEnv); spec != "" {
		f, _ := childFix(t, spec)
		env, _ := f.source(context.Background())
		rec, err := trustedRecord(context.Background(), env, cascadeGitHubPluginID)
		if err != nil {
			os.Exit(5)
		}
		if _, err := beginLaunch(context.Background(), env, rec, rec.ExecDigest); err != nil {
			os.Exit(6)
		}
		os.Exit(childCrashCode) // after BeginEffect(plugin.launch), before Confirm
	}
	f := newLaunchFix(t)
	f.installGitHub(t)
	f.grant(t, cascadeGitHubPluginID, githubManifest(t))
	if code := runCrashChild(t, f, "TestLaunchEffectCrashBetweenIntentAndConfirm", "launch"); code != childCrashCode {
		t.Fatalf("child exit = %d, want %d", code, childCrashCode)
	}
	requireRecoveredOnce(t, f, launchEffectPrefix+cascadeGitHubPluginID+":")
}

func TestCallEffectCrashBetweenEffectAndConfirm(t *testing.T) {
	if spec := os.Getenv(effectChildEnv); spec != "" {
		childFix(t, spec)
		h, err := LaunchInstalled(context.Background(), cascadeGitHubPluginID)
		if err != nil {
			os.Exit(5)
		}
		if _, err := h.beginCall(context.Background(), helloMethod, nil); err != nil {
			os.Exit(6)
		}
		if _, err := h.h.Call(context.Background(), helloMethod, nil); err != nil {
			os.Exit(7)
		}
		os.Exit(childCrashCode) // the call ran; its confirm never written
	}
	f := newLaunchFix(t)
	f.installGitHub(t)
	f.grant(t, cascadeGitHubPluginID, githubManifest(t))
	if code := runCrashChild(t, f, "TestCallEffectCrashBetweenEffectAndConfirm", "call"); code != childCrashCode {
		t.Fatalf("child exit = %d, want %d", code, childCrashCode)
	}
	requireRecoveredOnce(t, f, callEffectPrefix+cascadeGitHubPluginID+":")
}
