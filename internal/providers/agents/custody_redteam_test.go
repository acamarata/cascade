//go:build security

// Purpose: red-team tests for the shim's custody surfaces. A sentinel seeded
// through the daemon-environment key path must not reach any captured output
// line, the tool environment, the journal or the daemon log, in exact, base64
// and hex form; a tree holding an out-of-scope file or an escaping auth-dir
// link never reaches a spawn.
// Constraints: the "child" is a real sh process run with the exact
// spec.Env.Driver; its stdout and stderr lines go through spec.Filter.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-03.

package agents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	sentinel      = "CASCADE-TEST-SENTINEL-xyz123"
	otherSentinel = "CASCADE-TEST-SENTINEL-other456"
	childScript   = `printf '%s\n' "$ANTHROPIC_API_KEY"
printf '%s\n' "$ANTHROPIC_API_KEY" >&2
printf '%s' "$ANTHROPIC_API_KEY" | base64
printf '%s' "$ANTHROPIC_API_KEY" | od -An -tx1 | tr -d ' \n'; echo
printf '%s' "$ANTHROPIC_API_KEY" > "$HOME/written.txt"`
)

// lockedBuf is a log sink safe for the proxy's goroutines.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// forms returns the exact, base64 and hex spellings of s.
func forms(s string) []string {
	return []string{s, base64.StdEncoding.EncodeToString([]byte(s)), base64.RawURLEncoding.EncodeToString([]byte(s)), hex.EncodeToString([]byte(s))}
}

func absentEverywhere(t *testing.T, where, text string, secret string) {
	t.Helper()
	for _, f := range forms(secret) {
		if strings.Contains(text, f) {
			t.Fatalf("%s holds %q", where, f)
		}
	}
}

// runChild runs childScript with env, sends every stdout and stderr line
// through filter and returns the filtered lines.
func runChild(t *testing.T, spec provider.AgentJobSpec) []string {
	t.Helper()
	cmd := exec.Command("sh", "-c", childScript)
	cmd.Env, cmd.Dir = spec.Env.Driver, spec.WorktreeRoot
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("child: %v (%s)", err, errOut.String())
	}
	var lines []string
	for _, stream := range []*bytes.Buffer{&out, &errOut} {
		sc := bufio.NewScanner(stream)
		for sc.Scan() {
			got, err := spec.Filter([]byte(sc.Text()))
			if err != nil {
				t.Fatalf("filter(%q): %v", sc.Text(), err)
			}
			lines = append(lines, string(got))
		}
	}
	return lines
}

// journalText is every audit record, as JSON.
func journalText(t *testing.T, log *audit.Log) string {
	t.Helper()
	page, err := log.Query(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(page.Records)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func captureLog(t *testing.T) *lockedBuf {
	t.Helper()
	buf := &lockedBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func TestRedTeamKeyFromDaemonEnvNeverLeaks(t *testing.T) {
	logs := captureLog(t)
	f := newShimFix(t)
	f.env[shimKeyEnv], f.env["TEST_OTHER_KEY"] = sentinel, otherSentinel
	f.deps.Auth[HarnessID("codex")] = DriverAuth{Harness: HarnessID("codex"), Class: CredentialAPIKey, KeyEnv: "TEST_OTHER_KEY"}
	f.build()
	var lines []string
	f.drv.before = func(spec provider.AgentJobSpec) { lines = runChild(t, spec) }
	h, err := f.shim.Spawn(context.Background(), f.req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	spec := f.drv.specs[0]
	keyVar, _ := KeyVarFor(claudeRow, CredentialAPIKey)
	if !containsExactly(spec.Env.Driver, keyVar+"="+sentinel) {
		t.Fatalf("the key is not in the Driver slice as %s", keyVar)
	}
	for _, kv := range spec.Env.Driver {
		if !strings.HasPrefix(kv, keyVar+"=") {
			absentEverywhere(t, "Driver slice entry "+kv, kv, sentinel)
		}
	}
	if err := h.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if len(lines) < 4 {
		t.Fatalf("child produced %d lines, want the echo, the stderr echo, base64 and hex", len(lines))
	}
	all := strings.Join(lines, "\n") + "\n" + strings.Join(spec.Env.Tool, "\n") + "\n" + journalText(t, f.log) + "\n" + logs.String()
	for _, secret := range []string{sentinel, otherSentinel} {
		absentEverywhere(t, "output, Tool slice, journal or log", all, secret)
	}
	absentEverywhere(t, "the Driver slice", strings.Join(spec.Env.Driver, "\n"), otherSentinel)
	if !strings.Contains(strings.Join(lines, "\n"), RedactedTag) {
		t.Fatalf("lines %q lack %s: the unshaped key was not redacted by value", lines, RedactedTag)
	}
	if written, rerr := os.ReadFile(filepath.Join(spec.Env.Home, "written.txt")); rerr == nil && string(written) != sentinel {
		t.Fatalf("the child's file holds %q", written)
	}
}

func containsExactly(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// seededVault holds one stored value.
type seededVault struct{ name, value string }

func (v seededVault) List(context.Context) ([]string, error) { return []string{v.name}, nil }
func (v seededVault) Get(_ context.Context, n string) ([]byte, error) {
	if n != v.name {
		return nil, cascade.New(cascade.KindNotFound, "not stored")
	}
	return []byte(v.value), nil
}

// TestRedTeamVaultSeededSentinel is the regression for the vault path: a
// value the vault holds is substituted by the engine's exact-value pass.
func TestRedTeamVaultSeededSentinel(t *testing.T) {
	det, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), seededVault{"stored_sentinel", sentinel}, det)
	if err != nil {
		t.Fatal(err)
	}
	filter := WrapDriverOutput(engine, provider.SensitivityInternal, nil, func(string) {})
	out, ferr := filter([]byte("the model said " + sentinel))
	if ferr != nil || strings.Contains(string(out), sentinel) {
		t.Fatalf("vault-seeded sentinel passed: %q, %v", out, ferr)
	}
}

func TestRedTeamLeakMatrix(t *testing.T) {
	t.Run("sentinel as the key", func(t *testing.T) {
		logs := captureLog(t)
		f := newShimFix(t)
		f.env[shimKeyEnv] = sentinel
		var lines []string
		f.drv.before = func(spec provider.AgentJobSpec) { lines = runChild(t, spec) }
		h, err := f.shim.Spawn(context.Background(), f.req)
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		_ = h.Release(context.Background())
		spec := f.drv.specs[0]
		text := strings.Join(lines, "\n") + strings.Join(spec.Env.Tool, "\n") + journalText(t, f.log) + logs.String()
		absentEverywhere(t, "captured output, Tool slice, journal or log", text, sentinel)
	})
	t.Run("out of scope file", func(t *testing.T) {
		f := newShimFix(t)
		writeFile(t, filepath.Join(f.root, "notes", "private.txt"), "private", 0o600)
		_, err := f.shim.Spawn(context.Background(), f.req)
		f.refused(err)
		if !cascade.HasKind(err, cascade.KindPolicyDenied) {
			t.Fatalf("Spawn = %v, want KindPolicyDenied", err)
		}
	})
	t.Run("auth dir symlink", func(t *testing.T) {
		f := newShimFix(t)
		authDir := t.TempDir()
		writeFile(t, filepath.Join(authDir, ".credentials.json"), "{}", 0o600)
		symlinkOrSkip(t, authDir, filepath.Join(f.root, "src", ".claude-auth"))
		_, err := f.shim.Spawn(context.Background(), f.req)
		f.refused(err)
		if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("Spawn = %v, want KindPolicyDenied: escapes the worktree", err)
		}
	})
}
