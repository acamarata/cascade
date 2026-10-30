// Purpose: the clipboard writer's audit records on the wire: the closed
//
//	vocabulary of `vault get --clipboard` effect and refusal records that
//	the CLI sink, the daemon's audit.clipboard_effect verb and the direct
//	store path all validate the same way, and the one function that applies
//	a validated request to an audit log.
//
// Inputs: a clipboardEffectParams (op, action, key, explain, handle, detail).
// Outputs: clipboardEffectParams.validate, applyClipboardEffect,
//
//	clipboardEffectHandler.
//
// Constraints: FAIL CLOSED. Only clipboard records pass: the kind and actor
//
//	are fixed by the receiver, never read from the wire; the key must be a
//	clipboard.write or clipboard.clear key over cascade IDs; the explain is
//	the five-field zero-payload struct and nothing else; a terminal call may
//	only settle a clipboard intent. The verb is owner-only. No payload byte
//	can be expressed in any field.
//
// SPORT: cmd/cascade audit.clipboard_effect (ADD, P1-SEC-09, P1-BF-R298).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"time"
	"unicode"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// clipboardEffectMethod is the daemon verb that writes one clipboard record.
const clipboardEffectMethod = "audit.clipboard_effect"

// clipboardEffectMaxBytes bounds one encoded request on the wire.
const clipboardEffectMaxBytes = 2048

// clipboardDetailMax bounds a terminal record's detail.
const clipboardDetailMax = 256

const (
	clipOpBegin   = "begin"
	clipOpConfirm = "confirm"
	clipOpFail    = "fail"
	clipOpUnknown = "unknown"
	clipOpRefused = "refused"

	clipActionWrite   = "clipboard.write"
	clipActionClear   = "clipboard.clear"
	clipActionRefused = "clipboard.write.refused"
	clipActor         = "cli"
)

const clipID = `[0-9A-HJKMNP-TV-Z]{26}`

var (
	clipWriteKey    = regexp.MustCompile(`^clipboard\.write:(` + clipID + `)$`)
	clipClearKey    = regexp.MustCompile(`^clipboard\.clear:(` + clipID + `):` + clipID + `$`)
	clipPlatformPat = regexp.MustCompile(`^[a-z]{1,16}$`)
)

// clipboardHandle is an effect handle on the wire; its kind is implied.
type clipboardHandle struct {
	Key       string `json:"key"`
	IntentSeq uint64 `json:"intent_seq"`
	IntentID  string `json:"intent_id,omitempty"`
}

func (h clipboardHandle) effect() audit.EffectHandle {
	return audit.EffectHandle{Key: h.Key, Kind: secrets.KindSecretsClipboardWrite, IntentSeq: h.IntentSeq, IntentID: h.IntentID}
}

// clipboardEffectParams is one request.
type clipboardEffectParams struct {
	Op      string           `json:"op"`
	Action  string           `json:"action,omitempty"`
	Key     string           `json:"key,omitempty"`
	Explain json.RawMessage  `json:"explain,omitempty"`
	Handle  *clipboardHandle `json:"handle,omitempty"`
	Detail  string           `json:"detail,omitempty"`
}

// clipboardEffectAck is the reply: the committed record's sequence and, for
// a begin, the intent's handle.
type clipboardEffectAck struct {
	Seq    uint64           `json:"seq"`
	Handle *clipboardHandle `json:"handle,omitempty"`
}

// clipboardExplain mirrors the writer's zero-payload explain, field for field.
type clipboardExplain struct {
	ActionClass string `json:"action_class"`
	Ref         string `json:"ref,omitempty"`
	WrittenAt   string `json:"written_at,omitempty"`
	ClearDueAt  string `json:"clear_due_at,omitempty"`
	Platform    string `json:"platform"`
}

func clipInvalid(msg string) error {
	return cascade.New(cascade.KindInvalidInput, "audit.clipboard_effect: "+msg)
}

// checkClipboardExplain refuses anything but the writer's explain for action
// whose ref is ref ("" for a refusal).
func checkClipboardExplain(raw json.RawMessage, action, ref string) error {
	var e clipboardExplain
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil || dec.More() {
		return clipInvalid("the explain is not the clipboard explain")
	}
	if e.ActionClass != action || e.Ref != ref || !clipPlatformPat.MatchString(e.Platform) || (e.WrittenAt == "") != (e.ClearDueAt == "") {
		return clipInvalid("the explain does not match the record")
	}
	for _, ts := range []string{e.WrittenAt, e.ClearDueAt} {
		if _, err := time.Parse(time.RFC3339Nano, ts); ts != "" && err != nil {
			return clipInvalid("the explain carries a malformed time")
		}
	}
	return nil
}

