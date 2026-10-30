// Purpose: the audit.clipboard_effect vocabulary: every request outside the
//
//	clipboard record shapes is refused before anything is written, on the
//	sink side and in the daemon handler alike.
//
// SPORT: cmd/cascade audit.clipboard_effect (TEST, P1-SEC-09, P1-BF-R298).
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	vocabRef  = "0123456789ABCDEFGHJKMNPQRS"
	vocabRef2 = "0123456789ABCDEFGHJKMNPQRT"
)

func vocabExplain(action, ref string) json.RawMessage {
	if ref == "" {
		return json.RawMessage(`{"action_class":"` + action + `","platform":"darwin"}`)
	}
	return json.RawMessage(`{"action_class":"` + action + `","ref":"` + ref + `","platform":"darwin"}`)
}

func TestClipboardEffectVocabulary(t *testing.T) {
	h := &clipboardHandle{Key: "clipboard.write:" + vocabRef, IntentSeq: 3}
	good := map[string]clipboardEffectParams{
		"begin write": {Op: clipOpBegin, Action: clipActionWrite, Key: "clipboard.write:" + vocabRef, Explain: vocabExplain(clipActionWrite, vocabRef)},
		"begin clear": {Op: clipOpBegin, Action: clipActionClear, Key: "clipboard.clear:" + vocabRef + ":" + vocabRef2, Explain: vocabExplain(clipActionClear, vocabRef)},
		"confirm":     {Op: clipOpConfirm, Handle: h, Detail: "on the clipboard"},
		"fail":        {Op: clipOpFail, Handle: h, Detail: "not run: audit store down"},
		"unknown":     {Op: clipOpUnknown, Handle: h},
		"refused":     {Op: clipOpRefused, Action: clipActionRefused, Explain: vocabExplain(clipActionRefused, "")},
	}
	for name, p := range good {
		if err := p.validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	bad := map[string]clipboardEffectParams{
		"unknown op":          {Op: "append", Action: clipActionWrite},
		"foreign key":         {Op: clipOpBegin, Action: "approval.grant", Key: "approval.grant:" + vocabRef, Explain: vocabExplain("approval.grant", vocabRef)},
		"action/key mismatch": {Op: clipOpBegin, Action: clipActionClear, Key: "clipboard.write:" + vocabRef, Explain: vocabExplain(clipActionClear, vocabRef)},
		"lower-case ref":      {Op: clipOpBegin, Action: clipActionWrite, Key: "clipboard.write:" + strings.ToLower(vocabRef), Explain: vocabExplain(clipActionWrite, vocabRef)},
		"explain ref differs": {Op: clipOpBegin, Action: clipActionWrite, Key: "clipboard.write:" + vocabRef, Explain: vocabExplain(clipActionWrite, vocabRef2)},
		"explain extra field": {Op: clipOpBegin, Action: clipActionWrite, Key: "clipboard.write:" + vocabRef,
			Explain: json.RawMessage(`{"action_class":"clipboard.write","ref":"` + vocabRef + `","platform":"darwin","value":"x"}`)},
		"explain bad time": {Op: clipOpBegin, Action: clipActionWrite, Key: "clipboard.write:" + vocabRef,
			Explain: json.RawMessage(`{"action_class":"clipboard.write","ref":"` + vocabRef + `","written_at":"now","clear_due_at":"later","platform":"darwin"}`)},
		"begin with handle":       {Op: clipOpBegin, Action: clipActionWrite, Key: "clipboard.write:" + vocabRef, Explain: vocabExplain(clipActionWrite, vocabRef), Handle: h},
		"terminal no handle":      {Op: clipOpConfirm},
		"terminal foreign handle": {Op: clipOpConfirm, Handle: &clipboardHandle{Key: "approval.grant:x", IntentSeq: 1}},
		"terminal with key":       {Op: clipOpFail, Handle: h, Key: "clipboard.write:" + vocabRef},
		"unknown with detail":     {Op: clipOpUnknown, Handle: h, Detail: "x"},
		"detail newline":          {Op: clipOpConfirm, Handle: h, Detail: "a\nb"},
		"detail too long":         {Op: clipOpFail, Handle: h, Detail: strings.Repeat("x", 257)},
		"refusal with ref":        {Op: clipOpRefused, Action: clipActionRefused, Explain: vocabExplain(clipActionRefused, vocabRef)},
		"refusal other action":    {Op: clipOpRefused, Action: "vault.get", Explain: vocabExplain("vault.get", "")},
	}
	assertVocabRefused(t, bad)
}

// assertVocabRefused checks each request is refused by validate and by the
// daemon handler, plus the raw shapes only the handler sees.
func assertVocabRefused(t *testing.T, bad map[string]clipboardEffectParams) {
	t.Helper()
	handler := clipboardEffectHandler(nil, nil, runtime.NewSystemClock())
	for name, p := range bad {
		if err := p.validate(); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: validate = %v, want KindInvalidInput", name, err)
		}
		raw, _ := json.Marshal(p)
		if _, err := handler(context.Background(), raw); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: handler = %v, want KindInvalidInput", name, err)
		}
	}
	for name, raw := range map[string]string{
		"unknown field": `{"op":"unknown","handle":{"key":"clipboard.write:` + vocabRef + `","intent_seq":1},"kind":"approval.grant"}`,
		"two values":    `{"op":"unknown"} {}`,
		"oversize":      `{"op":"refused","detail":"` + strings.Repeat("x", clipboardEffectMaxBytes) + `"}`,
	} {
		if _, err := handler(context.Background(), json.RawMessage(raw)); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: handler = %v, want KindInvalidInput", name, err)
		}
	}
}

