// Package harness supplies process-boundary test assertions.
package harness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/pkg/provider"
)

// RecordCrash records each outbound effect and exits before confirmation.
func RecordCrash(root string) {
	f, err := os.OpenFile(filepath.Join(root, "sent"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(2)
	}
	if _, err = f.WriteString("one"); err != nil {
		os.Exit(2)
	}
	if err = f.Close(); err != nil {
		os.Exit(2)
	}
	os.Exit(3)
}

// Crash re-execs the HTTP crash leg before the parent opens its store.
func Crash(t *testing.T, root string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHostHTTPEffectCrashBetweenEffectAndConfirm$")
	cmd.Env = append(os.Environ(), "CASCADE_HOST_EFFECT_CHILD="+root)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("child=%v output=%s", err, out)
	}
}

// Recover checks an orphan intent is settled exactly once without resending.
func Recover(t *testing.T, effects *audit.EffectLog, dir, root string) {
	t.Helper()
	pending, err := effects.PendingEffects(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%v error=%v", pending, err)
	}
	for _, want := range []int{1, 0} {
		n, err := plugins.RecoverPluginEffects(context.Background(), effects, dir)
		if err != nil || n != want {
			t.Fatalf("recovery=%d error=%v", n, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "sent"))
	if err != nil || string(raw) != "one" {
		t.Fatal("effect count changed")
	}
}

// Proxy asks the executable to issue a host request or notification.
func Proxy(t *testing.T, h *plugins.TrustedHandle, method string, params any, notify bool) (json.RawMessage, *process.RPCError) {
	t.Helper()
	op := "proxy"
	if notify {
		op = "notify"
	}
	raw, _ := json.Marshal(map[string]any{"method": method, "params": params})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := h.Call(ctx, op, raw)
	if err != nil {
		t.Fatal(err)
	}
	if notify {
		return out, nil
	}
	var reply struct {
		Result json.RawMessage
		Error  *process.RPCError
	}
	if err = json.Unmarshal(out, &reply); err != nil {
		t.Fatal(err)
	}
	return reply.Result, reply.Error
}

// Effects checks the exact ordered HTTP effect phases in the real log.
func Effects(t *testing.T, log *audit.Log, want []string) {
	t.Helper()
	page, err := log.Query(context.Background(), audit.Filter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var phases []string
	for _, rec := range page.Records {
		if rec.Action == "plugin.host_http" {
			phases = append(phases, rec.EffectPhase)
		}
	}
	if strings.Join(phases, ",") != strings.Join(want, ",") {
		t.Fatalf("HTTP phases=%v want=%v", phases, want)
	}
}

// WaitNotification waits for the persisted notification audit, not its input ack.
func WaitNotification(t *testing.T, store provider.Store) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		it, err := store.Scan(context.Background(), "audit", "rec:")
		if err != nil {
			t.Fatal(err)
		}
		var stored strings.Builder
		for it.Next(context.Background()) {
			stored.Write(it.Value())
		}
		err = it.Err()
		_ = it.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stored.String(), "host call without reply") {
			return stored.String()
		}
		if time.Now().After(deadline) {
			t.Fatal("missing notification audit")
		}
		time.Sleep(time.Millisecond)
	}
}

// Transport is an in-memory http.RoundTripper for host-boundary tests. It
// answers every request 200 with the body "response" and never opens a
// connection, so a unit test can inject it without importing net/http.
type Transport struct {
	onRequest func(body string)
}

// NewTransport returns a Transport that calls onRequest, when it is not
// nil, with each request's body before it answers.
func NewTransport(onRequest func(body string)) *Transport {
	return &Transport{onRequest: onRequest}
}

// RoundTrip records the request through onRequest and answers 200.
func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.onRequest != nil {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		t.onRequest(string(body))
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("response"))}, nil
}
