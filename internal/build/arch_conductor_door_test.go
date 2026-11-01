// Package build (this file): the one-conductor-registration gate (P1-CPA-10,
// R17 m-3). RegisterConductorExecuteHandler builds the daemon's one Executor
// and binds the in-process conductor door to it. A second production caller
// would build a second Executor and silently rebind the door; a second bind
// call would point the door somewhere RegisterConductorExecuteHandler did not
// choose. Test files are exempt: a test composes as many daemons as it likes.
package build

import (
	"go/ast"
	"sort"
	"strings"
	"testing"
)

const (
	conductorRegisterFile = "cmd/cascade/daemon_unix_conductor.go"
	conductorBindFile     = "internal/daemon/conductor_execute.go"
)

// callSites returns one entry (the file's rel path) per call in srcs whose
// callee is name, bare or as any selector's final element, where keep
// accepts the file.
func callSites(srcs []archSource, name string, keep func(rel string) bool) []string {
	var out []string
	for _, s := range srcs {
		if !keep(s.rel) {
			continue
		}
		ast.Inspect(s.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				if fn.Name == name {
					out = append(out, s.rel)
				}
			case *ast.SelectorExpr:
				if fn.Sel.Name == name {
					out = append(out, s.rel)
				}
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

// conductorRegistrationViolations reports every way srcs break the rule:
// RegisterConductorExecuteHandler called anywhere but exactly once in
// daemon_unix_conductor.go, and bind (only ConductorDoor has one in
// internal/daemon) called anywhere but exactly once in conductor_execute.go.
func conductorRegistrationViolations(srcs []archSource) []string {
	var bad []string
	reg := callSites(srcs, "RegisterConductorExecuteHandler", func(string) bool { return true })
	if len(reg) != 1 || reg[0] != conductorRegisterFile {
		bad = append(bad, "RegisterConductorExecuteHandler callers: "+strings.Join(reg, ","))
	}
	bind := callSites(srcs, "bind", func(rel string) bool { return underDir(rel, "internal/daemon") })
	if len(bind) != 1 || bind[0] != conductorBindFile {
		bad = append(bad, "ConductorDoor.bind callers: "+strings.Join(bind, ","))
	}
	return bad
}

func TestArchOneConductorExecuteRegistration(t *testing.T) {
	if bad := conductorRegistrationViolations(shippingSources(t, archModuleRoot(t))); len(bad) > 0 {
		t.Fatalf("the daemon's conductor must be built and bound exactly once:\n  %s", strings.Join(bad, "\n  "))
	}
}

func TestArchOneConductorExecuteRegistrationDetectsViolation(t *testing.T) {
	good := map[string]string{
		conductorRegisterFile: "package main\nfunc f() { _ = daemon.RegisterConductorExecuteHandler(nil) }\n",
		conductorBindFile:     "package daemon\nfunc g() { ProcessConductorDoor().bind(nil, nil, nil, nil) }\n",
	}
	if bad := conductorRegistrationViolations(seededSources(t, good)); len(bad) != 0 {
		t.Fatalf("the seeded good tree is red: %v", bad)
	}
	extra := map[string]string{
		conductorRegisterFile:                 good[conductorRegisterFile],
		conductorBindFile:                     good[conductorBindFile],
		"cmd/cascade/second.go":               "package main\nfunc h() { _ = daemon.RegisterConductorExecuteHandler(nil) }\n",
		"internal/daemon/rebind_elsewhere.go": "package daemon\nfunc i() { processDoor.bind(nil, nil, nil, nil) }\n",
	}
	if bad := conductorRegistrationViolations(seededSources(t, extra)); len(bad) != 2 {
		t.Fatalf("a second registration and a second bind gave %d violations, want 2: %v", len(bad), bad)
	}
	if bad := conductorRegistrationViolations(seededSources(t, map[string]string{"cmd/cascade/x.go": "package main\n"})); len(bad) != 2 {
		t.Fatalf("a tree with no registration and no bind gave %d violations, want 2: %v", len(bad), bad)
	}
}
