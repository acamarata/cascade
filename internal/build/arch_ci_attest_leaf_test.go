// Package build (this file): the CI attestation leaf gate. internal/ci/attest
// must import neither internal/ci nor internal/jobs (directly or through any
// other package), so the jobs completion gate and the ci command tree can
// both import it; internal/nodes must reach neither internal/jobs nor
// internal/ci; internal/jobs must not reach internal/ci itself (the attest
// leaf is allowed).
//
// The rule is RED against seeded graphs and GREEN on the real tree; the real
// tree is cross-checked with `go list -deps` when the go tool is on PATH.
package build

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const attestLeafPath = cascadeModulePath + "/internal/ci/attest"

// leafRule forbids pkg from reaching any package forbidden reports true for.
type leafRule struct {
	pkg       string
	forbidden func(dep string) bool
}

var attestLeafRules = []leafRule{
	{attestLeafPath, func(d string) bool {
		return d != attestLeafPath && (archDirHasPrefix(d, cascadeModulePath+"/internal/ci") ||
			archDirHasPrefix(d, cascadeModulePath+"/internal/jobs"))
	}},
	{cascadeModulePath + "/internal/nodes", func(d string) bool {
		return d == cascadeModulePath+"/internal/jobs" || d == cascadeModulePath+"/internal/ci"
	}},
	{cascadeModulePath + "/internal/jobs", func(d string) bool { return d == cascadeModulePath+"/internal/ci" }},
}

// transitiveDeps returns every package reachable from pkg in g.
func transitiveDeps(g map[string][]string, pkg string) []string {
	seen := map[string]bool{}
	stack := append([]string(nil), g[pkg]...)
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[p] {
			continue
		}
		seen[p] = true
		stack = append(stack, g[p]...)
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// leafViolations returns "<pkg> reaches <dep>" for every broken rule.
func leafViolations(g map[string][]string) []string {
	var bad []string
	for _, r := range attestLeafRules {
		for _, d := range transitiveDeps(g, r.pkg) {
			if r.forbidden(d) {
				bad = append(bad, strings.TrimPrefix(r.pkg, cascadeModulePath+"/")+" reaches "+d)
			}
		}
	}
	return bad
}

func TestCIAttestIsLeaf(t *testing.T) {
	g := archBuildGraph(archScan(t, archModuleRoot(t), cascadeModulePath), cascadeModulePath)
	if _, ok := g[attestLeafPath]; !ok {
		t.Fatalf("%s not found in the scanned tree; the gate would be vacuous", attestLeafPath)
	}
	if bad := leafViolations(g); len(bad) > 0 {
		t.Fatalf("attestation leaf rules broken:\n  %s", strings.Join(bad, "\n  "))
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Log("go tool not on PATH: go list cross-check skipped")
		return
	}
	cmd := exec.Command("go", "list", "-deps", "./internal/ci/attest/")
	cmd.Dir = archModuleRoot(t)
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		t.Fatalf("go list -deps ./internal/ci/attest/: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if attestLeafRules[0].forbidden(dep) {
			t.Errorf("go list: internal/ci/attest depends on %s", dep)
		}
	}
}

func TestCIAttestIsLeafDetectsViolation(t *testing.T) {
	m := cascadeModulePath + "/"
	cases := map[string]map[string][]string{
		"direct ci":       {attestLeafPath: {m + "internal/ci"}},
		"direct jobs sub": {attestLeafPath: {m + "internal/jobs/sub"}},
		"transitive jobs": {attestLeafPath: {m + "internal/nodes"}, m + "internal/nodes": {m + "internal/x"}, m + "internal/x": {m + "internal/jobs"}},
		"nodes to ci":     {attestLeafPath: nil, m + "internal/nodes": {m + "internal/ci"}},
		"jobs to ci":      {attestLeafPath: nil, m + "internal/jobs": {m + "internal/y"}, m + "internal/y": {m + "internal/ci"}},
	}
	for name, g := range cases {
		if bad := leafViolations(g); len(bad) == 0 {
			t.Errorf("%s: seeded violation not detected", name)
		}
	}
	allowed := map[string][]string{
		attestLeafPath: {m + "internal/nodes", m + "pkg/cascade"}, m + "internal/jobs": {attestLeafPath},
		m + "internal/ci": {attestLeafPath, m + "internal/jobs"},
	}
	if bad := leafViolations(allowed); len(bad) != 0 {
		t.Fatalf("allowed graph flagged: %v", bad)
	}
}
