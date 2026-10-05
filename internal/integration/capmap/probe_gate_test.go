//go:build capmap

package capmap

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	register("TestCapmap_QualityGatesCiRun", probeCIRun)
}

// gitRepo makes a one-commit Go module repository under the probe's home.
func gitRepo(t *testing.T, c *cli, name, mainSrc string) string {
	t.Helper()
	dir := filepath.Join(c.home, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"go.mod": "module probe\n\ngo 1.26\n", "main.go": mainSrc}
	for f, body := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "."}, {"add", "-A"}, {"-c", "user.email=probe@example.invalid", "-c", "user.name=probe", "commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, c.env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// ciRuns returns repository name to conclusion from `ci status --json`.
func (c *cli) ciRuns() map[string]string {
	c.t.Helper()
	e := decodeEnvelope(c.t, c.mustOK("", "ci", "status", "--json"))
	var d struct {
		Runs []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil || !e.OK {
		c.t.Fatalf("ci status: ok=%v err=%v", e.OK, err)
	}
	runs := map[string]string{}
	for _, r := range d.Runs {
		runs[r.Name] = r.Conclusion
	}
	return runs
}

// probeCIRun proves the local gate runs on a real checkout and records the
// result. Authorization: a tree that does not build is refused (exit 4, a
// FAILED receipt) and a directory with no repository is refused with the
// typed invalid-input error. Routing: the gate ran its build step in the
// named checkout. Side effect: a separate `ci status` process reads one
// success and one failure from the store, keyed by repository. Result:
// the passing run prints PASSED and exits zero.
func probeCIRun(t *testing.T) {
	c := newCLI(t)
	good := gitRepo(t, c, "okrepo", "package main\n\nfunc main() {}\n")
	bad := gitRepo(t, c, "badrepo", "package main\n\nfunc main() { undefinedCall() }\n")
	r := c.runIn(good, "", "ci", "run", "--build-only")
	if r.code != 0 || !strings.Contains(r.stdout, "build  pass") || !strings.Contains(r.stdout, "PASSED") {
		t.Fatalf("good repo: exit %d stdout %q stderr %q, want a passing build", r.code, r.stdout, r.stderr)
	}
	r = c.runIn(bad, "", "ci", "run", "--build-only")
	if r.code != 4 || !strings.Contains(r.stdout, "FAILED at build") {
		t.Fatalf("bad repo: exit %d stdout %q, want exit 4 and FAILED at build", r.code, r.stdout)
	}
	norepo := filepath.Join(c.home, "norepo")
	if err := os.MkdirAll(norepo, 0o750); err != nil {
		t.Fatal(err)
	}
	r = c.runIn(norepo, "", "ci", "run", "--build-only", "--json")
	if e := decodeEnvelope(t, r); e.OK || e.Error == nil || e.Error.Kind != "invalid-input" {
		t.Fatalf("no repository: %+v exit %d, want a typed invalid-input refusal", e, r.code)
	}
	runs := c.ciRuns()
	if runs["okrepo"] != "success" || runs["badrepo"] != "failure" || len(runs) != 2 {
		t.Fatalf("stored runs = %v, want okrepo success and badrepo failure only", runs)
	}
}
