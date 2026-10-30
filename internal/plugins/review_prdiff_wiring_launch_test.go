//go:build !windows

package plugins

// Purpose (this file): the PR-diff wiring through a real launched process:
// the replay plugin, installed as cascade-github through the real trust
// path, answers from the api.github.com capture next to its executable.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

const prDiffCapture = "../../plugins/github/tools/testdata/prs-diff/response.diff"

func readCapture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(prDiffCapture)
	if err != nil || len(body) == 0 {
		t.Fatalf("capture: %d bytes, %v", len(body), err)
	}
	return body
}

func replayPRFetcher(t *testing.T) (*launchFix, *prDiffFetcher) {
	t.Helper()
	t.Setenv("CASCADE_HOME", t.TempDir())
	env := newLaunchFix(t)
	buildInto(t, "./testdata/replayplugin", env.exeDir, cascadeGitHubPluginID)
	if err := os.WriteFile(filepath.Join(env.exeDir, "response.diff"), readCapture(t), 0o600); err != nil {
		t.Fatal(err)
	}
	env.grant(t, cascadeGitHubPluginID, githubManifest(t))
	return env, newPRDiffFetcher(InstalledHandleProvider(), egress.DefaultRegistry())
}

func countActions(t *testing.T, env *launchFix, action, phase string) int {
	t.Helper()
	n := 0
	for _, r := range env.allRecords(t) {
		if r.Action == action && (phase == "" || r.EffectPhase == phase) {
			n++
		}
	}
	return n
}

// TestPRDiffWiringFetchesThroughTrustedPlugin: the capture comes back byte
// for byte; {owner, repo, number} transited github-pr-diff BEFORE the
// launch; the call is a confirmed plugin.call effect.
func TestPRDiffWiringFetchesThroughTrustedPlugin(t *testing.T) {
	env, f := replayPRFetcher(t)
	var order []string
	var seen []byte
	f.intercept = func(ctx context.Context, r *egress.Registry, b []byte) ([]byte, error) {
		out, err := interceptPRDiff(ctx, r, b)
		if err == nil {
			order, seen = append(order, "github-pr-diff"), b
		}
		return out, err
	}
	f.provider = func(ctx context.Context, id string) (*TrustedHandle, error) {
		order = append(order, "launch")
		return LaunchInstalled(ctx, id)
	}
	got, err := fetchTestPR(f, 1)
	if err != nil || !bytes.Equal(got, readCapture(t)) {
		t.Fatalf("diff %d bytes err %v, want the capture", len(got), err)
	}
	if !reflect.DeepEqual(order, []string{"github-pr-diff", "launch"}) {
		t.Fatalf("order %v, want firewall before launch", order)
	}
	if string(seen) != `{"owner":"octocat","repo":"Hello-World","number":1}` {
		t.Fatalf("intercepted %s", seen)
	}
	if n := countActions(t, env, "plugin.call", "confirmed"); n != 1 {
		t.Fatalf("confirmed plugin.call records = %d, want 1", n)
	}
}

// TestPRDiffWiringAbsentPluginUnavailable: no install is KindUnavailable
// naming the add command, with no launch.
func TestPRDiffWiringAbsentPluginUnavailable(t *testing.T) {
	t.Setenv("CASCADE_HOME", t.TempDir())
	env := newLaunchFix(t)
	_, err := fetchTestPR(newPRDiffFetcher(InstalledHandleProvider(), egress.DefaultRegistry()), 1)
	kind, _ := cascade.KindOf(err)
	if kind != cascade.KindUnavailable || !strings.Contains(err.Error(), "`cascade plugin add cascade-github`") {
		t.Fatalf("absent: %v (kind %v)", err, kind)
	}
	if n := countActions(t, env, "plugin.launch", ""); n != 0 {
		t.Fatalf("absent plugin produced %d launch records", n)
	}
}

// TestPRDiffWiringUntrustedPluginRefused: an untrusted or disabled install
// keeps LaunchInstalled's KindPolicyDenied and never launches.
func TestPRDiffWiringUntrustedPluginRefused(t *testing.T) {
	for _, demote := range []func(*PluginMetadata){
		func(r *PluginMetadata) { r.TrustTier = "" },
		func(r *PluginMetadata) { r.Enabled = false },
	} {
		env, f := replayPRFetcher(t)
		rec, ok, err := LoadMetadata(context.Background(), env.store, cascadeGitHubPluginID)
		if err != nil || !ok {
			t.Fatalf("load: %v %v", ok, err)
		}
		demote(&rec)
		if err := SaveMetadata(context.Background(), env.store, rec); err != nil {
			t.Fatal(err)
		}
		_, err = fetchTestPR(f, 1)
		if kind, _ := cascade.KindOf(err); kind != cascade.KindPolicyDenied || !strings.Contains(err.Error(), "is not an enabled, trusted process plugin") {
			t.Fatalf("demoted: %v", err)
		}
		if n := countActions(t, env, "plugin.launch", ""); n != 0 {
			t.Fatalf("demoted plugin produced %d launch records", n)
		}
	}
}

// TestPRDiffWiringStopsHandle: the handle is closed after a success and
// after a plugin error, and a plugin error returns no diff.
func TestPRDiffWiringStopsHandle(t *testing.T) {
	_, f := replayPRFetcher(t)
	var handles []*TrustedHandle
	f.provider = func(ctx context.Context, id string) (*TrustedHandle, error) {
		h, err := LaunchInstalled(ctx, id)
		handles = append(handles, h)
		return h, err
	}
	if _, err := fetchTestPR(f, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := fetchTestPR(f, 2); err == nil || got != nil {
		t.Fatalf("plugin error: diff %q err %v, want a refusal", got, err)
	}
	if len(handles) != 2 {
		t.Fatalf("launches = %d, want 2", len(handles))
	}
	for i, h := range handles {
		if h == nil || !h.closed.Load() {
			t.Fatalf("fetch %d left its handle open", i+1)
		}
		if _, err := h.Call(context.Background(), prDiffMethod, nil); !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("call after close: %v", err)
		}
	}
}
