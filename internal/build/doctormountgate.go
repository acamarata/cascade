// Purpose: the doctor MOUNT gate. A constructor that returns a
//
//	doctor.Check and is never registered in the CLI's
//	productionCheckRegistry ships as a diagnostic nobody can run. Five of
//	them accumulated that way before this gate existed, each behind an
//	honest deferral, and the shipped `cascade doctor` reported almost
//	nothing as a result. The test-only gate could not see it: those
//	constructors HAVE callers, in tests, which is exactly the usage that
//	proves nothing about what a user gets.
//
// Inputs: the module root. It reads internal/doctor and every other
//
//	package for functions returning a Check, and reads the body of
//	productionCheckRegistry out of the CLI's doctor command file.
//
// Outputs: the constructors with no production registration, minus the
//
//	exemptions declared below.
//
// Constraints: fail closed. A tree it cannot parse, a missing command
//
//	file and a missing registry function are all errors, never an empty
//	"nothing unmounted" result: a gate that reports clean because it
//	could not look is worse than no gate.
//
// SPORT: DOCTOR_MOUNT_GATE: ADD (internal/build doctor check registration gate).

package build

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// doctorRegistryFile is the composition root this gate reads. It is named
// once, here, so a move of the mounting point is a deliberate edit rather
// than a gate that quietly stops finding anything.
var doctorRegistryFile = filepath.Join("cmd", "cascade", "doctor_mounts.go")

// doctorRegistryFunc is the function whose body must name every mounted
// constructor.
const doctorRegistryFunc = "productionCheckRegistry"

// DoctorCheckConstructor is one function that returns a doctor.Check.
type DoctorCheckConstructor struct {
	// Name is the function's identifier, e.g. NewSubsystemCensusCheck.
	Name string
	// Dir is the package directory relative to the module root.
	Dir string
}

// String renders "dir.Name" for a gate failure message.
func (c DoctorCheckConstructor) String() string { return c.Dir + "." + c.Name }

// DoctorMountExemptions names the constructors allowed to stay unmounted,
// each with the reason and the caller that retires it. Same discipline as
// internal/build/testonly-allow.json: a reason, and the named place the
// mount lands when its precondition arrives.
//
// An exemption is DELETED when its check mounts, never repointed at a new
// destination. A stale exemption hides the next one.
var DoctorMountExemptions = map[string]string{
	"NewMCPIntegrationCheck": "takes a HarnessDiscoverer; no production implementation of that interface exists " +
		"in the tree, and satisfying it with a hand-written stand-in would put a check in the report that probes " +
		"nothing. Mounts in productionCheckRegistry in the SAME change that lands a real HarnessDiscoverer.",
	"NewSubsystemCensusCheck": "takes a SubsystemStateProvider; nothing in the tree implements " +
		"DeclaredSubsystems/RunningSubsystems, so there is no live state to compare a manifest against. Mounts in " +
		"productionCheckRegistry in the SAME change that lands a real SubsystemStateProvider.",
	"NewAttentionCheck": "takes a *supervision.Store; the daemon composition root that constructs a production " +
		"Store (internal/build/testonly-allow.json's internal/fleet/supervision.NewStore entry, P1-E18-W4-S39-T1) " +
		"does not exist yet, so there is no live store to check. Mounts in productionCheckRegistry in the SAME " +
		"change that wires the daemon composition root's production Store.",
}

// ScanDoctorCheckConstructors returns every function in the tree that
// returns a doctor.Check, keyed by the package directory it lives in.
// Test files are skipped: a constructor a test calls is still unmounted.
func ScanDoctorCheckConstructors(root string) ([]DoctorCheckConstructor, error) {
	var out []DoctorCheckConstructor
	for _, top := range []string{"internal", "cmd", "pkg", "providers", "plugins"} {
		dir := filepath.Join(root, top)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		found, err := scanCheckConstructorsIn(root, dir)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return out, nil
}

// scanCheckConstructorsIn walks one top-level tree.
func scanCheckConstructorsIn(root, tree string) ([]DoctorCheckConstructor, error) {
	var out []DoctorCheckConstructor
	err := filepath.WalkDir(tree, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parsing %s: %w", path, perr)
		}
		rel, rerr := filepath.Rel(root, filepath.Dir(path))
		if rerr != nil {
			return rerr
		}
		for _, name := range checkConstructorNames(file) {
			out = append(out, DoctorCheckConstructor{Name: name, Dir: rel})
		}
		return nil
	})
	return out, err
}

// checkConstructorNames returns the exported, non-method functions in file
// whose results include a doctor.Check.
func checkConstructorNames(file *ast.File) []string {
	var names []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Type.Results == nil || !fn.Name.IsExported() {
			continue
		}
		for _, result := range fn.Type.Results.List {
			if resultIsDoctorCheck(result.Type) {
				names = append(names, fn.Name.Name)
				break
			}
		}
	}
	return names
}

// resultIsDoctorCheck reports whether a result type is doctor.Check, in
// either the in-package form (Check) or the qualified form (doctor.Check).
func resultIsDoctorCheck(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "Check"
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		return ok && pkg.Name == "doctor" && t.Sel.Name == "Check"
	default:
		return false
	}
}

// ReadDoctorRegistryBody returns the source text of productionCheckRegistry.
// A file or function it cannot find is an error: a gate that reported
// "nothing unmounted" because the mounting point moved would be the exact
// silence it exists to break.
func ReadDoctorRegistryBody(root string) (string, error) {
	path := filepath.Join(root, doctorRegistryFile)
	src, err := os.ReadFile(path) //nolint:gosec // fixed repo-relative path
	if err != nil {
		return "", fmt.Errorf("reading the doctor composition root %s: %w", doctorRegistryFile, err)
	}
	fset := token.NewFileSet()
	file, perr := parser.ParseFile(fset, path, src, 0)
	if perr != nil {
		return "", fmt.Errorf("parsing %s: %w", doctorRegistryFile, perr)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != doctorRegistryFunc || fn.Body == nil {
			continue
		}
		start := fset.Position(fn.Body.Pos()).Offset
		end := fset.Position(fn.Body.End()).Offset
		return string(src[start:end]), nil
	}
	return "", fmt.Errorf("%s declares no %s: the doctor mounting point moved", doctorRegistryFile, doctorRegistryFunc)
}

// UnmountedDoctorChecks returns the constructors that neither appear in
// the registry body nor carry an exemption.
func UnmountedDoctorChecks(constructors []DoctorCheckConstructor, body string, exempt map[string]string) []DoctorCheckConstructor {
	var out []DoctorCheckConstructor
	for _, c := range constructors {
		if _, ok := exempt[c.Name]; ok {
			continue
		}
		if strings.Contains(body, c.Name+"(") {
			continue
		}
		out = append(out, c)
	}
	return out
}

// StaleDoctorMountExemptions returns exemptions naming a constructor that
// no longer exists, or one that IS now mounted. Both are stale, and a
// stale exemption is how the next unmounted check hides.
func StaleDoctorMountExemptions(constructors []DoctorCheckConstructor, body string, exempt map[string]string) []string {
	known := map[string]bool{}
	for _, c := range constructors {
		known[c.Name] = true
	}
	var stale []string
	for name := range exempt {
		if !known[name] {
			stale = append(stale, name+" (no such constructor)")
			continue
		}
		if strings.Contains(body, name+"(") {
			stale = append(stale, name+" (now mounted; delete the exemption)")
		}
	}
	return stale
}