// TestClipboardSinkRefusesForeignRecords: the sink refuses a non-clipboard
// event, request or handle before any daemon or store is reached.
func TestClipboardSinkRefusesForeignRecords(t *testing.T) {
	sink := &clipboardAuditSink{dial: func() (gateCall, error) { t.Fatal("dialled"); return nil, nil }}
	ctx := context.Background()
	if _, err := sink.Append(ctx, audit.Event{Kind: audit.KindVaultAccess, Actor: "cli", Action: clipActionRefused}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("foreign event = %v", err)
	}
	if _, err := sink.Append(ctx, audit.Event{Kind: audit.KindSecretsClipboardWrite, Actor: "cli", Action: clipActionRefused, ParamsHash: "x",
		Explain: vocabExplain(clipActionRefused, "")}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("refusal with extra fields = %v", err)
	}
	if _, err := sink.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindVaultAccess, Actor: "cli"}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("foreign request = %v", err)
	}
	if _, err := sink.ConfirmEffect(ctx, audit.EffectHandle{Key: "clipboard.write:" + vocabRef, Kind: audit.KindVaultAccess, IntentSeq: 1}, ""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("foreign handle = %v", err)
	}
	if _, err := sink.FailEffect(ctx, audit.EffectHandle{Key: "approval.grant:x", Kind: audit.KindSecretsClipboardWrite, IntentSeq: 1}, ""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("foreign key = %v", err)
	}
}

// TestClipboardSinkFailDetailFitsVocabulary: the writer's abandon reason is
// "not run: " plus the whole error chain, which for a pending-store I/O
// failure carries the file path and runs past the 256-byte detail bound.
// The sink must still land the fail record, not drop it for the daemon to
// mislabel as an unknown outcome later.
func TestClipboardSinkFailDetailFitsVocabulary(t *testing.T) {
	var got clipboardEffectParams
	sink := &clipboardAuditSink{openDirect: func(_ context.Context, p clipboardEffectParams) (clipboardEffectAck, error) {
		got = p
		return clipboardEffectAck{Seq: 9}, nil
	}, dial: func() (gateCall, error) { return nil, cascade.New(cascade.KindUnavailable, "no daemon") }}
	sink.mode = gateModeDirect
	h := audit.EffectHandle{Key: "clipboard.write:" + vocabRef, Kind: audit.KindSecretsClipboardWrite, IntentSeq: 3, IntentID: "x"}
	reason := "not run: unavailable: secrets: the pending clear could not be persisted; nothing was written: " +
		"unavailable: secrets: could not write the pending clipboard clears: open " + strings.Repeat("/very/long/data/dir", 12) +
		"/secrets/pending-clears.json:\nno space left on device\t(é)"
	if _, err := sink.FailEffect(context.Background(), h, reason); err != nil {
		t.Fatalf("FailEffect with a long multi-line reason = %v; want the fail record written", err)
	}
	if len(got.Detail) > 256 || containsControl(got.Detail) || !strings.HasPrefix(got.Detail, "not run: unavailable: secrets: the pending clear") {
		t.Fatalf("detail sent = %q; want a single-line prefix of the reason within 256 bytes", got.Detail)
	}
	if err := got.validate(); err != nil {
		t.Fatalf("the sent fail record does not validate: %v", err)
	}
}
