// Purpose: tests for the stdio output filter: every chunk is scrubbed by
// value (raw, base64, hex) and then substituted by the real egress engine, a
// filter error withholds the chunk, and the captured fixture replays exactly.
// Constraints: credential-shaped samples are assembled at run time.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-03.

package agents

import (
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

const fixtureKey = "synthetic-fixture-key-7781"

func fixtureCanary() string {
	return "sk-" + "ant-api03-" + strings.Repeat("Qw8rT2", 8)
}

func TestWrapDriverOutputFiltersEveryChunk(t *testing.T) {
	lit := "Zq-literal-secret-value-0042"
	f := WrapDriverOutput(newEngine(t), provider.SensitivityInternal, [][]byte{[]byte(lit), nil}, func(string) {})
	forms := map[string]string{
		"raw": lit, "hex": hex.EncodeToString([]byte(lit)), "HEX": strings.ToUpper(hex.EncodeToString([]byte(lit))),
		"base64":     base64.StdEncoding.EncodeToString([]byte(lit)),
		"base64 raw": base64.RawStdEncoding.EncodeToString([]byte(lit)),
		"base64 url": base64.RawURLEncoding.EncodeToString([]byte(lit)),
		"embedded":   base64.StdEncoding.EncodeToString([]byte("k=" + lit + ";")),
		"embedded 2": base64.StdEncoding.EncodeToString([]byte("kk=" + lit + ";")),
	}
	for name, form := range forms {
		for _, chunk := range []string{"line: " + form + " end", form, form + form} {
			out, err := f([]byte(chunk))
			if err != nil || strings.Contains(string(out), form) || !strings.Contains(string(out), RedactedTag) {
				t.Fatalf("%s: chunk %q -> %q, %v; want the form scrubbed", name, chunk, out, err)
			}
		}
	}
	out, err := f([]byte("found " + fixtureCanary() + " here"))
	if err != nil || strings.Contains(string(out), fixtureCanary()) {
		t.Fatalf("detector shape passed through: %q, %v", out, err)
	}
	if out, err := f([]byte("plain text")); err != nil || string(out) != "plain text" {
		t.Fatalf("plain chunk = %q, %v; want unchanged", out, err)
	}
}

func TestWrapDriverOutputFilterErrorWithholdsLine(t *testing.T) {
	var reasons []string
	chunk := []byte("secret-looking " + fixtureKey)
	// The agent-driver class refuses restricted content, so the engine errors.
	f := WrapDriverOutput(newEngine(t), provider.SensitivityRestricted, nil, func(r string) { reasons = append(reasons, r) })
	out, err := f(chunk)
	if out != nil || err != provider.ErrEgressSubstitutionFailed { //nolint:errorlint // identity: errors.Is compares Kind only
		t.Fatalf("filter = %q, %v; want nil and ErrEgressSubstitutionFailed by identity", out, err)
	}
	if err.Error() != provider.ErrEgressSubstitutionFailed.Error() {
		t.Fatalf("message %q differs from the sentinel's", err.Error())
	}
	if len(reasons) != 1 || strings.Contains(reasons[0], fixtureKey) || reasons[0] == "" {
		t.Fatalf("fail called with %q; want exactly one content-free reason", reasons)
	}
	nilEngine := WrapDriverOutput(nil, provider.SensitivityInternal, nil, nil)
	if out, err := nilEngine(chunk); out != nil || err != provider.ErrEgressSubstitutionFailed { //nolint:errorlint // identity
		t.Fatalf("nil engine filter = %q, %v; want withheld", out, err)
	}
}

func TestSubstitutionFixtureReplay(t *testing.T) {
	dir := filepath.Join("testdata", "substitution-fixture")
	in, err := os.ReadFile(filepath.Join(dir, "input.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(dir, "expected.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	f := WrapDriverOutput(newEngine(t), provider.SensitivityInternal, [][]byte{[]byte(fixtureKey)}, func(string) {})
	var got strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(in), "\n"), "\n") {
		line = strings.ReplaceAll(strings.ReplaceAll(line, "KEY_PLACEHOLDER", fixtureKey), "CANARY_PLACEHOLDER", fixtureCanary())
		out, err := f([]byte(line))
		if err != nil {
			t.Fatalf("filter(%q): %v", line, err)
		}
		got.Write(out)
		got.WriteString("\n")
	}
	if got.String() != string(want) {
		t.Fatalf("replay differs from the captured expectation:\n got: %s\nwant: %s", got.String(), want)
	}
	if strings.Contains(got.String(), fixtureKey) || strings.Contains(got.String(), fixtureCanary()) {
		t.Fatal("a secret survived the replay")
	}
}
