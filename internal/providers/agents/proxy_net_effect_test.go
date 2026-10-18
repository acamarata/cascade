//go:build integration && security

package agents

// Purpose: the proxied dial's effect legs (integration+security): the intent
// is stored before the dial and the confirm after it, and a child that dies
// between them leaves an intent that recovery marks unknown-outcome and never
// re-dials. Split from proxy_net_test.go under the 300-line file cap.
//
// Constraints: loopback only; the crash child follows the effect-record re-exec recipe
// (CASCADE_EFFECT_CHILD re-exec).

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/providers/agents/egressproxy"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

// orderedEffects wraps the real effect log and records every call and every
// dial in one sequence.
type orderedEffects struct {
	audit.EffectWriter
	mu  sync.Mutex
	seq []string
}

func (o *orderedEffects) note(s string) {
	o.mu.Lock()
	o.seq = append(o.seq, s)
	o.mu.Unlock()
}

func (o *orderedEffects) BeginEffect(ctx context.Context, r audit.EffectRequest) (audit.EffectHandle, error) {
	h, err := o.EffectWriter.BeginEffect(ctx, r)
	if err == nil {
		o.note("intent")
	}
	return h, err
}

func (o *orderedEffects) ConfirmEffect(ctx context.Context, h audit.EffectHandle, outcome string) (audit.Record, error) {
	rec, err := o.EffectWriter.ConfirmEffect(ctx, h, outcome)
	if err == nil {
		o.note("confirm:" + outcome)
	}
	return rec, err
}

func (o *orderedEffects) FailEffect(ctx context.Context, h audit.EffectHandle, reason string) (audit.Record, error) {
	rec, err := o.EffectWriter.FailEffect(ctx, h, reason)
	if err == nil {
		o.note("fail:" + reason)
	}
	return rec, err
}

// dialThroughProxy opens one tunnel to netAllowedDest and closes it.
func dialThroughProxy(t *testing.T, p *egressproxy.Proxy) {
	t.Helper()
	addr, auth := proxyAddrAuth(t, p)
	c, br, code := connect(t, addr, netAllowedDest, auth)
	if code != http.StatusOK {
		t.Fatalf("tunnel status %d, want 200", code)
	}
	_, _ = io.WriteString(c, "x\n")
	_, _ = br.ReadString('\n')
	_ = c.Close()
}

func TestProxiedDialEffectRecordsIntentConfirm(t *testing.T) {
	if path := os.Getenv(netChildEnv); path != "" {
		runNetChild(path)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Ordering: the intent is stored before the dial, the confirm after it.
	log, el := netAudit(t, storetest.NewMemStore())
	oe := &orderedEffects{EffectWriter: el}
	rd := &routeDial{routes: map[string]string{netAllowedDest: echo(t)}, after: func() { oe.note("dial") }}
	p, err := startSpawnProxy(ctx, egressproxy.DriverCodex, netJob, egressproxy.DestinationAllowlist{netAllowedDest},
		ProxyJournal(log, oe), rd.dial)
	if err != nil {
		t.Fatalf("startSpawnProxy: %v", err)
	}
	dialThroughProxy(t, p)
	_ = p.Close()
	oe.mu.Lock()
	seq := strings.Join(oe.seq, ",")
	oe.mu.Unlock()
	if seq != "intent,dial,confirm:connected" {
		t.Fatalf("effect order = %q, want intent,dial,confirm:connected", seq)
	}
	if pending, err := el.PendingEffects(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("PendingEffects after a confirmed dial = %+v, %v; want none", pending, err)
	}

	// Crash leg: the child exits between the dial and the confirm.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cascade.db")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProxiedDialEffectRecordsIntentConfirm$", "-test.count=1")
	cmd.Env = append(os.Environ(), netChildEnv+"="+dbPath)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != netChildExit {
		t.Fatalf("child = %v, want exit status %d; output:\n%s", err, netChildExit, out)
	}
	assertNetCrashRecovered(ctx, t, dbPath, dir)
}

// runNetChild dials once through a real proxy and dies inside the dial's
// success path, after the intent was committed and before the confirm.
func runNetChild(dbPath string) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		os.Exit(4)
	}
	log := audit.New(store, testkit.NewFrozenClock(netInstant), nil)
	el, err := audit.NewEffectLog(log)
	if err != nil {
		os.Exit(5)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(6)
	}
	rd := &routeDial{routes: map[string]string{netAllowedDest: l.Addr().String()}, after: func() {
		f, err := os.OpenFile(filepath.Join(filepath.Dir(dbPath), "dials.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(7)
		}
		_, _ = f.WriteString("dialed\n")
		_ = f.Close()
		os.Exit(netChildExit)
	}}
	p, err := startSpawnProxy(ctx, egressproxy.DriverCodex, netJob, egressproxy.DestinationAllowlist{netAllowedDest},
		ProxyJournal(log, el), rd.dial)
	if err != nil {
		os.Exit(8)
	}
	u, err := url.Parse(p.URL())
	if err != nil {
		os.Exit(9)
	}
	pw, _ := u.User.Password()
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pw))
	c, err := net.DialTimeout("tcp", u.Host, 2*time.Second)
	if err != nil {
		os.Exit(10)
	}
	_, _ = io.WriteString(c, "CONNECT "+netAllowedDest+" HTTP/1.1\r\nHost: x\r\nProxy-Authorization: "+auth+"\r\n\r\n")
	time.Sleep(30 * time.Second) // the dial hook exits first
	os.Exit(11)
}

