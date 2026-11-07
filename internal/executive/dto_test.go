package executive

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

var errInvalidInputKind = cascade.New(cascade.KindInvalidInput, "")

// forbiddenFieldNames walks typ through structs, pointers, slices, arrays
// and maps and returns every field name or JSON name that normalizes into
// the forbidden credential set.
func forbiddenFieldNames(typ reflect.Type) []string {
	var hits []string
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		if seen[t] {
			return
		}
		seen[t] = true
		switch t.Kind() { //nolint:exhaustive // the test walks only the field kinds the public DTOs declare
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(t.Elem())
		case reflect.Map:
			walk(t.Key())
			walk(t.Elem())
		case reflect.Struct:
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				if isForbiddenKey(f.Name) || isForbiddenKey(jsonName(f)) {
					hits = append(hits, t.Name()+"."+f.Name)
				}
				walk(f.Type)
			}
		}
	}
	walk(typ)
	return hits
}

// TestEXE01LegacyAC17 verifies every public surface is free of credential-named fields.
func TestEXE01LegacyAC17(t *testing.T) {
	isolateHome(t)
	types := PublicDTOTypes()
	want := []reflect.Type{reflect.TypeOf(EventDTO{}), reflect.TypeOf(ExplainDTO{}), reflect.TypeOf(LogDTO{})}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("PublicDTOTypes = %v, want %v", types, want)
	}
	types[0] = reflect.TypeOf(0)
	if PublicDTOTypes()[0] != want[0] {
		t.Fatalf("PublicDTOTypes returned a shared slice")
	}
	for _, typ := range want {
		if hits := forbiddenFieldNames(typ); len(hits) > 0 {
			t.Fatalf("%s carries forbidden fields %v", typ.Name(), hits)
		}
	}
	type leaky struct {
		Rows []struct{ SecretRef string }
		ByID map[string]*struct {
			Inner string `json:"api_key"`
		}
	}
	hits := forbiddenFieldNames(reflect.TypeOf(leaky{}))
	if len(hits) != 2 {
		t.Fatalf("walker missed nested forbidden fields: %v", hits)
	}
	det := newTestDetector(t)
	b, err := MarshalPublic(det, Envelope{EventID: "e", Kind: KindTaskStarted})
	if b != nil || !errors.Is(err, errInvalidInputKind) {
		t.Fatalf("MarshalPublic(Envelope) = %q, %v; want nil, KindInvalidInput", b, err)
	}
	for _, v := range []any{nil, &LogDTO{}, "text"} {
		if _, err := MarshalPublic(det, v); !errors.Is(err, errInvalidInputKind) {
			t.Fatalf("MarshalPublic(%T) = %v, want KindInvalidInput", v, err)
		}
	}
}

// TestMarshalPublicRejectsCredentialValue verifies serialized surfaces refuse credential values.
func TestMarshalPublicRejectsCredentialValue(t *testing.T) {
	isolateHome(t)
	det := newTestDetector(t)
	canary := skCanary()
	b, err := MarshalPublic(det, LogDTO{EventID: "e1", Kind: KindTaskFailed, Message: "leaked " + canary})
	wantErr(t, "log canary", err, ErrCredentialInPayload)
	if b != nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("refusal must return nil bytes and no canary: %q, %v", b, err)
	}
	ev := EventDTO{
		EventID: "01JC8Z3K4M5N6P7Q8R9S0T1V2W", Kind: KindTaskStarted, StreamID: "run-1", StreamSeq: 1,
		RunID: "run-1", Producer: "test", PayloadVersion: PayloadV1,
		Payload: json.RawMessage(`{"task_id":"t1"}`), OccurredAt: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC),
	}
	if len(ev.EventID) != 26 {
		t.Fatalf("fixture ULID is %d chars", len(ev.EventID))
	}
	out, err := MarshalPublic(det, ev)
	if err != nil || !strings.Contains(string(out), `"event_id":"01JC8Z3K4M5N6P7Q8R9S0T1V2W"`) {
		t.Fatalf("valid EventDTO: %s, %v", out, err)
	}
	got := SanitizeError(det, fmt.Errorf("dial failed with %s: refused", canary))
	if strings.Contains(got, canary) || !strings.Contains(got, redacted) || !strings.HasSuffix(got, ": refused") {
		t.Fatalf("SanitizeError = %q", got)
	}
	ev.Payload = json.RawMessage(`{"task_id":""}`)
	if _, err := MarshalPublic(det, ev); !errors.Is(err, ErrInvalidExecPayload) {
		t.Fatalf("invalid EventDTO payload: %v", err)
	}
	ev.Payload = json.RawMessage(`{"task_id":"t1"}`)
	ev.OccurredAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if out, err := MarshalPublic(det, ev); out != nil || !errors.Is(err, errInvalidInputKind) {
		t.Fatalf("unmarshalable EventDTO: %q, %v", out, err)
	}
	if out, err := MarshalPublic(nil, ExplainDTO{EventID: "e", Kind: KindTaskReady, Reason: "r"}); out != nil ||
		!errors.Is(err, errInvalidInputKind) {
		t.Fatalf("nil detector: %q, %v", out, err)
	}
}

func TestNewEventDTO(t *testing.T) {
	isolateHome(t)
	if _, err := NewEventDTO(nil); !errors.Is(err, errInvalidInputKind) {
		t.Fatalf("nil envelope: %v", err)
	}
	env := &Envelope{EventID: "e", Kind: KindTaskFailed, StreamID: "s", StreamSeq: 2, RunID: "r",
		ProjectID: "p", Producer: "pr", PayloadVersion: PayloadV1,
		Payload: Payload(`{"task_id":"t","consecutive_failed_attempts":3}`), OccurredAt: time.Unix(5, 0).UTC()}
	dto, err := NewEventDTO(env)
	if err != nil {
		t.Fatalf("NewEventDTO: %v", err)
	}
	if dto.EventID != "e" || dto.StreamSeq != 2 || dto.ProjectID != "p" || string(dto.Payload) != string(env.Payload) ||
		!dto.OccurredAt.Equal(env.OccurredAt) {
		t.Fatalf("dto does not mirror the envelope: %+v", dto)
	}
	env.Payload[2] = 'X'
	if dto.Payload[2] == 'X' {
		t.Fatalf("dto payload aliases the envelope")
	}
	env.PayloadVersion = 2
	if _, err := NewEventDTO(env); !errors.Is(err, ErrExecPayloadVersion) {
		t.Fatalf("version 2: %v", err)
	}
}
