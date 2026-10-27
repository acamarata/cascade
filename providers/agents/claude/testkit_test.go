//go:build !windows

package claude

// Purpose: the shared test fixture: a real git worktree, a 0700 runtime
//   home, a hook pack, and a POSIX shell script standing in for the vendor
//   harness (answers --version, prints an init line for the stream-json
//   probe and then sleeps until killed, and runs a per-test job body).
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; shared by
//   every _test.go here and split out for the 300-line cap. No real harness
//   runs; every path is under t.TempDir(); the fake home sets HOME only in
//   the child's SpawnEnv, never in this process.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/provider"
)

type fx struct {
	t                             *testing.T
	dir, root, home, vendor, bin  string
	pack, anyRan, jobRan, argvOut string
	versionBlock                  string
	initDefault, initBypass       []byte
	body                          string
}

// newFx builds a fixture whose default job prints the real (scrubbed)
// result object and exits 0.
func newFx(t *testing.T) *fx {
	t.Helper()
	dir := t.TempDir()
	homeBase, err := os.MkdirTemp("", "cch") // not named after the test: no ',' or '(' in HOME
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(homeBase) })
	f := &fx{t: t, dir: dir, root: newGitWorktree(t), home: filepath.Join(homeBase, "home"), bin: filepath.Join(dir, "bin")}
	f.vendor = filepath.Join(f.home, "vendor")
	f.pack = filepath.Join(dir, "pack")
	f.anyRan, f.jobRan, f.argvOut = filepath.Join(dir, "any-ran"), filepath.Join(dir, "job-ran"), filepath.Join(dir, "argv")
	mustMkdir(t, f.vendor)
	mustMkdir(t, f.bin)
	mustMkdir(t, filepath.Join(f.pack, "hooks"))
	mustWrite(t, filepath.Join(f.pack, "settings.json"), "{}\n")
	mustWrite(t, filepath.Join(f.pack, "hooks", "h.sh"), "#!/bin/sh\n")
	f.versionBlock = `echo "2.1.273 (vendor harness)"; exit 0`
	f.initDefault = initLine(t, func(m map[string]any) { m["permissionMode"] = "default" })
	f.initBypass = initLine(t, func(m map[string]any) { m["permissionMode"] = "bypassPermissions" })
	f.body = "printf '%s\\n' '" + resultLine(t, nil) + "'\n"
	return f
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newGitWorktree returns a fresh git repository (the leased tree).
func newGitWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet"}, {"config", "user.email", "t@example.invalid"}, {"config", "user.name", "t"}} {
		gitIn(t, dir, args...)
	}
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// initLine returns the committed init capture as one JSON line, with its
// MCP servers connected and mutate applied.
func initLine(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(readTestdata(t, "claude_headless_init.json"), &m); err != nil {
		t.Fatal(err)
	}
	m["mcp_servers"] = []any{map[string]any{"name": "connector-1", "status": "connected"}}
	if mutate != nil {
		mutate(m)
	}
	b, _ := json.Marshal(m)
	return b
}

