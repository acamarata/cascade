//go:build !windows

// Purpose: TestDaemonLifecycleLint, the source check that keeps the daemon on
//
//	one run context and one goroutine supervisor. Over every non-test
//	cmd/cascade daemon_unix*.go, plugin_rpc*.go, wire_*.go, compose_daemon.go
//	and internal/daemon/*.go file it requires (a) every context.Background()
//	call to match an owned allowlist entry, (b) every go statement to be a
//	listed supervisor or joined site, and (c) no non-test cmd/cascade file to
//	assign daemonCustodyHook.
//
// Constraints: an entry is (file, enclosing func, callee) with an exact
//
//	count, an owner and a reason; an unlisted site, a count mismatch, a stale
//	entry or an ownerless entry fails. Seeded sources prove each named
//	regression fails the check.
//
// SPORT: cmd/cascade/daemon (ADD, lifecycle lint).
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// lifecycleSite is one (file, enclosing func, callee) key.
type lifecycleSite struct{ file, fn, callee string }

// lifecycleEntry is one owned allowlist row.
type lifecycleEntry struct {
	lifecycleSite
	count         int
	owner, reason string
}

const lintOwner = "P1-CORE-05"

// backgroundAllow lists the context.Background() calls that stay, with why.
var backgroundAllow = []lifecycleEntry{
	{lifecycleSite{"cmd/cascade/daemon_unix_recall_what.go", "recallWhatConversationLeg", "conversation.ApplyConversationSchema"}, 1, lintOwner, "startup-only schema apply"},
	{lifecycleSite{"cmd/cascade/daemon_unix_recall_what.go", "recallWhatScopeResolver", "scope.ApplyScopeSchema"}, 1, lintOwner, "startup-only schema apply"},
	{lifecycleSite{"cmd/cascade/daemon_unix_reload.go", "wireBackgroundSubsystems", "schedCleanup"}, 1, lintOwner, "cleanup must run after cancellation"},
	{lifecycleSite{"cmd/cascade/daemon_unix_usage.go", "rateCardEstimator.EstimateCostMicroUSD", "e.reg.GetProvider"}, 1, lintOwner, "accounting hook without a ctx parameter"},
	{lifecycleSite{"cmd/cascade/daemon_unix_run.go", "buildDaemonRegistry", "<assign daemonWiring.Ctx>"}, 1, lintOwner, "no-option default for a test-built server"},
	{lifecycleSite{"internal/daemon/context_assemble.go", "RegisterContextAssembleHandler", "scope.ApplyScopeSchema"}, 1, lintOwner, "startup-only schema apply"},
	{lifecycleSite{"internal/daemon/context_scope.go", "RegisterContextScopeHandler", "scope.ApplyScopeSchema"}, 1, lintOwner, "startup-only schema apply"},
	{lifecycleSite{"internal/daemon/fleet_mode_rpc.go", "RegisterFleetModeHandler", "economics.ApplyMigrationSchema"}, 1, lintOwner, "startup-only schema apply"},
	{lifecycleSite{"internal/daemon/quota_rpc.go", "RegisterFleetQuotaHandler", "topology.ApplyMigrationSchema"}, 1, lintOwner, "startup-only schema apply"},
	{lifecycleSite{"internal/daemon/status_widget.go", "RegisterStatusWidgetHandler", "openWidgetJobsStore"}, 1, lintOwner, "open at registration"},
	{lifecycleSite{"internal/daemon/status_widget.go", "RegisterStatusWidgetHandler", "emitStatusWidgetChanged"}, 1, "P1-WID-08", "run-time emit residue that ticket removes"},
	{lifecycleSite{"internal/daemon/lifecycle_unix_serve.go", "shutdownRPCServer", "context.WithTimeout"}, 1, lintOwner, "grace must outlive the cancelled ctx"},
	{lifecycleSite{"internal/daemon/subsystems.go", "Manifest.logf", "m.log.Log"}, 1, lintOwner, "logging has no request ctx"},
}