// clipboardKeyRef returns the write ref a clipboard key names, or false.
func clipboardKeyRef(key string) (string, string, bool) {
	if m := clipWriteKey.FindStringSubmatch(key); m != nil {
		return clipActionWrite, m[1], true
	}
	if m := clipClearKey.FindStringSubmatch(key); m != nil {
		return clipActionClear, m[1], true
	}
	return "", "", false
}

// validate refuses any request outside the vocabulary.
func (p clipboardEffectParams) validate() error {
	switch p.Op {
	case clipOpBegin:
		action, ref, ok := clipboardKeyRef(p.Key)
		if !ok || action != p.Action || p.Handle != nil || p.Detail != "" {
			return clipInvalid("a begin needs a clipboard key matching its action and nothing else")
		}
		return checkClipboardExplain(p.Explain, p.Action, ref)
	case clipOpConfirm, clipOpFail, clipOpUnknown:
		if p.Handle == nil || p.Handle.IntentSeq == 0 || p.Action != "" || p.Key != "" || len(p.Explain) != 0 {
			return clipInvalid("a terminal call needs a handle and nothing else")
		}
		if _, _, ok := clipboardKeyRef(p.Handle.Key); !ok || len(p.Handle.IntentID) > 64 {
			return clipInvalid("the handle does not name a clipboard intent")
		}
		if (p.Op == clipOpUnknown && p.Detail != "") || len(p.Detail) > clipboardDetailMax || containsControl(p.Detail) {
			return clipInvalid("the detail is not a short single-line string")
		}
		return nil
	case clipOpRefused:
		if p.Action != clipActionRefused || p.Key != "" || p.Handle != nil || p.Detail != "" {
			return clipInvalid("a refusal names its action and explain only")
		}
		return checkClipboardExplain(p.Explain, clipActionRefused, "")
	}
	return clipInvalid("unknown op")
}

func containsControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// applyClipboardEffect validates p and writes it through log and fx.
func applyClipboardEffect(ctx context.Context, log audit.Writer, fx *audit.EffectLog, p clipboardEffectParams) (clipboardEffectAck, error) {
	if err := p.validate(); err != nil {
		return clipboardEffectAck{}, err
	}
	var rec audit.Record
	var err error
	switch p.Op {
	case clipOpBegin:
		h, berr := fx.BeginEffect(ctx, audit.EffectRequest{Kind: secrets.KindSecretsClipboardWrite, Actor: clipActor,
			Action: p.Action, Key: p.Key, Explain: p.Explain})
		if berr != nil {
			return clipboardEffectAck{}, berr
		}
		return clipboardEffectAck{Seq: h.IntentSeq, Handle: &clipboardHandle{Key: h.Key, IntentSeq: h.IntentSeq, IntentID: h.IntentID}}, nil
	case clipOpConfirm:
		rec, err = fx.ConfirmEffect(ctx, p.Handle.effect(), p.Detail)
	case clipOpFail:
		rec, err = fx.FailEffect(ctx, p.Handle.effect(), p.Detail)
	case clipOpUnknown:
		rec, err = fx.MarkUnknownOutcome(ctx, p.Handle.effect())
	default: // clipOpRefused
		rec, err = log.Append(ctx, audit.Event{Kind: secrets.KindSecretsClipboardWrite, Actor: clipActor, Action: clipActionRefused, Explain: p.Explain})
	}
	if err != nil {
		return clipboardEffectAck{}, err
	}
	return clipboardEffectAck{Seq: rec.Seq}, nil
}

// clipboardEffectHandler is the daemon side of audit.clipboard_effect.
func clipboardEffectHandler(log audit.Writer, fx *audit.EffectLog, clock runtime.Clock) func(context.Context, json.RawMessage) (any, error) {
	bucket := &gateAppendBucket{}
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if len(raw) > clipboardEffectMaxBytes {
			return nil, clipInvalid("the request is too large")
		}
		var p clipboardEffectParams
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil || dec.More() {
			return nil, clipInvalid("params are not one clipboard record")
		}
		if err := p.validate(); err != nil {
			return nil, err
		}
		if !bucket.admit(clock.Now()) {
			return nil, cascade.New(cascade.KindQuotaExhausted, "audit.clipboard_effect: too many records; retry shortly")
		}
		return applyClipboardEffect(ctx, log, fx, p)
	}
}
