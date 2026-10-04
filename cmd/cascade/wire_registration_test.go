//go:build !windows

// Purpose: the registration contract's tests: what the roots may hold, that
//
//	registrations are unique and ordered, that a scratch noun and method need
//	no root edit, and that a failing wire is named and stops the run.
//
// Constraints: test-only registrations are added inside a test and removed by
//
//	t.Cleanup. testdata/daemon_rpc_methods.golden is generated, never
//	hand-edited: regenerate with CASCADE_TESTKIT_UPDATE_GOLDEN=1.
//
// SPORT: cmd/cascade composition root tests (P1-CORE-01).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// scratchRegistrations snapshots both registration lists and restores them
// when the test ends, so a test-only registration never leaks.
func scratchRegistrations(t *testing.T) {
	t.Helper()
	mounts, wires := rootMounts, daemonRegistrations
	t.Cleanup(func() { rootMounts, daemonRegistrations = mounts, wires })
}

var subsystemCall = regexp.MustCompile(`^(mount.*|new.*Cmd)$`)

// forbiddenCalls returns the plain-identifier calls in fn's body whose name
// satisfies bad.
func forbiddenCalls(fn *ast.FuncDecl, bad func(string) bool) []string {
	var out []string
	ast.Inspect(fn, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := c.Fun.(type) {
		case *ast.Ident:
			if bad(f.Name) {
				out = append(out, f.Name)
			}
		case *ast.SelectorExpr:
			if id, ok := f.X.(*ast.Ident); ok && id.Name == "daemon" && strings.HasPrefix(f.Sel.Name, "Register") {
				out = append(out, "daemon."+f.Sel.Name)
			}
		}
		return true
	})
	return out
}

// TestCompositionRootsHoldNoSubsystemNames proves, by go/parser, that no
// mount*/new*Cmd call lives in root.go or mountSubcommands and no
// register*/wire*/daemon.Register* call lives in buildDaemonRegistry.
func TestCompositionRootsHoldNoSubsystemNames(t *testing.T) {
	rootBad := func(n string) bool { return n != "mountSubcommands" && subsystemCall.MatchString(n) }
	var checked int
	for _, d := range parseCmdFile(t, "root.go").Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name != "mountCICmd" {
			checked++
			if bad := forbiddenCalls(fn, rootBad); len(bad) > 0 {
				t.Errorf("root.go %s calls %v; a noun is a mount_<noun>.go registration", fn.Name.Name, bad)
			}
		}
	}
	ms := funcDecls(t, "mountSubcommands")["root_mounts.go"]
	if ms == nil || checked == 0 {
		t.Fatal("mountSubcommands or root.go functions not found")
	}
	if bad := forbiddenCalls(ms, func(n string) bool { return n != "sort.SliceStable" && subsystemCall.MatchString(n) }); len(bad) > 0 {
		t.Errorf("mountSubcommands calls %v", bad)
	}
	daemonBad := func(n string) bool {
		return strings.HasPrefix(n, "register") || strings.HasPrefix(n, "wire")
	}
	bdr := funcDecls(t, "buildDaemonRegistry")["daemon_unix_run.go"]
	if bdr == nil {
		t.Fatal("daemon_unix_run.go declares no buildDaemonRegistry")
	}
	if bad := forbiddenCalls(bdr, daemonBad); len(bad) > 0 {
		t.Errorf("buildDaemonRegistry calls %v; a subsystem is a wire_<subsystem>.go registration", bad)
	}
	if n := lineCount(t, "daemon_unix_run.go"); n > 250 {
		t.Errorf("daemon_unix_run.go has %d lines, want <= 250", n)
	}
	if n := lineCount(t, "daemon_unix.go"); n > 300 {
		t.Errorf("daemon_unix.go has %d lines, want <= 300", n)
	}
}

func noopWire(*daemonWiring) error { return nil }

