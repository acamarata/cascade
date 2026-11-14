//go:build integration && !windows

// Purpose: the gateway Epic's offline acceptance: a devkeys, CGO-free binary
// under a fresh HOME and CASCADE_HOME against two loopback fake gateways.
// Legs [0] accept/add/list/run, [1] retry/spill/health, [2] quota; canary token.
// SPORT: cmd/cascade gateway acceptance (ADD).

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
)

const gwTokenEnv, gwReply = "FAKEGW_TOKEN", "fake gateway reply ok"

// gwUtil matches the captured utilization in any JSON; gwFive the widget row.
var gwUtil, gwFive = regexp.MustCompile(`"utilization_pct":\s*37\.5`), regexp.MustCompile(`"ref":"gwf",[^}]*"five_hour":\{"utilization_pct":37\.5`)

func gwBuild(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	bin, fake := filepath.Join(dir, "cascade"), filepath.Join(dir, "fakegateway")
	for _, args := range [][]string{{"-tags", "devkeys", "-o", bin, "."},
		{"-o", fake, "../../internal/providers/dispatch/testdata/fakegateway"}} {
		cmd := exec.Command("go", append([]string{"build"}, args...)...)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build: %v\n%s", err, out)
		}
	}
	return bin, fake
}

type gwRig struct {
	bin, home, cfg, sock string
	env                  []string
	daemon               *exec.Cmd
}

func newGWRig(t *testing.T, bin, token string) *gwRig {
	t.Helper()
	home := t.TempDir()
	ch := filepath.Join(home, ".cascade")
	if err := os.MkdirAll(ch, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &gwRig{bin: bin, home: home, cfg: filepath.Join(ch, "config.toml"), sock: filepath.Join(t.TempDir(), "d.sock")}
	r.env = []string{"HOME=" + home, "USERPROFILE=" + home, "CASCADE_HOME=" + ch, "CASCADE_SOCKET=" + r.sock,
		"CASCADE_TEST_FILE_VAULT=1", "PATH=/usr/bin:/bin:/usr/sbin:/sbin", gwTokenEnv + "=" + token}
	t.Cleanup(r.stop)
	return r
}

func (r *gwRig) cli(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin, args...) //nolint:gosec // built by gwBuild.
	cmd.Dir, cmd.Env = r.home, r.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *gwRig) must(t *testing.T, args ...string) string {
	t.Helper()
	out, err := r.cli(args...)
	if err != nil {
		t.Fatalf("cascade %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (r *gwRig) start(t *testing.T) {
	t.Helper()
	log := filepath.Join(r.home, "daemon.log")
	f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	r.daemon = exec.Command(r.bin, "daemon", "run") //nolint:gosec // built by gwBuild.
	r.daemon.Dir, r.daemon.Env, r.daemon.Stdout, r.daemon.Stderr = r.home, r.env, f, f
	if err := r.daemon.Start(); err != nil {
		t.Fatalf("daemon run: %v", err)
	}
	var out string
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if out, err = r.cli("status"); err == nil {
			return
		}
	}
	raw, _ := os.ReadFile(log) //nolint:gosec // under t.TempDir.
	t.Fatalf("the daemon never answered `cascade status` (%v):\n%s\ndaemon log:\n%s", err, out, raw)
}

// stop SIGTERMs the daemon so it releases its scheduler lease; kill at 30 s.
func (r *gwRig) stop() {
	if r.daemon == nil || r.daemon.Process == nil {
		return
	}
	d := r.daemon
	kill := time.AfterFunc(30*time.Second, func() { _ = d.Process.Kill() })
	_ = d.Process.Signal(syscall.SIGTERM)
	_ = d.Wait()
	kill.Stop()
	r.daemon = nil
}

func (r *gwRig) widget(t *testing.T) string {
	t.Helper()
	var w json.RawMessage
	if err := client.New(r.sock, client.UnixDialer, 30*time.Second).Do(context.Background(), "status.widget", nil, &w); err != nil {
		t.Fatalf("status.widget: %v", err)
	}
	return string(w)
}

type fakeGW struct{ url, control, journal string }

func startFake(t *testing.T, bin, token string) *fakeGW {
	t.Helper()
	dir := t.TempDir()
	f := &fakeGW{control: dir, journal: filepath.Join(dir, "journal.jsonl")}
	cmd := exec.Command(bin, "-control", dir, "-journal", f.journal) //nolint:gosec // built by gwBuild.
	cmd.Env = []string{gwTokenEnv + "=" + token}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake gateway: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "listening http://127.0.0.1:") {
		t.Fatalf("fake gateway banner %q: %v", line, err)
	}
	f.url = strings.TrimSpace(strings.TrimPrefix(line, "listening "))
	return f
}

func (f *fakeGW) set(t *testing.T, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.control, name), []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeGW) journalText() string {
	raw, _ := os.ReadFile(f.journal)
	return string(raw)
}

// count counts bearer-matched journal lines (map keys marshal sorted).
func (f *fakeGW) count(method, path string, status int) int {
	return strings.Count(f.journalText(), fmt.Sprintf(`{"bearer_matched":true,"method":%q,"path":%q,"status":%d}`, method, path, status))
}

// gwAccount is one [gateways.<name>] table with the endpoint usage source.
func gwAccount(name, url string) string {
	return "[gateways." + name + "]\nbase_url = \"" + url + "\"\nkey_env = \"" + gwTokenEnv + "\"\n\n" +
		"[gateways." + name + ".usage]\nsource = \"endpoint\"\nwindow = \"5h\"\npath = \"/usage\"\n" +
		"used_pointer = \"/five_hour/used\"\nlimit_pointer = \"/five_hour/limit\"\n" +
		"utilization_pointer = \"/five_hour/utilization\"\nresets_at_pointer = \"/five_hour/resets_at\"\n\n"
}

