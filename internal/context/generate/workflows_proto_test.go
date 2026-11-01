package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ccFixture is one replayable harness CLI probe from testdata/real-cc-workflows.
type ccFixture struct {
	Tool       string `json:"tool"`
	Version    string `json:"version"`
	CapturedAt string `json:"captured_at"`
	Command    string `json:"command"`
	Stdout     string `json:"stdout"`
	ExitCode   int    `json:"exit_code"`
	Synthetic  bool   `json:"synthetic"`
}

// probeBin is the path the stand-in LookPath resolves the harness to.
const probeBin = "/opt/harness/bin/claude"

// runCall is one recorded invocation of a stand-in Run.
type runCall struct {
	bin  string
	args []string
	env  []string
}

// loadCCFixture reads one fixture strictly.
func loadCCFixture(t *testing.T, name string) ccFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "real-cc-workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f ccFixture
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return f
}

// replayProbe is an ExecProbe whose Run replays version and help output
// and records every call.
func replayProbe(versionOut string, versionExit int, helpOut string, helpExit int, calls *[]runCall) *ExecProbe {
	return &ExecProbe{
		LookPath: func(string) (string, error) { return probeBin, nil },
		Run: func(_ context.Context, bin string, args, env []string) ([]byte, int, error) {
			*calls = append(*calls, runCall{bin, args, env})
			if len(args) == 1 && args[0] == "--version" {
				return []byte(versionOut), versionExit, nil
			}
			return []byte(helpOut), helpExit, nil
		},
	}
}

// fixtureProbe replays a fixture file through ExecProbe.
func fixtureProbe(t *testing.T, name string, calls *[]runCall) *ExecProbe {
	f := loadCCFixture(t, name)
	return replayProbe(f.Version+" (claude CLI)\n", 0, f.Stdout, f.ExitCode, calls)
}

// stubProbe returns a fixed result and counts its calls.
type stubProbe struct {
	r   ProbeResult
	err error
	n   int
}

func (s *stubProbe) Probe(context.Context) (ProbeResult, error) { s.n++; return s.r, s.err }

func TestWorkflowsProtocolRevisionMapping(t *testing.T) {
	isolate(t)
	captured := loadCCFixture(t, "unsupported-fixture.json")
	if !strings.Contains(captured.Stdout, "workflows") {
		t.Fatal("the real capture should mention workflows in prose only; the prose case is not exercised")
	}
	prose := "Usage: x\n\nOptions:\n  --flag   loads workflows from disk\n\nCommands:\n  agents   Manage agents\n" +
		"                                        and workflows\n  run|workflows-x   other\n"
	cases := []struct {
		name, version, help string
		vexit, hexit        int
		want                string
	}{
		{"unparsable version", "claude CLI, build unknown\n", "Commands:\n  workflows  Run\n", 0, 0, resultIndeterminate},
		{"version exit", "2.1.300 (claude CLI)\n", "Commands:\n  workflows  Run\n", 1, 0, resultIndeterminate},
		{"help exit", "2.1.300 (claude CLI)\n", "Commands:\n  workflows  Run\n", 0, 2, resultIndeterminate},
		{"no commands section", "2.1.300 (claude CLI)\n", "Usage: claude\n  workflows  Run\n", 0, 0, resultIndeterminate},
		{"prose only", "2.1.300 (claude CLI)\n", prose, 0, 0, resultUnsupported},
		{"alias", "2.1.300 (claude CLI)\n", "Commands:\n  wf|workflows  Run\n", 0, 0, resultSupported},
		{"newer minor", "2.2.0\n", "Commands:\n  workflows  Run\n", 0, 0, resultSupported},
		{"older major", "1.9.999\n", "Commands:\n  workflows  Run\n", 0, 0, resultOutOfRange},
	}
	for _, c := range cases {
		var calls []runCall
		r, _ := Negotiate(t.Context(), replayProbe(c.version, c.vexit, c.help, c.hexit, &calls))
		if r.Result != c.want {
			t.Errorf("%s: result %q, want %q", c.name, r.Result, c.want)
		}
	}
	for name, want := range map[string]string{"supported-fixture.json": resultSupported,
		"unsupported-fixture.json": resultUnsupported, "out-of-range-fixture.json": resultOutOfRange} {
		var calls []runCall
		r, _ := Negotiate(t.Context(), fixtureProbe(t, name, &calls))
		if r.Result != want || r.Binary != probeBin {
			t.Errorf("%s: %+v, want %s", name, r, want)
		}
		if len(calls) != 2 || calls[0].args[0] != "--version" || calls[1].args[0] != "--help" ||
			len(calls[0].args) != 1 || len(calls[1].args) != 1 {
			t.Errorf("%s: argv %+v, want [--version] then [--help]", name, calls)
		}
	}
}