// goStmtAllow lists the go statements that stay: the supervisor's own two
// and the goroutines joined outside it.
var goStmtAllow = []lifecycleEntry{
	{lifecycleSite{"internal/daemon/subsystems_goroutines.go", "Manifest.goSubsystem", "go"}, 1, lintOwner, "the supervisor: tracked by Manifest.running"},
	{lifecycleSite{"internal/daemon/subsystems_goroutines.go", "Manifest.waitContext", "go"}, 1, lintOwner, "bounded-join waiter: exits with the last supervised goroutine"},
	{lifecycleSite{"internal/daemon/lifecycle_unix_serve.go", "serveRPC", "go"}, 1, lintOwner, "joined through serveDone"},
	{lifecycleSite{"internal/daemon/lifecycle_unix_handoff.go", "handleUpgradeWithSignals", "go"}, 1, lintOwner, "signal watcher cancelled or joined before hand-off returns"},
	{lifecycleSite{"internal/daemon/upgrade_conntracker.go", "ConnTracker.Done", "go"}, 1, lintOwner, "joined through its WaitGroup"},
}

// lifecycleScope returns the repo-relative non-test files the lint covers.
func lifecycleScope(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, pattern := range []string{"daemon_unix*.go", "plugin_rpc*.go", "wire_*.go", "compose_daemon.go", "../../internal/daemon/*.go"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		for _, m := range matches {
			if !strings.HasSuffix(m, "_test.go") {
				out = append(out, repoRel(m))
			}
		}
	}
	if len(out) < 40 {
		t.Fatalf("lint scope holds %d files; the globs no longer match the tree", len(out))
	}
	return out
}

// repoRel maps a path relative to cmd/cascade onto the repo root.
func repoRel(p string) string {
	if strings.HasPrefix(p, "../../") {
		return strings.TrimPrefix(p, "../../")
	}
	return "cmd/cascade/" + p
}

// diskPath maps a repo-relative path back onto cmd/cascade's directory.
func diskPath(rel string) string {
	if strings.HasPrefix(rel, "cmd/cascade/") {
		return strings.TrimPrefix(rel, "cmd/cascade/")
	}
	return "../../" + rel
}

// collectSites parses src (nil reads rel from disk) and adds its Background
// and go-statement sites to bg and gos.
func collectSites(t *testing.T, rel string, src any, bg, gos map[lifecycleSite]int) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), diskPath(rel), src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	fn := "<package>"
	var stack []ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			if _, ok := stack[len(stack)-1].(*ast.FuncDecl); ok {
				fn = "<package>"
			}
			stack = stack[:len(stack)-1]
			return true
		}
		if fd, ok := n.(*ast.FuncDecl); ok {
			fn = funcDeclName(fd)
		}
		if _, ok := n.(*ast.GoStmt); ok {
			gos[lifecycleSite{rel, fn, "go"}]++
		}
		if call, ok := n.(*ast.CallExpr); ok && exprText(call.Fun) == "context.Background" {
			bg[lifecycleSite{rel, fn, backgroundCallee(stack)}]++
		}
		stack = append(stack, n)
		return true
	})
}

// backgroundCallee names what consumes the Background call on top of stack's
// parent chain: the call it is an argument of, or the field or variable it
// is assigned to.
func backgroundCallee(stack []ast.Node) string {
	if len(stack) == 0 {
		return "<other>"
	}
	switch p := stack[len(stack)-1].(type) {
	case *ast.CallExpr:
		return exprText(p.Fun)
	case *ast.KeyValueExpr:
		if len(stack) > 1 {
			if lit, ok := stack[len(stack)-2].(*ast.CompositeLit); ok && lit.Type != nil {
				return "<assign " + exprText(lit.Type) + "." + exprText(p.Key) + ">"
			}
		}
	case *ast.AssignStmt:
		return "<assign " + exprText(p.Lhs[0]) + ">"
	}
	return "<other>"
}

// funcDeclName is Recv.Name for a method (pointer stripped), else Name.
func funcDeclName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	return strings.TrimPrefix(exprText(fd.Recv.List[0].Type), "*") + "." + fd.Name.Name
}

// exprText renders an expression as source text.
func exprText(e ast.Expr) string { return types.ExprString(e) }

// lifecycleViolations compares found sites with allow and returns every
// unlisted site, count mismatch, stale entry and ownerless entry, sorted.
func lifecycleViolations(kind string, found map[lifecycleSite]int, allow []lifecycleEntry) []string {
	var out []string
	listed := map[lifecycleSite]bool{}
	for _, e := range allow {
		listed[e.lifecycleSite] = true
		switch got := found[e.lifecycleSite]; {
		case e.owner == "" || e.reason == "":
			out = append(out, fmt.Sprintf("%s entry %+v has no owner or reason", kind, e.lifecycleSite))
		case got == 0:
			out = append(out, fmt.Sprintf("stale %s entry %+v: no such site", kind, e.lifecycleSite))
		case got != e.count:
			out = append(out, fmt.Sprintf("%s %+v: %d sites, entry allows %d", kind, e.lifecycleSite, got, e.count))
		}
	}
	for site, n := range found {
		if !listed[site] {
			out = append(out, fmt.Sprintf("unlisted %s %+v (x%d)", kind, site, n))
		}
	}
	sort.Strings(out)
	return out
}

