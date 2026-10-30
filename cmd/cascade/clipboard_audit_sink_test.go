//go:build !windows

// Purpose: the clipboard audit sink against the real single-opener sqlite
//
//	store: daemonless, the gate's rows and the clipboard records each open
//	and close the same store in one `vault get --clipboard`; with a composed
//	daemon holding the store, the records go through the owner-only
//	audit.clipboard_effect verb into the daemon's log; the verb's vocabulary
//	refuses everything else. No test touches the real clipboard or HOME.
//
// SPORT: cmd/cascade clipboard audit sink (TEST, P1-SEC-09, P1-BF-R298).
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

var clipHash = strings.Repeat("ab", 32)

func clipRef(t *testing.T) string {
	t.Helper()
	id, err := cascade.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

func clipWriteRequest(ref string) audit.EffectRequest {
	explain := `{"action_class":"clipboard.write","ref":"` + ref + `","written_at":"2026-10-29T12:00:00Z","clear_due_at":"2026-10-29T12:00:30Z","platform":"darwin"}`
	return audit.EffectRequest{Kind: secrets.KindSecretsClipboardWrite, Actor: "cli", Action: "clipboard.write",
		Key: "clipboard.write:" + ref, Explain: json.RawMessage(explain)}
}

// storeRowGate audits through storeGateSink, which opens the real store per
// row, exactly as the daemonless production gate does.
type storeRowGate struct{ sink storeGateSink }

func (g storeRowGate) Authorize(ctx context.Context, verb string) error {
	_, err := g.sink.AppendGateRow(ctx, gateRow{Kind: "elevation.attempt", Verb: verb, ParamsHash: clipHash})
	return err
}
func (g storeRowGate) RecordVaultAccess(ctx context.Context, name, _ string) error {
	_, err := g.sink.AppendGateRow(ctx, gateRow{Kind: "vault.access", Verb: "vault.get", ParamsHash: clipHash, Name: name})
	return err
}

// sinkDrivingWriter writes one clipboard intent and its confirm through the
// sinks it was built with, as the real writer does around setValue.
type sinkDrivingWriter struct {
	fx  audit.EffectWriter
	key string
	t   *testing.T
}

func (w *sinkDrivingWriter) Write(ctx context.Context, _, _ []byte) error {
	ref := clipRef(w.t)
	h, err := w.fx.BeginEffect(ctx, clipWriteRequest(ref))
	if err != nil {
		return err
	}
	w.key = h.Key
	_, err = w.fx.ConfirmEffect(ctx, h, "on the clipboard")
	return err
}

// TestVaultGetClipboardDaemonlessSharesTheStore: with no daemon (the real
// dial to the profile socket fails), the gate's rows and the clipboard records open and close the one real sqlite store in
// turn; nothing holds it, so the call completes and both land in the log.
func TestVaultGetClipboardDaemonlessSharesTheStore(t *testing.T) {
	resetProcessFirewall(t)
	selectProcessCustody = testProcessCustody(t.TempDir())
	paths := firewallTestPaths(t)
	clock := runtime.NewSystemClock()
	w := &sinkDrivingWriter{t: t}
	deps, _ := clipDeps(t, storeRowGate{storeGateSink{paths: paths, clock: clock}}, &fakeClipWriter{})
	deps.NewClipboard = func(_ time.Duration, _ secrets.ApprovalVerifier, _ audit.Writer, fx audit.EffectWriter) (secrets.ClipboardWriter, error) {
		sink, ok := fx.(*clipboardAuditSink)
		if !ok {
			t.Fatalf("writer effects = %T, want the clipboard audit sink", fx)
		}
		w.fx = sink
		return w, nil
	}
	host := hostClipboard(paths)
	host.ttl = func(context.Context) (time.Duration, error) { return 30 * time.Second, nil }
	if _, _, err := runClip(context.Background(), t, deps, host); err != nil {
		t.Fatalf("vault get --clipboard daemonless: %v", err)
	}
	store, closeStore, err := openPluginStore(context.Background(), paths, clock)
	if err != nil {
		t.Fatalf("the store is still held after the call: %v", err)
	}
	defer closeStore()
	fx, err := openEffectLog(store, clock, nil, paths)
	if err != nil {
		t.Fatal(err)
	}
	if phase, ok, err := fx.EffectState(context.Background(), w.key); err != nil || !ok || phase != audit.EffectConfirmed {
		t.Fatalf("clipboard intent %q = %q %v %v; want confirmed in the profile log", w.key, phase, ok, err)
	}
}

// TestClipboardSinkThroughDaemon: with a daemon holding the store, every
// record goes through audit.clipboard_effect into the daemon's log and the
// direct path is never used; a daemon error other than a dial failure
// refuses rather than falling back.
func TestClipboardSinkThroughDaemon(t *testing.T) {
	d := composePluginDaemon(t)
	clock := runtime.NewSystemClock()
	direct := func(context.Context, clipboardEffectParams) (clipboardEffectAck, error) {
		t.Fatal("the direct store path was used while a daemon answered")
		return clipboardEffectAck{}, nil
	}
	sink := &clipboardAuditSink{paths: d.Deps.Paths, clock: clock, openDirect: direct,
		dial: func() (gateCall, error) { return d.Call, nil }}
	ctx := context.Background()
	ref := clipRef(t)
	h, err := sink.BeginEffect(ctx, clipWriteRequest(ref))
	if err != nil {
		t.Fatalf("BeginEffect via daemon: %v", err)
	}
	if _, err := sink.BeginEffect(ctx, clipWriteRequest(ref)); err == nil || !strings.Contains(err.Error(), "already begun") {
		t.Fatalf("second BeginEffect = %v, want the daemon log's ErrEffectExists", err)
	}
	if _, err := sink.MarkUnknownOutcome(ctx, h); err != nil {
		t.Fatalf("MarkUnknownOutcome via daemon: %v", err)
	}
	refusal := json.RawMessage(`{"action_class":"clipboard.write.refused","platform":"darwin"}`)
	if rec, err := sink.Append(ctx, audit.Event{Kind: secrets.KindSecretsClipboardWrite, Actor: "cli", Action: "clipboard.write.refused", Explain: refusal}); err != nil || rec.Seq == 0 {
		t.Fatalf("refusal via daemon = %+v, %v", rec, err)
	}
	fx, err := openEffectLog(d.Store, clock, nil, d.Deps.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if phase, ok, err := fx.EffectState(ctx, h.Key); err != nil || !ok || phase != audit.EffectUnknown {
		t.Fatalf("daemon log state = %q %v %v; want unknown-outcome", phase, ok, err)
	}
	broken := &clipboardAuditSink{openDirect: direct, dial: func() (gateCall, error) {
		return func(context.Context, string, any, any) error {
			return cascade.New(cascade.KindUnavailable, "fake: daemon refused")
		}, nil
	}}
	if _, err := broken.BeginEffect(ctx, clipWriteRequest(clipRef(t))); err == nil || !strings.Contains(err.Error(), "daemon refused") {
		t.Fatalf("daemon refusal = %v, want it surfaced with no fallback", err)
	}
}

// TestClipboardEffectIsOwnerOnly: a principal-bearing caller and a
// caller-less call are refused; the owner is acknowledged.
func TestClipboardEffectIsOwnerOnly(t *testing.T) {
	d := composePluginDaemon(t)
	req := clipWriteRequest(clipRef(t))
	params, _ := json.Marshal(clipboardEffectParams{Op: clipOpBegin, Action: req.Action, Key: req.Key, Explain: req.Explain})
	dispatch := func(ctx context.Context) (any, *rpc.ErrorObject) {
		return d.Registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: clipboardEffectMethod, Params: params})
	}
	if _, eo := dispatch(principal.WithCaller(t.Context(), principal.Caller{Principal: &principal.Principal{}})); eo == nil {
		t.Fatal("a principal-bearing caller wrote a clipboard record")
	}
	if _, eo := dispatch(t.Context()); eo == nil {
		t.Fatal("a caller-less call wrote a clipboard record")
	}
	if res, eo := dispatch(principal.WithCaller(t.Context(), principal.Caller{OwnerCLI: true})); eo != nil || res.(clipboardEffectAck).Seq == 0 {
		t.Fatalf("owner begin = %v %v", res, eo)
	}
}
