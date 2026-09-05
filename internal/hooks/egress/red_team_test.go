package egress

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// The synthetic red-team credential. It is generated for this test and
// exists nowhere else: a fixture holding a live value would make this
// package the leak it was written to prevent.
const (
	redTeamName   = "SK_RED_TEAM_TEST_0001"
	redTeamValue  = "sk-rtt-AAAA1234567890BBBBcc"
	fixturePlaced = "SECRET_PLACEHOLDER"
)

// redTeamVault returns the hermetic vault holding the synthetic secret.
func redTeamVault() map[string][]byte {
	return map[string][]byte{redTeamName: []byte(redTeamValue)}
}

// loadFixture reads a testdata fixture and plants the synthetic secret in
// it. The committed file carries a placeholder, never a value.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw := readSourceFile(t, "testdata/"+name)
	if !strings.Contains(raw, fixturePlaced) {
		t.Fatalf("fixture %s carries no %s slot", name, fixturePlaced)
	}
	return []byte(strings.ReplaceAll(raw, fixturePlaced, redTeamValue))
}

// assertNoCanary asserts the raw value is absent from payload in every
// encoding a recipient could trivially decode: the bytes themselves, hex,
// and both base64 alphabets. A placeholder that leaked the value in any
// of these would be reversible by whoever received it.
func assertNoCanary(t *testing.T, what string, payload []byte) {
	t.Helper()
	encodings := map[string][]byte{
		"raw":           []byte(redTeamValue),
		"hex":           []byte(hex.EncodeToString([]byte(redTeamValue))),
		"base64-std":    []byte(base64.StdEncoding.EncodeToString([]byte(redTeamValue))),
		"base64-url":    []byte(base64.URLEncoding.EncodeToString([]byte(redTeamValue))),
		"base64-rawstd": []byte(base64.RawStdEncoding.EncodeToString([]byte(redTeamValue))),
		"base64-rawurl": []byte(base64.RawURLEncoding.EncodeToString([]byte(redTeamValue))),
	}
	for encoding, canary := range encodings {
		if bytes.Contains(payload, canary) {
			t.Fatalf("%s: the synthetic secret is present in %s encoding", what, encoding)
		}
	}
}

// TestRedTeamSubstitution is the byte-for-byte assertion on the tool
// protocol leg: the raw value is gone and the vault-reference tag is
// present.
func TestRedTeamSubstitution(t *testing.T) {
	engine := newEngineOn(t, DefaultRegistry(), &mapVault{values: redTeamVault()})
	fixture := loadFixture(t, "mcp-response-fixture.json")
	if !bytes.Contains(fixture, []byte(redTeamValue)) {
		t.Fatal("the fixture does not carry the synthetic secret; the test would prove nothing")
	}
	out, err := engine.InterceptClass(context.Background(), EgressClassMCP, TierInternal, fixture)
	if err != nil {
		t.Fatalf("InterceptClass: %v", err)
	}
	assertNoCanary(t, "tool-protocol response", out)
	if !bytes.Contains(out, []byte("<apikey>"+redTeamName+"</apikey>")) {
		t.Fatalf("no vault-reference tag in the substituted response: %q", string(out))
	}
}

// TestRedTeamHookResponse repeats the assertions on the hook leg.
func TestRedTeamHookResponse(t *testing.T) {
	engine := newEngineOn(t, DefaultRegistry(), &mapVault{values: redTeamVault()})
	fixture := loadFixture(t, "hook-response-fixture.json")
	out, err := engine.InterceptClass(context.Background(), EgressClassHook, TierInternal, fixture)
	if err != nil {
		t.Fatalf("InterceptClass: %v", err)
	}
	assertNoCanary(t, "hook payload", out)
	if !bytes.Contains(out, []byte("<apikey>"+redTeamName+"</apikey>")) {
		t.Fatalf("no vault-reference tag in the substituted hook payload: %q", string(out))
	}
}

// TestRedTeamDisabledClassEgressesNothing is the disabled leg: the call
// is refused and the sink stays empty.
func TestRedTeamDisabledClassEgressesNothing(t *testing.T) {
	engine := newEngineOn(t, DefaultRegistry(), &mapVault{values: redTeamVault()})
	fixture := loadFixture(t, "mcp-response-fixture.json")
	var sink bytes.Buffer
	out, err := engine.InterceptClass(context.Background(), EgressClassTelemetry, TierInternal, fixture)
	if !errors.Is(err, ErrClassDisabled) {
		t.Fatalf("got %v, want ErrClassDisabled", err)
	}
	if out != nil {
		t.Fatalf("a disabled class returned %d bytes", len(out))
	}
	writeIfPermitted(&sink, out, err)
	if sink.Len() != 0 {
		t.Fatalf("%d bytes reached the sink from a disabled class", sink.Len())
	}
}

