//go:build !windows

// Purpose: self-checks for the child-daemon harness that run in the plain
//
//	suite. The H5 isolation proof fails closed: a child HOME that is not
//	a throwaway under a non-empty TMPDIR, and any default-keychain query
//	outcome other than "no default keychain", refuse the child. The deny
//	wiring (wireDenyConductor, registerDenyConductor) must call exactly
//	what the production wire path calls, so a collaborator added to
//	wireConductorExecute turns this suite red until the deny wiring has it.
//
// Constraints: offline; the probe outcomes come from /bin/sh exit codes
//
//	and a missing binary, never from the real keychain.
//
// SPORT: cmd/cascade/daemon (CHANGE, P1-CORE-15).
package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// keychainNotFound is what `security default-keychain -d user` prints to
// stderr, with exit status 1, when HOME holds no default keychain.
const keychainNotFound = "A default keychain could not be found."

// throwawayHomeReason says why home is not a throwaway HOME, or "" when it
// is: TMPDIR must be a non-empty absolute path and home a directory strictly
// below it, never the passwd home.
func throwawayHomeReason(home, passwdHome, tmpdir string) string {
	if tmpdir == "" || !filepath.IsAbs(tmpdir) {
		return fmt.Sprintf("TMPDIR %q is empty or relative", tmpdir)
	}
	if home == "" || filepath.Clean(home) == filepath.Clean(passwdHome) {
		return fmt.Sprintf("HOME %q is empty or the passwd home", home)
	}
	rel, err := filepath.Rel(filepath.Clean(tmpdir), filepath.Clean(home))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Sprintf("HOME %q is not below TMPDIR %q", home, tmpdir)
	}
	return ""
}

// keychainProbeReason says why the default-keychain query outcome does not
// prove the keychain unreachable, or "" when it does. Only exit status 1
// with empty stdout and the "could not be found" stderr proves it; success,
// a query that never ran, or any other failure refuses (fails closed).
func keychainProbeReason(stdout, stderr string, err error) string {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return "the default-keychain query found " + strconv.Quote(strings.TrimSpace(stdout))
	case !errors.As(err, &exit):
		return "the default-keychain query did not run: " + err.Error()
	case exit.ExitCode() != 1 || strings.TrimSpace(stdout) != "" || !strings.Contains(stderr, keychainNotFound):
		return fmt.Sprintf("the default-keychain query failed otherwise (exit %d, stdout %q, stderr %q)", exit.ExitCode(), strings.TrimSpace(stdout), strings.TrimSpace(stderr))
	}
	return ""
}

func TestResumeChildIsolationProofFailsClosed(t *testing.T) {
	for name, c := range map[string]struct{ home, tmp string }{
		"empty TMPDIR": {"/tmp/x/home", ""}, "relative TMPDIR": {"/tmp/x/home", "tmp"}, "HOME is TMPDIR": {"/tmp/x", "/tmp/x"},
		"HOME outside": {"/tmp/y/home", "/tmp/x"}, "sibling prefix": {"/tmp/xy/home", "/tmp/x"}, "passwd home": {"/Users/me", "/Users"},
	} {
		if throwawayHomeReason(c.home, "/Users/me", c.tmp) == "" {
			t.Errorf("%s: HOME %q under TMPDIR %q accepted as a throwaway", name, c.home, c.tmp)
		}
	}
	if why := throwawayHomeReason("/tmp/x/cascade-cmd-home1", "/Users/me", "/tmp/x"); why != "" {
		t.Errorf("a HOME below TMPDIR refused: %s", why)
	}
	exit := func(code int) error { return exec.Command("/bin/sh", "-c", "exit "+strconv.Itoa(code)).Run() }
	missing := exec.Command(filepath.Join(t.TempDir(), "security")).Run()
	for name, c := range map[string]struct {
		stdout, stderr string
		err            error
	}{
		"found":          {`"/Users/me/Library/Keychains/login.keychain-db"`, "", nil},
		"found, no path": {"", "", nil},
		"missing binary": {"", "", missing},
		"other exit":     {"", keychainNotFound, exit(2)},
		"other stderr":   {"", "security: permission denied", exit(1)},
		"stdout too":     {`"/x"`, keychainNotFound, exit(1)},
	} {
		if keychainProbeReason(c.stdout, c.stderr, c.err) == "" {
			t.Errorf("%s: probe outcome accepted as proof the keychain is unreachable", name)
		}
	}
	if why := keychainProbeReason("", "security: SecKeychainCopyDomainDefault user: "+keychainNotFound+"\n", exit(1)); why != "" {
		t.Errorf("the not-found outcome refused: %s", why)
	}
}

func TestDenyConductorMirrorsProductionWiring(t *testing.T) {
	prod := wiringRefs(t, "daemon_unix_conductor.go", "wireConductorAndReachability", "wireConductorExecute")
	deny := wiringRefs(t, "daemon_resume_child_test.go", "wireDenyConductor", "registerDenyConductor")
	// The only allowed differences: the production split into its own
	// helper, and the deny side's own finisher and tunnel lookup (production
	// takes the lookup as a parameter).
	delete(prod, "wireConductorExecute")
	delete(deny, "registerDenyConductor")
	delete(deny, "nodeTunnelLookup")
	var onlyProd, onlyDeny []string
	for name := range prod {
		if !deny[name] {
			onlyProd = append(onlyProd, name)
		}
	}
	for name := range deny {
		if !prod[name] {
			onlyDeny = append(onlyDeny, name)
		}
	}
	sort.Strings(onlyProd)
	sort.Strings(onlyDeny)
	if len(onlyProd)+len(onlyDeny) != 0 {
		t.Fatalf("deny wiring drifted from wireConductorExecute: production only %v, deny only %v", onlyProd, onlyDeny)
	}
}

// wiringRefs returns every call and composite literal inside funcs of
// file: "pkg.Name" for a package-qualified name, "Name" for a package-level
// one, ".Name" for a method (the receiver spelling differs between sides).
func wiringRefs(t *testing.T, file string, funcs ...string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	pkgs := map[string]bool{}
	for _, spec := range f.Imports {
		p, _ := strconv.Unquote(spec.Path.Value)
		name := path.Base(p)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		pkgs[name] = true
	}
	want, out := map[string]bool{}, map[string]bool{}
	for _, name := range funcs {
		want[name] = true
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && want[fd.Name.Name] {
			delete(want, fd.Name.Name)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					out[refName(x.Fun, pkgs)] = true
				case *ast.CompositeLit:
					out["{}"+refName(x.Type, pkgs)] = true
				}
				return true
			})
		}
	}
	if len(want) != 0 {
		t.Fatalf("%s: functions %v not found", file, want)
	}
	return out
}

// refName spells e for wiringRefs.
func refName(e ast.Expr, pkgs map[string]bool) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && pkgs[id.Name] {
			return id.Name + "." + x.Sel.Name
		}
		return "." + x.Sel.Name
	}
	return fmt.Sprintf("%T", e)
}
