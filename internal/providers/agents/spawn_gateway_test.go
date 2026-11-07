//go:build integration

// Purpose: tests for a gateway spawn through the shim: the per-spawn proxy
// admits the account's host for that spawn alone, and the gateway value stays
// out of relayed output, the journal, logs, the runtime profile and effects.
// Constraints: the only sockets are the client side of the shim's own loopback
// proxy; its upstream dial is an in-memory pipe, so no host is ever resolved.
// SPORT: internal.providers.agents tests (ADD) - P1-GWY-05.

package agents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/providers/agents/egressproxy"
	"github.com/acamarata/cascade/pkg/provider"
)

const gwBaseHost = egressproxy.Destination("base.invalid:443")

func init() { gwInstallDial = func(f *gwFix) { f.shim.dial = f.dial } }

func (f *gwFix) dial(_ context.Context, _, addr string) (net.Conn, error) {
	f.mu.Lock()
	f.dials = append(f.dials, addr)
	f.mu.Unlock()
	near, far := net.Pipe()
	f.t.Cleanup(func() { _ = far.Close() })
	return near, nil
}

// gwConnect sends one CONNECT through the spawn's proxy and returns the status.
func gwConnect(t *testing.T, spec provider.AgentJobSpec, target egressproxy.Destination) int {
	t.Helper()
	var raw string
	for _, kv := range spec.Env.Driver {
		if v, ok := strings.CutPrefix(kv, "HTTPS_PROXY="); ok {
			raw = v
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("the proxy URL does not parse: %v", err)
	}
	c, err := net.DialTimeout("tcp", u.Host, 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	pw, _ := u.User.Password()
	proof := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pw))
	head := "CONNECT " + string(target) + " HTTP/1.1\r\nHost: " + string(target) + "\r\nProxy-Authorization: Basic " + proof + "\r\n\r\n"
	if _, err := c.Write([]byte(head)); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// gwJournal is every audit and effect record, as JSON.
func gwJournal(t *testing.T, log *audit.Log) string {
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

// gwSpawnConcurrently spawns every fixture at once and releases each handle
// at cleanup, so the shared allowlist is exercised by concurrent spawns.
func gwSpawnConcurrently(t *testing.T, fixes ...*gwFix) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make([]error, len(fixes))
	handles := make([]SpawnHandle, len(fixes))
	for i, f := range fixes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handles[i], errs[i] = f.shim.Spawn(context.Background(), f.req)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
		h := handles[i]
		t.Cleanup(func() { _ = h.Release(context.Background()) })
	}
}

