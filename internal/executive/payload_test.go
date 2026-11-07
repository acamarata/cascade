package executive

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const payloadGoldenPath = "testdata/payload-schema.golden.json"

type payloadSchemaEntry struct {
	Version int      `json:"version"`
	Type    string   `json:"type"`
	Fields  []string `json:"fields"`
}

// schemaFromTable renders the unexported kind-to-struct table as the golden shape.
func schemaFromTable() map[string]payloadSchemaEntry {
	out := make(map[string]payloadSchemaEntry, len(payloadTable))
	for k, typ := range payloadTable {
		var fields []string
		for i := 0; i < typ.NumField(); i++ {
			fields = append(fields, jsonName(typ.Field(i)))
		}
		out[string(k)] = payloadSchemaEntry{Version: int(PayloadV1), Type: typ.Name(), Fields: fields}
	}
	return out
}

// validFixture returns a valid v1 body for kind with every field set.
func validFixture(t *testing.T, kind Kind) map[string]any {
	t.Helper()
	typ, ok := payloadTable[kind]
	if !ok {
		t.Fatalf("no payload type for %s", kind)
	}
	body := map[string]any{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		switch f.Type.Kind() { //nolint:exhaustive // the test walks only the field kinds the payload schema declares
		case reflect.String:
			body[jsonName(f)] = "v-1"
		case reflect.Int64:
			body[jsonName(f)] = 1
		case reflect.Pointer:
			body[jsonName(f)] = false
		default:
			t.Fatalf("%s.%s has unsupported type %s", typ.Name(), f.Name, f.Type)
		}
	}
	return body
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func wantErr(t *testing.T, label string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: got %v, want %v", label, err, target)
	}
}

// TestEXE01LegacyAC16 verifies every exec.* kind has a closed, versioned payload.
func TestEXE01LegacyAC16(t *testing.T) {
	isolateHome(t)
	if os.Getenv("CASCADE_UPDATE_PAYLOAD_GOLDEN") == "1" {
		b, _ := json.MarshalIndent(schemaFromTable(), "", "  ")
		if err := os.WriteFile(payloadGoldenPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.FromSlash(payloadGoldenPath))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]payloadSchemaEntry
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if len(golden) != len(AllKinds) || len(AllKinds) != 41 {
		t.Fatalf("golden has %d keys, AllKinds %d, want 41", len(golden), len(AllKinds))
	}
	if !reflect.DeepEqual(golden, schemaFromTable()) {
		t.Fatalf("golden differs from the payload table")
	}
	for _, k := range AllKinds {
		if _, ok := golden[string(k)]; !ok {
			t.Fatalf("golden lacks %s", k)
		}
		if err := ValidatePayload(k, PayloadV1, mustJSON(t, validFixture(t, k))); err != nil {
			t.Fatalf("valid fixture for %s: %v", k, err)
		}
	}
	checkAC16Required(t)
	checkAC16Version(t)
	checkAC16Size(t)
}

func checkAC16Required(t *testing.T) {
	t.Helper()
	cases := []struct {
		label string
		kind  Kind
		body  string
	}{
		{"failed attempts 0", KindTaskFailed, `{"task_id":"t","consecutive_failed_attempts":0}`},
		{"failed attempts missing", KindTaskFailed, `{"task_id":"t"}`},
		{"quota low no role", KindQuotaLow, `{"lane_id":"l"}`},
		{"privacy no alternative", KindPrivacyPolicyBlocked, `{"subject_id":"s"}`},
		{"empty string", KindTaskStarted, `{"task_id":""}`},
		{"wrong type", KindTaskStarted, `{"task_id":7}`},
	}
	for _, c := range cases {
		wantErr(t, c.label, ValidatePayload(c.kind, PayloadV1, []byte(c.body)), ErrInvalidExecPayload)
	}
	ok := `{"subject_id":"s","safe_alternative_exists":false}`
	if err := ValidatePayload(KindPrivacyPolicyBlocked, PayloadV1, []byte(ok)); err != nil {
		t.Fatalf("safe_alternative_exists false must be valid: %v", err)
	}
}

func checkAC16Version(t *testing.T) {
	t.Helper()
	body := []byte(`{"task_id":"t"}`)
	wantErr(t, "version 2", ValidatePayload(KindTaskStarted, 2, body), ErrExecPayloadVersion)
	wantErr(t, "version 0", ValidatePayload(KindTaskStarted, 0, body), ErrExecPayloadVersion)
	wantErr(t, "exec.NOPE", ValidatePayload("exec.NOPE", PayloadV1, body), ErrExecPayloadVersion)
}

func checkAC16Size(t *testing.T) {
	t.Helper()
	pad := MaxPayloadBytes - len(`{"task_id":""}`)
	fits := []byte(`{"task_id":"` + strings.Repeat("a", pad) + `"}`)
	if len(fits) != MaxPayloadBytes {
		t.Fatalf("fixture is %d bytes", len(fits))
	}
	if err := ValidatePayload(KindTaskStarted, PayloadV1, fits); err != nil {
		t.Fatalf("16384-byte payload: %v", err)
	}
	over := []byte(`{"task_id":"` + strings.Repeat("a", pad+1) + `"}`)
	err := ValidatePayload(KindTaskStarted, PayloadV1, over)
	wantErr(t, "16385 bytes", err, ErrInvalidExecPayload)
	if strings.Contains(err.Error(), "aaaa") {
		t.Fatalf("error text carries payload bytes: %v", err)
	}
}

// TestValidatePayloadStrictDecode verifies the decode is exactly one closed object.
func TestValidatePayloadStrictDecode(t *testing.T) {
	isolateHome(t)
	cases := map[string]string{
		"trailing object": `{"task_id":"t"} {}`,
		"trailing junk":   `{"task_id":"t"} x`,
		"duplicate key":   `{"task_id":"t","task_id":"u"}`,
		"null":            `null`,
		"empty":           ``,
		"whitespace":      "  \n",
		"array":           `[{"task_id":"t"}]`,
		"unknown field":   `{"task_id":"t","extra":"e"}`,
		"version field":   `{"task_id":"t","payload_version":1}`,
	}
	for label, body := range cases {
		err := ValidatePayload(KindTaskStarted, PayloadV1, []byte(body))
		wantErr(t, label, err, ErrInvalidExecPayload)
		if strings.Contains(err.Error(), "extra") || strings.Contains(err.Error(), `"u"`) {
			t.Fatalf("%s: error text quotes the input: %v", label, err)
		}
	}
	if err := ValidatePayload(KindTaskStarted, PayloadV1, []byte(" {\"task_id\":\"t\"}\n")); err != nil {
		t.Fatalf("surrounding whitespace must be valid: %v", err)
	}
}

func TestJSONNameFallsBackToGoName(t *testing.T) {
	f := reflect.StructField{Name: "Plain"}
	if got := jsonName(f); got != "Plain" {
		t.Fatalf("jsonName = %q, want Plain", got)
	}
}