// assertNetCrashRecovered reopens the crashed store and checks that recovery
// marks the dial unknown-outcome and that it is never run a second time.
func assertNetCrashRecovered(ctx context.Context, t *testing.T, dbPath, dir string) {
	t.Helper()
	store, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopening the crashed store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log, el := netAudit(t, store)
	key := "agent.egress:" + netJob + ":1"
	if phase, found, err := el.EffectState(ctx, key); err != nil || !found || phase != audit.EffectIntent {
		t.Fatalf("EffectState after the crash = %q, %v, %v; want intent", phase, found, err)
	}
	pending, err := el.PendingEffects(ctx)
	if err != nil || len(pending) != 1 || pending[0].Key != key {
		t.Fatalf("PendingEffects = %+v, %v; want exactly %q", pending, err, key)
	}
	if _, err := el.MarkUnknownOutcome(ctx, pending[0]); err != nil {
		t.Fatalf("MarkUnknownOutcome: %v", err)
	}
	if phase, _, err := el.EffectState(ctx, key); err != nil || phase != audit.EffectUnknown {
		t.Fatalf("EffectState after recovery = %q, %v; want unknown-outcome", phase, err)
	}
	// The same key can never begin again, so the dial is not repeated.
	_, err = el.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect, Actor: "agent-driver:codex",
		Action: "agent.egress.connect", Key: key, ParamsHash: audit.HashParams([]byte(netAllowedDest))})
	if !carries(err, audit.ErrEffectExists) {
		t.Fatalf("BeginEffect after unknown-outcome = %v, want ErrEffectExists", err)
	}
	ran, err := os.ReadFile(filepath.Join(dir, "dials.log"))
	if err != nil || strings.Count(string(ran), "dialed\n") != 1 {
		t.Fatalf("the dial ran %q (%v), want exactly once", ran, err)
	}
	if err := log.Verify(ctx); err != nil {
		t.Fatalf("Verify after recovery: %v", err)
	}
}

func TestSpawnProxyCloseDuringDialFailsTheEffect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	log, el := netAudit(t, storetest.NewMemStore())
	oe := &orderedEffects{EffectWriter: el}
	started := make(chan struct{})
	dial := func(c context.Context, _, _ string) (net.Conn, error) {
		close(started)
		<-c.Done()
		return nil, c.Err()
	}
	p, err := startSpawnProxy(ctx, egressproxy.DriverCodex, netJob, egressproxy.DestinationAllowlist{netAllowedDest},
		ProxyJournal(log, oe), dial)
	if err != nil {
		t.Fatalf("startSpawnProxy: %v", err)
	}
	addr, auth := proxyAddrAuth(t, p)
	go func() {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_, _ = io.WriteString(c, "CONNECT "+netAllowedDest+" HTTP/1.1\r\nHost: x\r\nProxy-Authorization: "+auth+"\r\n\r\n")
			defer func() { _ = c.Close() }()
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the dial never started")
	}
	done := make(chan error, 1)
	go func() { done <- p.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close during a dial: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return while a dial was in flight")
	}
	// Closing cancels the dial, which the proxy journals as a failed dial: the
	// effect is terminal (never re-dialed) and nothing is left pending.
	oe.mu.Lock()
	seq := strings.Join(oe.seq, ",")
	oe.mu.Unlock()
	if seq != "intent,fail:dial-failed" {
		t.Fatalf("effect order = %q, want intent,fail:dial-failed", seq)
	}
	if pending, err := el.PendingEffects(context.Background()); err != nil || len(pending) != 0 {
		t.Fatalf("PendingEffects = %+v, %v; want none after a failed dial", pending, err)
	}
}