// gwAdmit writes cfg, enrolls elevation, accepts the loosening and restarts.
func gwAdmit(t *testing.T, r *gwRig, cfg string) {
	t.Helper()
	if err := os.WriteFile(r.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r.must(t, "elevate-helper", "--enroll")
	r.start(t)
	r.must(t, "config", "reload", "--accept-loosening")
	r.stop()
	r.start(t)
}

func gwRun(t *testing.T, r *gwRig) string {
	t.Helper()
	out := r.must(t, "run", "--task", "chat", "--sensitivity", "public", "--input", "user:reply ok", "--json")
	if !strings.Contains(out, gwReply) {
		t.Fatalf("cascade run did not return the fake reply:\n%s", out)
	}
	return out
}

func eventually(t *testing.T, bound time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(bound); !cond(); time.Sleep(2 * time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within %s", what, bound)
		}
	}
}

func TestGatewayAcceptanceFake(t *testing.T) {
	bin, fake := gwBuild(t)
	token := rand.Text()
	a, b := startFake(t, fake, token), startFake(t, fake, token)
	r := newGWRig(t, bin, token)
	gwAdmit(t, r, gwAccount("gwf", a.url)+gwAccount("gwf2", b.url))
	t.Run("accept_add_list_run", func(t *testing.T) { gwLeg0(t, r, a) })
	t.Run("transient_retry_spill_health", func(t *testing.T) { gwLeg1(t, r, a, b) })
	t.Run("quota_window", func(t *testing.T) { gwLeg2(t, r, a) })
	if strings.Contains(a.journalText()+b.journalText(), token) {
		t.Fatal("the token reached a fake gateway journal")
	}
}

func gwLeg0(t *testing.T, r *gwRig, a *fakeGW) {
	r.must(t, "provider", "add", "gwf", "--gateway", "gwf")
	if list := r.must(t, "provider", "list", "--json"); !strings.Contains(list, `"gwf"`) {
		t.Fatalf("provider list lacks gwf:\n%s", list)
	}
	t.Logf("run: %s", gwRun(t, r))
	if a.count("GET", "/v1/models", 200) < 1 || a.count("POST", "/v1/messages", 200) < 1 {
		t.Fatalf("journal lacks a bearer-matched catalog and message: %s", a.journalText())
	}
}

func gwLeg1(t *testing.T, r *gwRig, a, b *fakeGW) {
	before := a.count("POST", "/v1/messages", 529)
	a.set(t, "faults", "2 x overloaded then ok")
	t.Logf("retry run: %s", gwRun(t, r))
	if got := a.count("POST", "/v1/messages", 529) - before; got != 2 {
		t.Fatalf("fake served %d transient failures, want 2: %s", got, a.journalText())
	}
	r.must(t, "provider", "add", "gwf2", "--gateway", "gwf2")
	a.set(t, "faults", "always overloaded")
	failedAt := time.Now()
	t.Logf("spill run: %s", gwRun(t, r))
	if b.count("POST", "/v1/messages", 200) < 1 {
		t.Fatalf("no spill to the second provider: %s", b.journalText())
	}
	health := func(want string) bool {
		out, _ := r.cli("provider", "health", "--json")
		t.Logf("health: %s", out)
		return gwHealthOf(out, "gwf") == want
	}
	eventually(t, 30*time.Second, "gwf degraded", func() bool { return health("degraded") })
	doctor, _ := r.cli("doctor")
	t.Logf("doctor: %s", doctor)
	time.Sleep(time.Until(failedAt.Add(180 * time.Second)))
	if !health("degraded") {
		t.Fatal("gwf recovered while its gateway still fails")
	}
	a.set(t, "faults", "ok")
	eventually(t, 180*time.Second, "gwf healthy again", func() bool { return health("healthy") })
}

// gwHealthOf is the health of provider name in `provider health --json`.
func gwHealthOf(out, name string) string {
	var env struct {
		Data struct {
			Providers []struct{ Name, Health string }
		}
	}
	// The first document is the report; a failing exit appends an error one.
	if i := strings.Index(out, "{"); i < 0 || json.NewDecoder(strings.NewReader(out[i:])).Decode(&env) != nil {
		return ""
	}
	for _, p := range env.Data.Providers {
		if p.Name == name {
			return p.Health
		}
	}
	return ""
}

func gwLeg2(t *testing.T, r *gwRig, a *fakeGW) {
	capacity := func() string { out, _ := r.cli("fleet", "capacity", "--json"); t.Logf("capacity: %s", out); return out }
	eventually(t, 150*time.Second, "quota window in fleet capacity", func() bool {
		return gwUtil.MatchString(capacity())
	})
	if w := r.widget(t); !gwFive.MatchString(w) {
		t.Fatalf("widget five_hour differs from the quota window: %s", w)
	}
	a.set(t, "usage", "fail")
	eventually(t, 200*time.Second, "stale quota window", func() bool {
		return regexp.MustCompile(`"stale":\s*true`).MatchString(capacity())
	})
	if w := r.widget(t); !regexp.MustCompile(`"ref":"gwf",[^}]*"five_hour":\{"utilization_pct":null`).MatchString(w) {
		t.Fatalf("widget kept a stale utilization: %s", w)
	}
}