// resultLine returns the committed result capture as one JSON line.
func resultLine(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(readTestdata(t, "protocol/1/result.json"), &m); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func readTestdata(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// script writes the fake harness and returns its path.
func (f *fx) script() string {
	f.t.Helper()
	def, byp := filepath.Join(f.dir, "init-default"), filepath.Join(f.dir, "init-bypass")
	mustWrite(f.t, def, string(f.initDefault)+"\n")
	mustWrite(f.t, byp, string(f.initBypass)+"\n")
	body := "#!/bin/sh\ntouch " + f.anyRan + "\n" +
		"for a in \"$@\"; do if [ \"$a\" = --version ]; then " + f.versionBlock + "; fi; done\n" +
		"case \" $* \" in *\" stream-json \"*) case \" $* \" in *\" bypassPermissions \"*) cat " + byp +
		";; *) cat " + def + ";; esac; exec sleep 30;; esac\n" +
		"touch " + f.jobRan + "\nprintf '%s\\n' \"$@\" > " + f.argvOut + "\n" + f.body
	p := filepath.Join(f.bin, "harness")
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// fakeKeyValue is an obviously synthetic key-variable value; no real
// credential appears in any test.
const fakeKeyValue = "synthetic-key-value-0001"

// identity is the pass-through per-spawn OutputFilter.
func identity(b []byte) ([]byte, error) { return b, nil }

// spec is a valid job for this fixture: its WorktreeRoot is the leased
// tree and its Filter is the identity.
func (f *fx) spec() provider.AgentJobSpec {
	return provider.AgentJobSpec{
		Prompt: "hello", DataClass: provider.DataClassInternal, WorktreeRoot: f.root, Filter: identity,
		Envelope: testEnvelope(f.root),
		Env: provider.SpawnEnv{
			Driver: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + f.home, VendorConfigDirVar + "=" + f.vendor,
				KeyVarOAuthToken + "=" + fakeKeyValue},
			Tool: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + f.home},
			Home: f.home,
		},
	}
}

type fakeProfile struct {
	prof Profile
	err  error
}

func (p fakeProfile) Resolve(context.Context) (Profile, error) { return p.prof, p.err }

type fakeLeaser struct {
	lease Lease
	err   error
}

func (l fakeLeaser) Lease(context.Context, provider.WorktreeScope) (Lease, error) {
	return l.lease, l.err
}

func (f *fx) profile() Profile {
	return Profile{VendorAuthVar: VendorConfigDirVar, RuntimeHome: f.home, CredentialClass: CredentialClassOAuthToken, KeyVar: KeyVarOAuthToken,
		Posture: provider.NewCompliancePosture([]string{"oauth"}, true, true, []string{"agent"}, "steady", false)}
}

// config is a valid Config over this fixture (500ms cancel grace).
func (f *fx) config() Config {
	def := provider.NewDriverDefaults()
	def.CancelGrace = 500 * time.Millisecond
	return Config{Binary: f.script(), HookPackPath: f.pack, Profile: fakeProfile{prof: f.profile()},
		Worktree: fakeLeaser{lease: Lease{Root: f.root, GitDir: filepath.Join(f.root, ".git")}},
		Defaults: def, ProbeTimeout: 5 * time.Second, Clock: testClock{}, ContentMatch: processMatch(f.t)}
}

func (f *fx) driver(cfg Config) *Driver {
	f.t.Helper()
	d, err := New(cfg)
	if err != nil {
		f.t.Fatalf("New: %v", err)
	}
	// Registered after newFx's t.TempDir, so it runs before that RemoveAll.
	f.t.Cleanup(func() { settleJobs(f.t, d) })
	return d
}

// run spawns spec and collects it with a 20s bound.
func run(t *testing.T, d *Driver, spec provider.AgentJobSpec) (provider.SpawnResult, provider.CollectResult, error) {
	t.Helper()
	res, err := d.Spawn(context.Background(), spec)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cr, cerr := d.Collect(ctx, res.JobID)
	return res, cr, cerr
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

// eventually polls cond every 10ms until it holds, failing after 5s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline, tick := time.NewTimer(5*time.Second), time.NewTicker(10*time.Millisecond)
	defer deadline.Stop()
	defer tick.Stop()
	for !cond() {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		case <-tick.C:
		}
	}
}

// readPid waits for a pid file and returns its pid.
func readPid(t *testing.T, p string) int {
	t.Helper()
	pid := 0
	eventually(t, "pid file "+filepath.Base(p), func() bool {
		b, err := os.ReadFile(p)
		n, cerr := strconv.Atoi(strings.TrimSpace(string(b)))
		pid = n
		return err == nil && cerr == nil && n > 0
	})
	return pid
}

func argvOf(t *testing.T, f *fx) []string {
	t.Helper()
	b, err := os.ReadFile(f.argvOut)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func has(s []string, tok string) bool {
	for _, x := range s {
		if x == tok {
			return true
		}
	}
	return false
}

func contains(b []byte, s string) bool { return bytes.Contains(b, []byte(s)) }

// processMatch is the host's matcher: the process's one secret detector.
func processMatch(t testing.TB) func([]byte) (string, bool) {
	t.Helper()
	d, err := secrets.ProcessDetector()
	if err != nil {
		t.Fatalf("secrets.ProcessDetector: %v", err)
	}
	return func(content []byte) (string, bool) {
		hits := d.ScanCertain(content)
		if len(hits) == 0 {
			return "", false
		}
		return string(hits[0].Class), true
	}
}