// TestCompositionRegistrationsUnique runs each rule as a table case against
// the validator, then holds the live registrations to it.
func TestCompositionRegistrationsUnique(t *testing.T) {
	cases := []struct {
		name string
		regs []daemonRegistration
		want string
	}{
		{"duplicate name", []daemonRegistration{{"a", phaseCore, 1, noopWire}, {"a", phaseLate, 2, noopWire}}, "duplicate daemon registration name"},
		{"duplicate phase and order", []daemonRegistration{{"a", phaseLate, 1, noopWire}, {"b", phaseLate, 1, noopWire}}, "share phase"},
		{"second MCP-last", []daemonRegistration{{"a", phaseMCPLast, 1, noopWire}, {"b", phaseMCPLast, 2, noopWire}}, "phaseMCPLast"},
		{"nil wire", []daemonRegistration{{"a", phaseCore, 1, nil}}, "nil Wire"},
	}
	for _, tc := range cases {
		err := validateDaemonRegistrations(tc.regs)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", tc.name, err, tc.want)
		}
	}
	if err := validateRootMounts([]rootMount{{Name: "x", Mount: func(*cobra.Command) {}}, {Name: "x", Mount: func(*cobra.Command) {}}}); err == nil {
		t.Error("duplicate root mount name was accepted")
	}
	if err := validateDaemonRegistrations(daemonRegistrations); err != nil {
		t.Errorf("live daemon registrations: %v", err)
	}
	if err := validateRootMounts(rootMounts); err != nil {
		t.Errorf("live root mounts: %v", err)
	}
	if len(daemonRegistrations) < 10 || len(rootMounts) < 20 {
		t.Errorf("implausibly few registrations: %d daemon, %d root", len(daemonRegistrations), len(rootMounts))
	}
}

// TestScratchRegistrationNeedsNoRootEdit registers a hidden root noun and a
// daemon method inside the test, and finds both with no root, compose or
// existing mount/wire file involved.
func TestScratchRegistrationNeedsNoRootEdit(t *testing.T) {
	scratchRegistrations(t)
	_ = registerRootMount(rootMount{Name: "scratch-noun", Order: 9999, Mount: func(root *cobra.Command) {
		root.AddCommand(&cobra.Command{Use: "scratch-noun", Hidden: true})
	}})
	_ = registerDaemonWiring(daemonRegistration{Name: "scratch-ping", Phase: phaseLate, Order: 9999,
		Wire: func(w *daemonWiring) error {
			w.Registry.Register("scratch.ping", func(context.Context, json.RawMessage) (any, error) { return "pong", nil })
			return nil
		}})
	found := false
	for _, c := range newRootCmd().Commands() {
		found = found || (c.Name() == "scratch-noun" && c.Hidden)
	}
	if !found {
		t.Error("hidden scratch-noun is not reachable from newRootCmd()")
	}
	if c := composeForTest(t); !c.Registry.Registered("scratch.ping") {
		t.Error("scratch.ping is not registered on the composeDaemon registry")
	}
}

// TestDaemonWiringErrorNamesEntry is the fail-closed probe: a failing Wire
// aborts composeDaemon with an error that names the entry and wraps the cause,
// and no later-phase registration runs.
func TestDaemonWiringErrorNamesEntry(t *testing.T) {
	scratchRegistrations(t)
	boom := errors.New("injected wire failure")
	laterRan := false
	_ = registerDaemonWiring(daemonRegistration{Name: "scratch-fail", Phase: phaseLate, Order: 9999,
		Wire: func(*daemonWiring) error { return boom }})
	_ = registerDaemonWiring(daemonRegistration{Name: "scratch-later", Phase: phaseFleet, Order: 9999,
		Wire: func(*daemonWiring) error { laterRan = true; return nil }})
	t.Setenv("HOME", t.TempDir())
	_, cleanups, err := composeDaemon(context.Background(), newRunTestDeps(t, nonexistentExecutable(t)), nil)
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err == nil {
		t.Fatal("composeDaemon succeeded despite a failing registration")
	}
	if !errors.Is(err, boom) || errors.Unwrap(err) != boom {
		t.Errorf("error does not wrap the injected cause by identity: %v", err)
	}
	if !strings.Contains(err.Error(), `"scratch-fail"`) || !strings.Contains(err.Error(), boom.Error()) {
		t.Errorf("error %q does not name the entry and cause", err)
	}
	if laterRan {
		t.Error("a later-phase registration ran after the failure")
	}
}