// TestRedTeamErrorPathsWriteNothing drives every refusal shape through a
// caller that writes to a sink only when the firewall admits, and asserts
// the sink is empty every time.
func TestRedTeamErrorPathsWriteNothing(t *testing.T) {
	fixture := loadFixture(t, "mcp-response-fixture.json")
	cases := []struct {
		name  string
		run   func(*Engine) ([]byte, error)
		wants error
	}{
		{"unknown class", func(e *Engine) ([]byte, error) {
			return e.InterceptClass(context.Background(), "no.such.class", TierInternal, fixture)
		}, ErrUnknownClass},
		{"disabled class", func(e *Engine) ([]byte, error) {
			return e.InterceptClass(context.Background(), EgressClassTelemetry, TierInternal, fixture)
		}, ErrClassDisabled},
		{"refused tier", func(e *Engine) ([]byte, error) {
			return e.InterceptClass(context.Background(), EgressClassMCP, TierLocalOnly, fixture)
		}, ErrSensitivityViolation},
		{"no capability", func(e *Engine) ([]byte, error) {
			return e.Intercept(context.Background(), Capability{}, TierInternal, fixture)
		}, ErrCapabilityRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := newEngineOn(t, DefaultRegistry(), &mapVault{values: redTeamVault()})
			var sink bytes.Buffer
			out, err := tc.run(engine)
			if !errors.Is(err, tc.wants) {
				t.Fatalf("got %v, want %v", err, tc.wants)
			}
			writeIfPermitted(&sink, out, err)
			assertNoCanary(t, tc.name+" sink", sink.Bytes())
			if sink.Len() != 0 {
				t.Fatalf("%d bytes egressed on the %s path", sink.Len(), tc.name)
			}
		})
	}
}

// TestRedTeamPartialFailureWritesNothing proves the partial-failure path:
// a vault whose second read fails, after the first has already produced a
// substitution, still writes nothing. A firewall that returned the
// partially substituted bytes here would leak everything it had not yet
// reached.
func TestRedTeamPartialFailureWritesNothing(t *testing.T) {
	vault := &failAfterVault{
		values: map[string][]byte{redTeamName: []byte(redTeamValue), "SECOND": []byte("second-value-0123456789")},
		after:  1,
	}
	engine := newEngineOn(t, DefaultRegistry(), vault)
	var sink bytes.Buffer
	out, err := engine.InterceptClass(context.Background(), EgressClassMCP, TierInternal,
		loadFixture(t, "mcp-response-fixture.json"))
	if err == nil {
		t.Fatal("a vault that failed partway admitted the write")
	}
	writeIfPermitted(&sink, out, err)
	assertNoCanary(t, "partial-failure sink", sink.Bytes())
	if sink.Len() != 0 {
		t.Fatalf("%d bytes egressed after a partial failure", sink.Len())
	}
}

// TestPlaceholderLeaksNoLength proves the placeholder is not a length
// oracle: two secrets of very different lengths, stored under the same
// name, substitute to byte-identical output.
func TestPlaceholderLeaksNoLength(t *testing.T) {
	short := "shortish-secret-value"
	long := strings.Repeat("x", 512) + "-tail"
	var rendered []string
	for _, value := range []string{short, long} {
		engine := newEngineOn(t, DefaultRegistry(), &mapVault{values: map[string][]byte{redTeamName: []byte(value)}})
		out, err := engine.InterceptClass(context.Background(), EgressClassMCP, TierInternal,
			[]byte("value: "+value+" end"))
		if err != nil {
			t.Fatalf("InterceptClass: %v", err)
		}
		rendered = append(rendered, string(out))
	}
	if rendered[0] != rendered[1] {
		t.Fatalf("the placeholder encodes the secret's length:\nshort: %q\nlong:  %q", rendered[0], rendered[1])
	}
}

// writeIfPermitted is the caller contract this package requires: write
// only what Intercept returned, and only when it returned no error.
func writeIfPermitted(sink *bytes.Buffer, out []byte, err error) {
	if err != nil {
		return
	}
	sink.Write(out)
}

// failAfterVault succeeds for the first `after` reads and fails from then
// on, so a substitution run can be interrupted mid-way.
type failAfterVault struct {
	values map[string][]byte
	after  int
	reads  int
}

func (v *failAfterVault) List(context.Context) ([]string, error) {
	out := make([]string, 0, len(v.values))
	for name := range v.values {
		out = append(out, name)
	}
	return out, nil
}

func (v *failAfterVault) Get(_ context.Context, name string) ([]byte, error) {
	v.reads++
	if v.reads > v.after {
		return nil, errors.New("vault: read failed partway through")
	}
	return v.values[name], nil
}