func TestSpawnGatewayEgressAllowsOnlyAccountHost(t *testing.T) {
	base := make(egressproxy.DestinationAllowlist, 1, 8) // spare room, so an in-place append would be seen
	base[0] = gwBaseHost
	shared := map[egressproxy.DriverID]egressproxy.DestinationAllowlist{egressproxy.DriverClaude: base}

	gw := newGatewayFix(t, gwCanary(t))
	gw.deps.EgressAllow = shared
	gw.rebuild()
	plain := &gwFix{shimFix: newShimFix(t)}
	plain.deps.EgressAllow = shared
	plain.rebuild()

	gwSpawnConcurrently(t, gw, plain)
	gwSpec, plainSpec := gw.drv.specs[0], plain.drv.specs[0]
	for _, c := range []struct {
		name string
		spec provider.AgentJobSpec
		dest egressproxy.Destination
		want int
	}{
		{"gateway spawn, account host", gwSpec, gwHost, http.StatusOK},
		{"gateway spawn, base host", gwSpec, gwBaseHost, http.StatusOK},
		{"gateway spawn, other host", gwSpec, "other.invalid:443", http.StatusForbidden},
		{"api_key spawn, account host", plainSpec, gwHost, http.StatusForbidden},
		{"api_key spawn, base host", plainSpec, gwBaseHost, http.StatusOK},
	} {
		if got := gwConnect(t, c.spec, c.dest); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
	if j := gwJournal(t, gw.log); !strings.Contains(j, "other.invalid:443") || !strings.Contains(j, string(egressproxy.ReasonDestinationNotAllowed)) {
		t.Fatalf("the gateway proxy journal lacks the refusal: %s", j)
	}
	if j := gwJournal(t, plain.log); !strings.Contains(j, string(gwHost)) || !strings.Contains(j, string(egressproxy.ReasonDestinationNotAllowed)) {
		t.Fatalf("the api_key proxy journal lacks the refusal: %s", j)
	}
	if slices.Contains(plain.dials, string(gwHost)) || slices.Contains(gw.dials, "other.invalid:443") {
		t.Fatalf("a refused destination was dialed: %v, %v", gw.dials, plain.dials)
	}
	if !slices.Equal(shared[egressproxy.DriverClaude], egressproxy.DestinationAllowlist{gwBaseHost}) || base[:2][1] != "" {
		t.Fatalf("the shared allowlist changed: %v", base[:2])
	}
	for _, kv := range plainSpec.Env.Driver {
		if name, _, _ := strings.Cut(kv, "="); name == gwAuthName || name == gwBaseName || name == gwManagedName {
			t.Fatalf("the api_key spawn carries %s", name)
		}
	}
}

// gwLogs is a slog sink read after the proxies have closed.
type gwLogs struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *gwLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *gwLogs) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// gwRelayed runs a real child that prints the gateway variable on both streams
// and sends each line, and the encoded spellings, through the output filter.
func gwRelayed(t *testing.T, spec provider.AgentJobSpec, value string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `printenv "$0"; printenv "$0" >&2`, gwAuthName)
	cmd.Env, cmd.Dir = spec.Env.Driver, spec.WorktreeRoot
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	if !bytes.Contains(raw, []byte(value)) {
		t.Fatal("the child did not see the gateway value, so the filter proved nothing")
	}
	var out strings.Builder
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for _, line := range append(lines, gwForms(value)...) {
		got, err := spec.Filter([]byte("said " + line))
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		out.Write(got)
		out.WriteByte('\n')
	}
	return out.String()
}

func gwForms(s string) []string {
	return []string{s, base64.StdEncoding.EncodeToString([]byte(s)), hex.EncodeToString([]byte(s))}
}

// gwProfileText is every file name and file body under the spawn's profile.
func gwProfileText(t *testing.T, root string) string {
	t.Helper()
	var out strings.Builder
	files := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out.WriteString(p + "\n")
		if d.Type().IsRegular() {
			body, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			files++
			out.Write(body)
		}
		return nil
	})
	if err != nil || files == 0 {
		t.Fatalf("scanned %d profile files: %v", files, err)
	}
	return out.String()
}

func TestGatewayCustodyRedTeam(t *testing.T) {
	logs := &gwLogs{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	canary := gwCanary(t)
	f := newGatewayFix(t, canary)
	var relayed string
	f.drv.before = func(spec provider.AgentJobSpec) { relayed = gwRelayed(t, spec, canary) }
	h, err := f.shim.Spawn(context.Background(), f.req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	spec := f.drv.specs[0]
	if !envHas(spec.Env.Driver, gwAuthName+"="+canary) || !envHas(spec.Env.Driver, gwBaseName+"="+gwBaseURL) {
		t.Fatal("the driver environment lacks the gateway pairs")
	}
	profile := gwProfileText(t, filepath.Dir(spec.Env.Home))
	if err := h.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if !strings.Contains(relayed, RedactedTag) {
		t.Fatalf("relayed output lacks %s: %q", RedactedTag, relayed)
	}
	for where, text := range map[string]string{
		"relayed output": relayed, "tool env": strings.Join(spec.Env.Tool, "\n"), "journal and effects": gwJournal(t, f.log),
		"logs": logs.String(), "runtime profile": profile,
	} {
		for _, form := range gwForms(canary) {
			if strings.Contains(text, form) {
				t.Errorf("%s holds a spelling of the gateway value", where)
			}
		}
	}
}
