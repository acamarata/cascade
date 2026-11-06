//go:build !windows

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
)

// scrubTestNames are test-only variable names standing in for the
// host-injected gateway names; the canary value is not a credential.
var scrubTestNames = []string{"GWYTEST_SCRUB_BASE", "GWYTEST_SCRUB_TOKEN", "GWYTEST_SCRUB_MANAGED"}

const scrubCanary = "canary-value-0f1e"

func TestDaemonStartScrubsCredentialEnv(t *testing.T) {
	for _, n := range scrubTestNames {
		t.Setenv(n, scrubCanary+n)
	}
	t.Setenv("GWYTEST_SCRUB_OTHER", "kept")
	creds, removed := scrubCredentialEnv(scrubTestNames, os.LookupEnv, os.Unsetenv)
	if !slices.Equal(removed, []string{"GWYTEST_SCRUB_BASE", "GWYTEST_SCRUB_MANAGED", "GWYTEST_SCRUB_TOKEN"}) {
		t.Fatalf("removed = %v", removed)
	}
	for _, kv := range os.Environ() {
		for _, n := range scrubTestNames {
			if strings.HasPrefix(kv, n+"=") {
				t.Fatalf("%s still in os.Environ after the scrub", n)
			}
		}
		if strings.Contains(kv, scrubCanary) {
			t.Fatal("canary value still in os.Environ after the scrub")
		}
	}
	creds.Admit(scrubTestNames[1:2])
	if got := creds.Getenv("GWYTEST_SCRUB_TOKEN"); got != scrubCanary+"GWYTEST_SCRUB_TOKEN" {
		t.Fatal("admitted name lost its snapshot value")
	}
	if got := creds.Getenv("GWYTEST_SCRUB_BASE"); got != "" {
		t.Fatal("unadmitted name kept a value after Admit")
	}
	if got := creds.Getenv("GWYTEST_SCRUB_OTHER"); got != "kept" {
		t.Fatalf("unscrubbed name = %q, want the process value", got)
	}
	relaunch := creds.RelaunchEnviron()
	if !slices.Contains(relaunch, "GWYTEST_SCRUB_TOKEN="+scrubCanary+"GWYTEST_SCRUB_TOKEN") {
		t.Fatal("relaunch environ lacks the admitted entry")
	}
	if slices.ContainsFunc(relaunch, func(kv string) bool { return strings.HasPrefix(kv, "GWYTEST_SCRUB_BASE=") }) {
		t.Fatal("relaunch environ carries an unadmitted entry")
	}
}

func TestScrubKeepsNameUnsetRefuses(t *testing.T) {
	vals := map[string]string{"A": "1", "B": "2"}
	lookup := func(n string) (string, bool) { v, ok := vals[n]; return v, ok }
	unset := func(n string) error {
		if n == "B" {
			return fmt.Errorf("refused")
		}
		delete(vals, n)
		return nil
	}
	creds, removed := scrubCredentialEnv([]string{"A", "B", "C", ""}, lookup, unset)
	if !slices.Equal(removed, []string{"A"}) {
		t.Fatalf("removed = %v", removed)
	}
	creds.Admit([]string{"A", "B"})
	if creds.Getenv("A") != "1" || creds.Getenv("B") != "2" || creds.Getenv("C") != "" {
		t.Fatal("snapshot values wrong")
	}
}

func TestCredentialEnvRedactsEveryFormatter(t *testing.T) {
	creds, _ := scrubCredentialEnv([]string{"X"}, func(string) (string, bool) { return scrubCanary, true }, func(string) error { return nil })
	creds.Admit([]string{"X"})
	var b strings.Builder
	slog.New(slog.NewTextHandler(&b, nil)).Info("m", "creds", creds)
	outs := []string{creds.String(), creds.GoString(), fmt.Sprintf("%v %+v %#v %s %q %x", creds, creds, creds, creds, creds, creds), b.String()}
	for _, o := range outs {
		if strings.Contains(o, scrubCanary) || !strings.Contains(o, "[redacted n=1]") {
			t.Fatalf("formatter output %q", o)
		}
	}
}

func TestScrubRunsBeforeAdmission(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "daemon_unix.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "composeDaemon" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("composeDaemon not found in daemon_unix.go")
	}
	first := map[string]token.Pos{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				if _, seen := first[id.Name]; !seen {
					first[id.Name] = call.Pos()
				}
			}
		}
		return true
	})
	scrub, ok := first["scrubCredentialEnv"]
	if !ok {
		t.Fatal("composeDaemon never calls scrubCredentialEnv")
	}
	load, ok := first["loadDaemonConfig"]
	if !ok || load > scrub {
		t.Fatal("scrubCredentialEnv must follow loadDaemonConfig")
	}
	for _, later := range []string{"openRuntimeStore", "checkBootBaseline", "wireBackgroundSubsystems", "startFleetRuntime"} {
		pos, ok := first[later]
		if !ok {
			t.Fatalf("composeDaemon no longer calls %s", later)
		}
		if pos < scrub {
			t.Fatalf("%s runs before scrubCredentialEnv", later)
		}
	}
}