func TestWorkflowsProtocolRangeNegotiated(t *testing.T) {
	isolate(t)
	if ProtocolMin != 1 || ProtocolMax != 1 || MinCLIVersion != "2.1.273" {
		t.Fatalf("protocol constants %d..%d %s", ProtocolMin, ProtocolMax, MinCLIVersion)
	}
	if revisionFor(resultSupported) != 1 || revisionFor(resultOutOfRange) != 0 || revisionFor("") != 0 {
		t.Fatal("revision mapping wrong")
	}
	var calls []runCall
	p := fixtureProbe(t, "supported-fixture.json", &calls)
	for i := 1; i <= 3; i++ {
		if r, err := Negotiate(t.Context(), p); err != nil || r.Result != resultSupported {
			t.Fatalf("call %d: %+v %v", i, r, err)
		}
		if len(calls) != 2*i {
			t.Fatalf("call %d: %d invocations, want %d (every call re-probes)", i, len(calls), 2*i)
		}
	}
	for _, c := range []struct {
		a, b string
		want int
	}{{"2.1.273", "2.1.273", 0}, {"2.1.272", "2.1.273", -1}, {"2.1.274", "2.1.273", 1}, {"3.0.0", "2.9.9", 1}, {"2.0.999", "2.1.0", -1}} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if v, ok := parseVersion([]byte(strings.Repeat("x", 100))); ok || len(v) != 64 {
		t.Fatalf("parseVersion of junk = %q %v, want a 64-byte prefix and false", v, ok)
	}
}

func TestWorkflowsProbeDedupAndInvalidate(t *testing.T) {
	isolate(t)
	s := &stubProbe{r: ProbeResult{Result: resultUnsupported, Binary: probeBin, Version: "2.1.316"}}
	first, _ := Negotiate(t.Context(), s)
	again, _ := Negotiate(t.Context(), s)
	if first.Seq == 0 || again.Seq != first.Seq || s.n != 2 {
		t.Fatalf("repeat: seq %d then %d, %d probes; want one record re-probed", first.Seq, again.Seq, s.n)
	}
	if ledger.current != first {
		t.Fatalf("recorded %+v, want %+v", ledger.current, first)
	}
	s.r.Version = "2.1.317"
	bumped, _ := Negotiate(t.Context(), s)
	if bumped.Seq <= first.Seq || ledger.current.Version != "2.1.317" {
		t.Fatalf("changed version kept seq %d (was %d)", bumped.Seq, first.Seq)
	}
	s.r.Binary = "/elsewhere/claude"
	moved, _ := Negotiate(t.Context(), s)
	if moved.Seq <= bumped.Seq {
		t.Fatalf("changed binary kept seq %d (was %d)", moved.Seq, bumped.Seq)
	}
	s.r.ExitCode = 3
	if exited, _ := Negotiate(t.Context(), s); exited.Seq <= moved.Seq {
		t.Fatalf("changed exit kept seq %d", exited.Seq)
	}
}

func TestWorkflowsErrHarnessIncompatibleUniform(t *testing.T) {
	isolate(t)
	const msg = "generate: harness workflows surface incompatible"
	for _, res := range []string{resultUnsupported, resultOutOfRange, resultIndeterminate, "bogus", ""} {
		r, err := Negotiate(t.Context(), &stubProbe{r: ProbeResult{Result: res, Binary: probeBin, Version: "2.1.300"}})
		if err != ErrHarnessIncompatible {
			t.Fatalf("%q: error %v is not ErrHarnessIncompatible by identity", res, err)
		}
		wantKind(t, err, cascade.KindUnsupported, msg)
		if !knownResult(r.Result) {
			t.Fatalf("%q: recorded unknown result %q", res, r.Result)
		}
	}
	if _, err := Negotiate(t.Context(), &stubProbe{r: ProbeResult{Result: resultSupported}}); err != nil {
		t.Fatalf("supported: %v", err)
	}
	_, err := Negotiate(t.Context(), nil)
	wantKind(t, err, cascade.KindInvalidInput, "generate: nil workflows probe")
	boom := cascade.New(cascade.KindUnavailable, "probe broke")
	if _, err := Negotiate(t.Context(), &stubProbe{err: boom}); err != boom {
		t.Fatalf("probe error = %v, want it returned as is", err)
	}
	if got := unsupportedSentinels(t); strings.Join(got, ",") != "ErrHarnessIncompatible" {
		t.Fatalf("KindUnsupported sentinels = %v, want only ErrHarnessIncompatible", got)
	}
}

// unsupportedSentinels lists the package-level vars of non-test files
// built with cascade.New(cascade.KindUnsupported, ...).
func unsupportedSentinels(t *testing.T) []string {
	t.Helper()
	paths, _ := filepath.Glob("*.go")
	var out []string
	fset := token.NewFileSet()
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, sp := range g.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, v := range vs.Values {
					if call, ok := v.(*ast.CallExpr); ok && len(call.Args) > 0 && isSel(call.Args[0], "KindUnsupported") {
						out = append(out, vs.Names[i].Name)
					}
				}
			}
		}
	}
	return out
}

// isSel reports whether e is a selector naming sel.
func isSel(e ast.Expr, sel string) bool {
	s, ok := e.(*ast.SelectorExpr)
	return ok && s.Sel.Name == sel
}
