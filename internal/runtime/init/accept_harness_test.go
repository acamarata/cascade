//go:build integration

package init_test

// Purpose: the fixture every `cascade init` acceptance scenario shares
//   (P1-E16-W4-S35-T5) — a real cascade binary, a clean HOME with one
//   harness installed in it, and a project with instructions to generate
//   from.
// Inputs: the tree this test is compiled from; nothing about the machine
//   running it.
// Outputs: an env the scenarios run the binary under, and the paths they
//   assert on.
// Constraints: Art.1 — no stub stands in for the thing under test. The
//   binary is built from cmd/cascade and run as a child process, the
//   harness is a real directory tree, and every assertion reads a file
//   the binary wrote. The one sanctioned double is the recorded-fixture
//   provider endpoint in accept_provider_test.go.
// Constraints: Art.7.1 — HOME, the config root and the project are all
//   temp directories, so nothing here reads or writes the developer's own
//   harness installation.
// SPORT: internal/runtime/init acceptance (ADD) — P1-E16-W4-S35-T5.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// env is one scenario's whole world: a built binary, a HOME with a
// harness in it, and a project to run from.
type env struct {
	bin     string
	home    string
	project string
	// cascadeHome is where the journal, config and database live.
	cascadeHome string
}

// buildOnce builds cmd/cascade once per test binary. Every scenario runs
// the SAME artifact, which is the point of an acceptance suite: a
// scenario that passed against its own rebuild would not have proven the
// scenarios agree about one program.
var buildOnce struct {
	sync.Once
	path string
	err  error
}

// buildCascade returns the path to a freshly built cascade binary.
func buildCascade(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cascade-accept-bin")
		if err != nil {
			buildOnce.err = err
			return
		}
		out := filepath.Join(dir, "cascade"+exeSuffix())
		cmd := exec.Command("go", "build", "-o", out, "github.com/acamarata/cascade/cmd/cascade")
		cmd.Dir = repoRoot()
		if combined, buildErr := cmd.CombinedOutput(); buildErr != nil {
			buildOnce.err = &buildFailure{err: buildErr, output: string(combined)}
			return
		}
		buildOnce.path = out
	})
	if buildOnce.err != nil {
		t.Fatalf("building cmd/cascade: %v", buildOnce.err)
	}
	return buildOnce.path
}

// buildFailure carries the compiler's own output, because "exit status 1"
// on its own sends whoever reads it to the wrong place.
type buildFailure struct {
	err    error
	output string
}

func (b *buildFailure) Error() string { return b.err.Error() + "\n" + b.output }

// newEnv builds the binary and stages a machine with one harness
// installed and a project carrying instructions.
//
// The harness directory is SEEDED rather than left empty. "A developer
// with no prior cascade configuration" still has the harness installed —
// that is what makes the story a story — and on a machine with no harness
// at all the honest outcome is that setup wires nothing, which is a
// different scenario and is asserted separately.
func newEnv(t *testing.T) env {
	t.Helper()
	bin := buildCascade(t)
	home := t.TempDir()
	project := t.TempDir()

	mustMkdirAll(t, filepath.Join(home, ".claude"))
	mustWrite(t, filepath.Join(home, ".claude.json"), "{}\n")
	mustMkdirAll(t, filepath.Join(project, ".cascade"))
	mustWrite(t, filepath.Join(project, ".cascade", "CASCADE.md"),
		"# Project instructions\n\nRun the tests before pushing.\n")

	return env{bin: bin, home: home, project: project, cascadeHome: filepath.Join(home, ".cascade")}
}

// run invokes the binary with args and returns stdout+stderr and the exit
// code. A non-zero code is returned, never fatal: several scenarios are
// about which non-zero code came back.
func (e env) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Dir = e.project
	cmd.Env = e.environ()
	out, err := cmd.CombinedOutput()
	return string(out), exitCodeOf(t, err)
}

// environ is the child's whole environment: nothing inherited from the
// developer's shell, so a scenario cannot pass because of something
// already on this machine.
func (e env) environ() []string {
	return append([]string{
		"HOME=" + e.home,
		"USERPROFILE=" + e.home,
		"CASCADE_HOME=" + e.cascadeHome,
		"XDG_CONFIG_HOME=" + filepath.Join(e.home, ".config"),
		"PATH=" + filepath.Dir(e.bin) + string(os.PathListSeparator) + os.Getenv("PATH"),
	}, passthroughEnv()...)
}

// instructionFile is where the harness instruction file lands for this
// project.
func (e env) instructionFile() string {
	return filepath.Join(e.project, ".claude", "CLAUDE.md")
}

// hookPack is the hook-pack artifact this plugin writes.
func (e env) hookPack() string {
	return filepath.Join(e.home, ".claude", "hooks", "cascade-claude.json")
}

// userConfig is the harness's user-scope config, which carries the MCP
// server table. It sits BESIDE home, not inside the config directory,
// whenever the config-dir override is unset — which it is here.
func (e env) userConfig() string { return filepath.Join(e.home, ".claude.json") }

// journal is the init resume journal.
func (e env) journal() string { return filepath.Join(e.cascadeHome, "init-state.json") }

// harnessRow reads one row out of `context harness list --json`.
func (e env) harnessRow(t *testing.T, kind string) harnessState {
	t.Helper()
	out, code := e.run(t, "context", "harness", "list", "--json")
	if code != 0 {
		t.Fatalf("context harness list exited %d:\n%s", code, out)
	}
	var env struct {
		Data struct {
			Harnesses []harnessState `json:"harnesses"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(jsonTail(out)), &env); err != nil {
		t.Fatalf("decoding harness list: %v\n%s", err, out)
	}
	for _, h := range env.Data.Harnesses {
		if h.Kind == kind {
			return h
		}
	}
	t.Fatalf("no row for harness %q in:\n%s", kind, out)
	return harnessState{}
}

// harnessState is the subset of the row these scenarios assert on.
type harnessState struct {
	Kind              string `json:"kind"`
	Detected          bool   `json:"detected"`
	CascadeRegistered bool   `json:"cascade_registered"`
}

// mcpServerNames lists the MCP servers registered in the user config.
func (e env) mcpServerNames(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(e.userConfig())
	if err != nil {
		t.Fatalf("reading the harness user config: %v", err)
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding the harness user config: %v\n%s", err, raw)
	}
	names := make([]string, 0, len(doc.MCPServers))
	for name := range doc.MCPServers {
		names = append(names, name)
	}
	return names
}

// jsonTail returns out from its first "{" on, so a warning printed before
// the envelope does not break the decode. The warning itself is real
// output and is left in the string the failure messages print.
func jsonTail(out string) string {
	if i := strings.Index(out, "{"); i >= 0 {
		return out[i:]
	}
	return out
}

// runEnv is run with an explicit environment, for a scenario that needs a
// variable the standard one does not carry.
func (e env) runEnv(t *testing.T, environ []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Dir = e.project
	cmd.Env = environ
	out, err := cmd.CombinedOutput()
	return string(out), exitCodeOf(t, err)
}
