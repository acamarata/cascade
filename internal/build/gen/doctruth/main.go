// Command doctruth is the CLI front end for the public-doc truth gate
// (P1-DOC-07): `check` prints every finding, `baseline` prunes the
// committed ratchet, `baseline --init` creates it once, and `guard`
// compares the baseline across a git ref for CI's own ratchet check. See
// docs/developer/docs-as-tests.md for the full contract.
//
// Run any subcommand via `go run ./internal/build/gen/doctruth <cmd>` from
// anywhere inside the repo.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/output"
)

func main() {
	root, err := repoRoot()
	if err != nil {
		output.NewDefault(false, false, false, false).Fail(err)
		os.Exit(2)
	}
	os.Exit(run(os.Args[1:], output.NewDefault(false, false, false, false), root))
}

// run executes one doctruth invocation (args excludes the program name),
// writing through w, against the checkout at root. It returns the process
// exit code: 0 clean, 1 for new findings or guard additions, 2 for usage or
// runtime errors.
func run(args []string, w *output.Writer, root string) int {
	if len(args) < 1 {
		w.Fail(fmt.Errorf("usage: doctruth <check|baseline|guard> [args...]"))
		return 2
	}
	code, err := dispatch(w, root, args[0], args[1:])
	if err != nil {
		w.Fail(err)
		if code == 0 {
			code = 2
		}
	}
	return code
}

func dispatch(w *output.Writer, root, cmd string, args []string) (int, error) {
	switch cmd {
	case "check":
		return runCheck(w, root, args)
	case "baseline":
		return runBaseline(w, root, args)
	case "guard":
		return runGuard(w, root, args)
	default:
		return 2, fmt.Errorf("doctruth: unknown subcommand %q", cmd)
	}
}

func runCheck(w *output.Writer, root string, args []string) (int, error) {
	mode := build.DocTruthCI
	var paths []string
	for _, a := range args {
		if a == "--release" {
			mode = build.DocTruthRelease
			continue
		}
		paths = append(paths, a)
	}
	rep, err := build.CheckDocTruth(root, mode, paths...)
	if err != nil {
		return 2, err
	}
	printFindings(w, rep)
	w.Println(fmt.Sprintf("checked %d files, %d links, %d refs, %d directives, %d findings",
		rep.Files, rep.Links, rep.Refs, rep.Directives, len(rep.New)))
	if len(rep.New) > 0 {
		return 1, nil
	}
	return 0, nil
}

// printFindings prints every finding sorted by File:Line, suffixing each
// one that only passed because it is baselined (present in Findings but
// absent from New) with " (baselined)".
func printFindings(w *output.Writer, rep build.DocTruthReport) {
	newKeys := map[string]bool{}
	for _, f := range rep.New {
		newKeys[f.Key] = true
	}
	all := append([]build.DocFinding(nil), rep.Findings...)
	sort.Slice(all, func(i, j int) bool {
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		if all[i].Line != all[j].Line {
			return all[i].Line < all[j].Line
		}
		return all[i].Key < all[j].Key
	})
	for _, f := range all {
		line := fmt.Sprintf("%s:%d: %s: %s", f.File, f.Line, f.Rule, f.Detail)
		if !newKeys[f.Key] {
			line += " (baselined)"
		}
		w.Println(line)
	}
}

func runBaseline(w *output.Writer, root string, args []string) (int, error) {
	if len(args) == 1 && args[0] == "--init" {
		n, err := build.InitDocTruthBaseline(root)
		if err != nil {
			return 2, err
		}
		w.Println(fmt.Sprintf("baseline initialized: %d findings", n))
		return 0, nil
	}
	if len(args) != 0 {
		return 2, fmt.Errorf("doctruth: baseline takes no arguments except --init")
	}
	kept, dropped, err := build.PruneDocTruthBaseline(root)
	if err != nil {
		return 2, err
	}
	w.Println(fmt.Sprintf("baseline pruned: %d kept, %d dropped", kept, dropped))
	return 0, nil
}

func runGuard(w *output.Writer, root string, args []string) (int, error) {
	if len(args) != 1 {
		return 2, fmt.Errorf("doctruth: guard requires exactly one git ref")
	}
	added, err := build.GuardDocTruthBaseline(root, args[0])
	if err != nil {
		return 2, err
	}
	if len(added) == 0 {
		w.Println("guard: no keys added")
		return 0, nil
	}
	for _, k := range added {
		w.Println("added: " + k)
	}
	return 1, nil
}

// repoRoot resolves the checkout root by walking up from the working
// directory to the nearest go.mod, so this generator never needs os/exec
// (and so never needs an internal/build/egress_allow.go entry, which sits
// outside this ticket's write_scope).
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("doctruth: os.Getwd: %w", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("doctruth: no go.mod found above %s", wd)
		}
		dir = parent
	}
}
