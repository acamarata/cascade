package antigravity

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

const fixturePath = "testdata/stream-json/agy-1.4.12-print.jsonl"

// isSentinel walks the Unwrap chain comparing identity, because
// *cascade.Error.Is matches any error of the same Kind.
func isSentinel(err, target error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == target {
			return true
		}
	}
	return false
}

func mustSentinel(t *testing.T, err, target error) {
	t.Helper()
	if !isSentinel(err, target) {
		t.Fatalf("error %v does not wrap %v by identity", err, target)
	}
}

// freshHome isolates the test from the caller's HOME.
func freshHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func fixtureLines(t *testing.T) [][]byte {
	t.Helper()
	b, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	return bytes.Split(bytes.TrimSpace(b), []byte("\n"))
}

func identity(b []byte) ([]byte, error) { return b, nil }

// cleanRun: the recorded fixture decodes in full through a pass-through
// filter.
func cleanRun(t *testing.T, lines [][]byte) {
	t.Helper()
	good, err := NewDecoder(identity)
	if err != nil {
		t.Fatal(err)
	}
	var last Event
	for i, l := range lines {
		if last, err = good.Decode(l); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
	}
	if got := good.Output(); got != "ok\n" || last.Type != EventResult || last.Status != "SUCCESS" {
		t.Fatalf("clean run: output %q, last %+v", got, last)
	}
}

// TestAntigravityOutputFilterErrorWithholds: a filter error on a text
// chunk withholds that chunk and every later one, fails with
// ErrEgressSubstitutionFailed by identity, and leaks nothing through
// Output, the returned event or the error text.
func TestAntigravityOutputFilterErrorWithholds(t *testing.T) {
	freshHome(t)
	lines := fixtureLines(t)
	cleanRun(t, lines)

	secret := "ok"
	refuse := func(b []byte) ([]byte, error) {
		if bytes.Contains(b, []byte(secret)) {
			return nil, errors.New("substitution failed for " + string(b))
		}
		return b, nil
	}
	d, err := NewDecoder(refuse)
	if err != nil {
		t.Fatal(err)
	}
	var failErr error
	for _, l := range lines {
		ev, err := d.Decode(l)
		if err != nil {
			if ev != (Event{}) {
				t.Fatalf("a failed decode returned event %+v", ev)
			}
			if failErr == nil {
				failErr = err
			} else if err != failErr {
				t.Fatalf("failure is not sticky: %v then %v", failErr, err)
			}
		}
	}
	if failErr != provider.ErrEgressSubstitutionFailed {
		t.Fatalf("err = %v, want ErrEgressSubstitutionFailed by identity", failErr)
	}
	if strings.Contains(failErr.Error(), secret) || d.Output() != "" || d.Err() != failErr {
		t.Fatalf("withheld chunk leaked: err %q output %q", failErr, d.Output())
	}

	if _, err := NewDecoder(nil); err == nil {
		t.Fatal("a nil filter was accepted")
	}
	noInternalImports(t)
}

// noInternalImports: the package never imports an internal/ path.
func noInternalImports(t *testing.T) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("listing package files: %v", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", f, err)
		}
		for _, im := range af.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if strings.HasPrefix(p, "internal/") || strings.Contains(p, "/internal/") || strings.HasSuffix(p, "/internal") {
				t.Fatalf("%s imports %s", f, p)
			}
		}
	}
}

// TestAntigravityOutputMalformed: shape violations fail typed and sticky.
func TestAntigravityOutputMalformed(t *testing.T) {
	freshHome(t)
	init := fixtureLines(t)[0]
	for _, bad := range []string{
		`not json`, `{"event":""}`, `{"event":"step_update","step_update":{"conversation_id":"c","state":"DONE"}}`,
		`{"event":"result","result":{"conversation_id":"c"}}`, string(init),
	} {
		d, _ := NewDecoder(identity)
		if _, err := d.Decode(init); err != nil {
			t.Fatal(err)
		}
		_, err := d.Decode([]byte(bad))
		mustSentinel(t, err, ErrMalformedEvent)
		if _, again := d.Decode(init); again != err {
			t.Fatalf("failure is not sticky for %s", bad)
		}
	}
}

const agyHelp = "  --output-format  Output format for print mode (text, json, stream-json) (default text)\n"

// TestAntigravityProtocolPinRefusal: a version or event-set mismatch is
// provider.ErrHarnessIncompatible (identity and message); a missing
// stream-json interface is ErrInterfaceAbsent; the two never alias.
func TestAntigravityProtocolPinRefusal(t *testing.T) {
	freshHome(t)
	if err := CheckHarness(PinnedAgyVersion+":", []byte(agyHelp)); err != nil {
		t.Fatalf("pinned harness refused: %v", err)
	}
	if v, err := Negotiate(provider.ProtocolRange{Min: "1", Max: "1"}); err != nil || v != ProtocolRevision {
		t.Fatalf("Negotiate({1,1}) = %q, %v", v, err)
	}

	incompatible := []error{
		CheckHarness("1.5.0", []byte(agyHelp)),
		negotiateErr(provider.ProtocolRange{Min: "2", Max: "3"}),
	}
	d, _ := NewDecoder(identity)
	if _, err := d.Decode(fixtureLines(t)[0]); err != nil {
		t.Fatal(err)
	}
	_, drift := d.Decode([]byte(`{"event":"tool_call","tool_call":{}}`))
	mustSentinel(t, drift, provider.ErrHarnessIncompatible)
	if want := `"tool_call" is not in protocol revision 1`; !strings.Contains(drift.Error(), want) {
		t.Fatalf("drift message %q lacks %q", drift, want)
	}
	for _, err := range incompatible {
		if err != provider.ErrHarnessIncompatible {
			t.Fatalf("err = %v, want ErrHarnessIncompatible by identity", err)
		}
		if err.Error() != "conflict: provider: no compatible protocol version in range" {
			t.Fatalf("message = %q", err.Error())
		}
	}

	absent := []error{CheckHarness(PinnedAgyVersion, []byte("--output-format text only"))}
	text, _ := NewDecoder(identity)
	_, err := text.Decode([]byte("ok"))
	absent = append(absent, err)
	for _, err := range absent {
		if err != ErrInterfaceAbsent {
			t.Fatalf("err = %v, want ErrInterfaceAbsent by identity", err)
		}
		if err.Error() != "unsupported: antigravity: stream-json output interface absent" {
			t.Fatalf("message = %q", err.Error())
		}
		if isSentinel(err, provider.ErrHarnessIncompatible) || errors.Is(err, provider.ErrHarnessIncompatible) {
			t.Fatal("ErrInterfaceAbsent aliases ErrHarnessIncompatible")
		}
	}
	if isSentinel(provider.ErrHarnessIncompatible, ErrInterfaceAbsent) {
		t.Fatal("ErrHarnessIncompatible aliases ErrInterfaceAbsent")
	}
}

func negotiateErr(peer provider.ProtocolRange) error {
	_, err := Negotiate(peer)
	return err
}