// TestDaemonWiringOrderConstraints pins the orderings the daemon depends on.
func TestDaemonWiringOrderConstraints(t *testing.T) {
	regs := sortedDaemonRegistrations(daemonRegistrations)
	pos := map[string]int{}
	var first string
	mcpLast := 0
	for i, r := range regs {
		pos[r.Name] = i
		if first == "" && r.Phase != phaseMiddleware {
			first = r.Name
		}
		if r.Phase == phaseMCPLast {
			mcpLast++
		}
	}
	if first != "status-get" {
		t.Errorf("first non-middleware registration = %q, want status-get", first)
	}
	if pos["context-engine"] >= pos["conductor"] {
		t.Error("context-engine must run before conductor (ApplyGraphSchema needs the scope schema)")
	}
	if last := regs[len(regs)-1]; last.Name != "socket-mcp" || last.Phase != phaseMCPLast || mcpLast != 1 {
		t.Errorf("last = %q (phase %d), phaseMCPLast count %d; want socket-mcp alone, last", last.Name, last.Phase, mcpLast)
	}
}

// TestDaemonRPCMethodSet records every method the composed registry serves,
// dynamic plugin-built names included, in the generated golden.
func TestDaemonRPCMethodSet(t *testing.T) {
	c := composeForTest(t)
	methods := c.Registry.Methods()
	if len(methods) < 20 {
		t.Fatalf("composed registry serves only %d methods", len(methods))
	}
	compareOrUpdateGolden(t, "testdata/daemon_rpc_methods.golden", []byte(strings.Join(methods, "\n")+"\n"))
}

// TestEmbeddedWarningSuppressionByAnnotation (audit S-2): the embedded-mode
// warning is suppressed for status, daemon run and run because those commands
// carry an annotation, not because of a CommandPath string. A hypothetical
// `status detail` child inherits it, `daemon status` keeps the warning, and a
// command with the annotation but an unlisted path is still suppressed.
func TestEmbeddedWarningSuppressionByAnnotation(t *testing.T) {
	root := newRootCmd()
	find := func(path ...string) *cobra.Command {
		c, _, err := root.Find(path)
		if err != nil || c == root {
			t.Fatalf("command %v not found: %v", path, err)
		}
		return c
	}
	for _, path := range [][]string{{"status"}, {"run"}, {"daemon", "run"}} {
		if !embeddedWarningSuppressed(find(path...)) {
			t.Errorf("%v is not marked to suppress the embedded warning", path)
		}
	}
	for _, path := range [][]string{{"daemon", "status"}, {"recall"}, {"fleet"}} {
		if embeddedWarningSuppressed(find(path...)) {
			t.Errorf("%v wrongly suppresses the embedded warning", path)
		}
	}
	detail := &cobra.Command{Use: "detail"}
	find("status").AddCommand(detail)
	if !embeddedWarningSuppressed(detail) {
		t.Error("a status child does not inherit the suppression")
	}

	home := shortCascadeHome(t)
	t.Setenv("CASCADE_HOME", home)
	t.Setenv("HOME", home)
	globalFlags = GlobalFlags{}
	out := captureRealStderr(t, func() { probeDaemonlessAndAttach(context.Background(), detail) })
	if strings.Contains(out, "embedded (daemonless) mode") {
		t.Errorf("a status child printed the embedded warning: %q", out)
	}
	odd := &cobra.Command{Use: "renamed", Annotations: map[string]string{embeddedWarningAnnotation: "suppress"}}
	root.AddCommand(odd)
	out = captureRealStderr(t, func() { probeDaemonlessAndAttach(context.Background(), odd) })
	if strings.Contains(out, "embedded (daemonless) mode") {
		t.Errorf("an annotated command with another name printed the warning: %q", out)
	}
}
