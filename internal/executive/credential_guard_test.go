package executive

import (
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// skCanary builds the credential canary by concatenation so no key
// literal lands in a tracked file.
func skCanary() string {
	return "sk" + "-" + strings.Repeat("Zq7", 9) + "x"
}

func newTestDetector(t *testing.T) *secrets.Detector {
	t.Helper()
	det, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	if len(det.ScanCertain([]byte(skCanary()))) == 0 {
		t.Fatalf("ScanCertain does not flag the canary; stop and report")
	}
	return det
}

// TestEXE01LegacyAC11 verifies no envelope payload may carry a credential key at any depth.
func TestEXE01LegacyAC11(t *testing.T) {
	isolateHome(t)
	for _, g := range kindGroups {
		kind := g.kinds[0]
		deep := validFixture(t, kind)
		deep["meta"] = map[string]any{"inner": map[string]any{"Secret_Ref": "r"}}
		wantErr(t, string(kind)+" deep", ValidatePayload(kind, PayloadV1, mustJSON(t, deep)), ErrCredentialInPayload)

		arr := validFixture(t, kind)
		arr["items"] = []any{1, map[string]any{"API-Key": "k"}}
		wantErr(t, string(kind)+" array", ValidatePayload(kind, PayloadV1, mustJSON(t, arr)), ErrCredentialInPayload)
	}
}

// TestCredentialKeyGuardNestedExact verifies exact, nested key matching.
func TestCredentialKeyGuardNestedExact(t *testing.T) {
	isolateHome(t)
	wantErr(t, "nested access-token",
		CheckPayloadKeys([]byte(`{"a":{"b":[{"access-token":"x"}]}}`)), ErrCredentialInPayload)
	benign := `{"token_count":1,"keyboard":"k","secretary_id":"s","monkey":"m","tokens":[1]}`
	if err := CheckPayloadKeys([]byte(benign)); err != nil {
		t.Fatalf("substring defect: benign keys flagged: %v", err)
	}
	wantErr(t, "open brace", CheckPayloadKeys([]byte("{")), ErrInvalidExecPayload)
	for _, k := range []string{"Credential", "CREDENTIALS", "credential.ref", "secret", "SECRET-REF",
		"api_key", "Token", "access_token", "refresh.token", "Password", "Authorization",
		"private_key", "Bearer"} {
		wantErr(t, k, CheckPayloadKeys([]byte(`[{"`+k+`":1}]`)), ErrCredentialInPayload)
	}
	malformed := []string{"", "}", `{"a" 1}`, `[1 2]`, `{"a":}`, `{1:2}`, `["a",`}
	for _, m := range malformed {
		wantErr(t, "malformed "+m, CheckPayloadKeys([]byte(m)), ErrInvalidExecPayload)
	}
	deep := strings.Repeat("[", maxKeyDepth+1) + strings.Repeat("]", maxKeyDepth+1)
	wantErr(t, "too deep", CheckPayloadKeys([]byte(deep)), ErrInvalidExecPayload)
	if err := CheckPayloadKeys([]byte(`{"a":1} [2] "s"`)); err != nil {
		t.Fatalf("a value sequence of benign keys: %v", err)
	}
	wantErr(t, "key in second value", CheckPayloadKeys([]byte(`{} {"x":{"password":1}}`)), ErrCredentialInPayload)
}

func TestGuardSerialized(t *testing.T) {
	isolateHome(t)
	det := newTestDetector(t)
	err := GuardSerialized(nil, []byte("x"))
	if !errors.Is(err, cascade.New(cascade.KindInvalidInput, "")) {
		t.Fatalf("nil detector: got %v, want KindInvalidInput", err)
	}
	if err := GuardSerialized(det, []byte(`{"message":"all clear"}`)); err != nil {
		t.Fatalf("clean bytes refused: %v", err)
	}
	canary := skCanary()
	err = GuardSerialized(det, []byte(`{"a":"`+canary+`","b":"`+canary+`"}`))
	wantErr(t, "canary", err, ErrCredentialInPayload)
	if strings.Contains(err.Error(), canary) || !strings.Contains(err.Error(), "2 certain") {
		t.Fatalf("error must name the count only: %v", err)
	}
}

func TestSanitizeError(t *testing.T) {
	isolateHome(t)
	det := newTestDetector(t)
	if got := SanitizeError(det, nil); got != "" {
		t.Fatalf("nil err = %q", got)
	}
	if got := SanitizeError(nil, errors.New("anything")); got != redacted {
		t.Fatalf("nil det = %q", got)
	}
	if got := SanitizeError(det, errors.New("plain failure")); got != "plain failure" {
		t.Fatalf("clean err = %q", got)
	}
}