// hookAssignments returns every non-test cmd/cascade file:line that assigns
// daemonCustodyHook. srcs adds seeded sources by name.
func hookAssignments(t *testing.T, srcs map[string]string) []string {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var out []string
	check := func(name string, src any) {
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, name, src, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok {
				for _, lhs := range as.Lhs {
					if exprText(lhs) == "daemonCustodyHook" {
						out = append(out, fset.Position(as.Pos()).String())
					}
				}
			}
			if vs, ok := n.(*ast.ValueSpec); ok && len(vs.Values) > 0 && vs.Names[0].Name == "daemonCustodyHook" {
				out = append(out, fset.Position(vs.Pos()).String())
			}
			return true
		})
	}
	for _, name := range names {
		if !strings.HasSuffix(name, "_test.go") {
			check(name, nil)
		}
	}
	for name, src := range srcs {
		check(name, src)
	}
	return out
}

// TestDaemonLifecycleLint runs the check over the real tree, then proves each
// named regression fails it.
func TestDaemonLifecycleLint(t *testing.T) {
	bg, gos := map[lifecycleSite]int{}, map[lifecycleSite]int{}
	for _, rel := range lifecycleScope(t) {
		collectSites(t, rel, nil, bg, gos)
	}
	for _, v := range append(lifecycleViolations("Background", bg, backgroundAllow), lifecycleViolations("go", gos, goStmtAllow)...) {
		t.Error(v)
	}
	if hits := hookAssignments(t, nil); len(hits) != 0 {
		t.Errorf("daemonCustodyHook assigned outside a test file: %v", hits)
	}
	t.Run("seeded", func(t *testing.T) { seededLifecycleRegressions(t, bg, gos) })
}

// seededLifecycleRegressions adds one regression at a time to copies of the
// real tree's sites and requires each to produce a violation.
func seededLifecycleRegressions(t *testing.T, bg, gos map[lifecycleSite]int) {
	seeds := []struct{ name, rel, src string }{
		{"second Background in wireBackgroundSubsystems", "cmd/cascade/daemon_unix_reload.go", "package main\nfunc wireBackgroundSubsystems() { schedCleanup(context.Background()) }\n"},
		{"Background back in wireJobRPC's call", "cmd/cascade/daemon_unix_run_fleetjobs.go", "package main\nfunc wireJobRPCHandlers() { wireJobRPC(context.Background(), nil) }\n"},
		{"bare go in daemon_unix_metrics.go", "cmd/cascade/daemon_unix_metrics.go", "package main\nfunc startFleetMetricsConsumer() { go func() {}() }\n"},
	}
	for _, s := range seeds {
		b, g := copySites(bg), copySites(gos)
		collectSites(t, s.rel, s.src, b, g)
		got := append(lifecycleViolations("Background", b, backgroundAllow), lifecycleViolations("go", g, goStmtAllow)...)
		if len(got) == 0 {
			t.Errorf("seed %q produced no violation", s.name)
		}
		t.Logf("seed %q -> %v", s.name, got)
	}
	swapped := copySites(bg)
	usage := backgroundAllow[3].lifecycleSite
	delete(swapped, usage)
	swapped[lifecycleSite{usage.file, usage.fn, "e.reg.ListProviders"}] = 1
	if got := lifecycleViolations("Background", swapped, backgroundAllow); len(got) < 2 {
		t.Errorf("a callee swap within an entry produced %v, want an unlisted and a stale violation", got)
	} else {
		t.Logf("seed \"callee swap\" -> %v", got)
	}
	if hits := hookAssignments(t, map[string]string{"seed_hook.go": "package main\nfunc init() { daemonCustodyHook = nil }\n"}); len(hits) != 1 {
		t.Errorf("a seeded non-test hook assignment produced %v, want one hit", hits)
	}
}

// copySites copies a site map.
func copySites(in map[lifecycleSite]int) map[lifecycleSite]int {
	out := make(map[lifecycleSite]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
