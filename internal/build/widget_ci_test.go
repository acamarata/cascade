// Purpose: shape and behaviour gates for the macOS widget CI lane in ci.yml (the
// `changes` and `widget` jobs, wired into ci-gate). Each check maps the parsed
// workflow to a problem list: the real ci.yml yields none, each seeded file under
// testdata/seeded-violations/widget-ci/ yields the problem it was built for. The
// ci-gate script is executed with bash. No network, no Xcode.
package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type widgetNeeds []string

func (n *widgetNeeds) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		*n = widgetNeeds{v.Value}
		return nil
	}
	var l []string
	if err := v.Decode(&l); err != nil {
		return err
	}
	*n = l
	return nil
}

type widgetStep struct {
	ID   string            `yaml:"id"`
	Uses string            `yaml:"uses"`
	If   string            `yaml:"if"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
	Env  map[string]string `yaml:"env"`
}

type widgetJob struct {
	Needs   widgetNeeds       `yaml:"needs"`
	RunsOn  string            `yaml:"runs-on"`
	If      string            `yaml:"if"`
	Outputs map[string]string `yaml:"outputs"`
	Env     map[string]string `yaml:"env"`
	Steps   []widgetStep      `yaml:"steps"`
}

func widgetLoad(t *testing.T, path string) map[string]widgetJob {
	t.Helper()
	var doc struct {
		Jobs map[string]widgetJob `yaml:"jobs"`
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		err = yaml.Unmarshal(raw, &doc)
	}
	if err != nil {
		t.Fatalf("widget ci gate: %s: %v", path, err)
	}
	return doc.Jobs
}

func widgetReal(t *testing.T) map[string]widgetJob {
	return widgetLoad(t, filepath.Join(moduleRoot(t), ".github", "workflows", "ci.yml"))
}

func widgetSeed(t *testing.T, name string) map[string]widgetJob {
	return widgetLoad(t, filepath.Join(moduleRoot(t), "internal", "build", "testdata", "seeded-violations", "widget-ci", name))
}

func widgetScript(j widgetJob) (script string) {
	for _, s := range j.Steps {
		script += s.Run + "\n"
	}
	return script
}

func widgetExpectProblem(t *testing.T, label string, problems []string, want string) {
	t.Helper()
	for _, p := range problems {
		if strings.Contains(p, want) {
			return
		}
	}
	t.Errorf("%s: gate did not flag %q; problems: %v", label, want, problems)
}

func widgetRequireNone(t *testing.T, problems []string) {
	t.Helper()
	if len(problems) != 0 {
		t.Errorf("real ci.yml: %s", strings.Join(problems, "; "))
	}
}

func widgetBash(t *testing.T, dir, script string, env []string) (string, int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil || runtime.GOOS == "windows" {
		t.Skipf("widget ci gate: needs bash on a unix leg (%v)", err)
	}
	cmd := exec.Command(bash, "-c", script)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	if err != nil {
		t.Fatalf("widget ci gate: running bash: %v", err)
	}
	return string(out), 0
}

var widgetTestStepRE = regexp.MustCompile(`-scheme (CascadeMenuBarTests|CascadeWidgetExtensionTests)`)
var widgetGateCore = []string{"build-test", "race", "lint", "hygiene", "coverage-gate"}

func widgetLaneProblems(jobs map[string]widgetJob) []string {
	var p []string
	w, ok := jobs["widget"]
	if !ok {
		return append([]string{"no widget job"}, widgetGateWiringProblems(jobs["ci-gate"], jobs["changes"])...)
	}
	if w.RunsOn != "macos-15" {
		p = append(p, "widget runs-on is "+w.RunsOn+", want macos-15")
	}
	if len(w.Needs) != 1 || w.Needs[0] != "changes" {
		p = append(p, fmt.Sprintf("widget needs %v, want [changes]", []string(w.Needs)))
	}
	if w.If != "needs.changes.outputs.widget == 'true'" {
		p = append(p, "widget if is "+w.If)
	}
	all, uploaded := widgetScript(w), false
	for _, s := range w.Steps {
		if strings.Contains(s.Uses, "upload-artifact") {
			uploaded = s.If == "failure()"
		}
		if widgetTestStepRE.MatchString(s.Run) && !strings.Contains(s.Run, "-enableThreadSanitizer YES") {
			p = append(p, "an XCTest scheme step lacks -enableThreadSanitizer YES")
		}
	}
	if !uploaded {
		p = append(p, "no xcresult upload step with if: failure()")
	}
	for _, want := range []string{"-scheme CascadeMenuBar ", "-scheme CascadeMenuBarTests", "-scheme CascadeWidgetExtensionTests",
		"xcodegen generate", "shasum -a 256 -c", "xcode-select -s", "xcresulttool", "jq -e '.passedTests > 0 and .failedTests == 0'"} {
		if !strings.Contains(all, want) {
			p = append(p, "widget steps lack "+want)
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(w.Env["XCODEGEN_SHA256"]) || w.Env["XCODE_DEVELOPER_DIR"] == "" {
		p = append(p, "widget env lacks XCODEGEN_SHA256 or XCODE_DEVELOPER_DIR")
	}
	return append(p, widgetGateWiringProblems(jobs["ci-gate"], jobs["changes"])...)
}

func widgetGateWiringProblems(gate, changes widgetJob) []string {
	var p []string
	for _, n := range append([]string{"changes", "widget"}, widgetGateCore...) {
		if !slices.Contains(gate.Needs, n) {
			p = append(p, "ci-gate needs does not include "+n)
		}
	}
	for _, ref := range []string{"needs.changes.result", "needs.widget.result", "needs.changes.outputs.widget"} {
		if !strings.Contains(widgetScript(gate), ref) {
			p = append(p, "ci-gate script never reads "+ref)
		}
	}
	if changes.Outputs["widget"] == "" {
		p = append(p, "changes job has no widget output")
	}
	return p
}

// TestWidgetJobInRequiredStatus: the widget job has the contracted shape and
// ci-gate requires both it and the changes job.
func TestWidgetJobInRequiredStatus(t *testing.T) {
	widgetRequireNone(t, widgetLaneProblems(widgetReal(t)))
	widgetExpectProblem(t, "ci-missing-widget-status.yml", widgetLaneProblems(widgetSeed(t, "ci-missing-widget-status.yml")), "ci-gate needs does not include widget")
	widgetExpectProblem(t, "ci-changes-not-required.yml", widgetLaneProblems(widgetSeed(t, "ci-changes-not-required.yml")), "ci-gate needs does not include changes")
}

var widgetGateScenarios = []struct {
	name    string
	results map[string]string
	wanted  string
	pass    bool
}{
	{"all green, widget ran", nil, "true", true},
	{"widget skipped, nothing widget-relevant changed", map[string]string{"widget": "skipped"}, "false", true},
	{"widget skipped although widget changed", map[string]string{"widget": "skipped"}, "true", false},
	{"widget skipped, changes output empty", map[string]string{"widget": "skipped"}, "", false},
	{"widget failed", map[string]string{"widget": "failure"}, "true", false},
	{"widget cancelled", map[string]string{"widget": "cancelled"}, "false", false},
	{"changes failed", map[string]string{"changes": "failure", "widget": "skipped"}, "false", false},
	{"changes skipped", map[string]string{"changes": "skipped", "widget": "skipped"}, "false", false},
	{"an older lane failed", map[string]string{"race": "failure"}, "true", false},
}

var widgetExprRE = regexp.MustCompile(`\$\{\{\s*needs\.([\w-]+)\.(result|outputs\.widget)\s*\}\}`)

// widgetGateRuns executes the ci-gate scripts with the given needs results.
func widgetGateRuns(t *testing.T, jobs map[string]widgetJob, results map[string]string, wanted string) bool {
	t.Helper()
	body := widgetExprRE.ReplaceAllStringFunc(widgetScript(jobs["ci-gate"]), func(m string) string {
		sub := widgetExprRE.FindStringSubmatch(m)
		if sub[2] != "result" {
			return wanted
		}
		if v, ok := results[sub[1]]; ok {
			return v
		}
		return "success"
	})
	if strings.Contains(body, "${{") {
		t.Fatalf("widget ci gate: unsubstituted expression in ci-gate script")
	}
	_, code := widgetBash(t, t.TempDir(), body, nil)
	return code == 0
}

// widgetGateProblems runs every scenario against the ci-gate scripts of jobs.
func widgetGateProblems(t *testing.T, jobs map[string]widgetJob) []string {
	var p []string
	for _, sc := range widgetGateScenarios {
		if widgetGateRuns(t, jobs, sc.results, sc.wanted) != sc.pass {
			p = append(p, fmt.Sprintf("ci-gate wrong verdict (want pass=%v) for: %s", sc.pass, sc.name))
		}
	}
	return p
}

// TestWidgetSkipOnlyWithoutChanges: a skipped widget lane passes ci-gate only when
// changes ran and reported widget=false; every other deviation is red.
func TestWidgetSkipOnlyWithoutChanges(t *testing.T) {
	widgetRequireNone(t, widgetGateProblems(t, widgetReal(t)))
	seeded := widgetGateProblems(t, widgetSeed(t, "ci-skipped-with-changes.yml"))
	widgetExpectProblem(t, "ci-skipped-with-changes.yml", seeded, "widget skipped although widget changed")
	widgetExpectProblem(t, "ci-skipped-with-changes.yml", seeded, "widget skipped, changes output empty")
}

var widgetUsesRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}$`)

// widgetPinProblems: every action in the lane is pinned to a full commit SHA and
// no step installs the toolchain from Homebrew.
func widgetPinProblems(jobs map[string]widgetJob) []string {
	var p []string
	for _, name := range []string{"changes", "widget"} {
		if len(jobs[name].Steps) == 0 {
			p = append(p, name+": no steps, the pin check would be vacuous")
		}
		for _, s := range jobs[name].Steps {
			if s.Uses != "" && !widgetUsesRE.MatchString(s.Uses) {
				p = append(p, name+": action not SHA-pinned: "+s.Uses)
			}
			if strings.Contains(s.Run, "brew ") {
				p = append(p, name+": Homebrew install in a lane step")
			}
		}
	}
	return p
}

// TestWidgetLaneActionsSHAPinned: no floating action tag in the widget lane.
func TestWidgetLaneActionsSHAPinned(t *testing.T) {
	widgetRequireNone(t, widgetPinProblems(widgetReal(t)))
	widgetExpectProblem(t, "ci-floating-tag.yml", widgetPinProblems(widgetSeed(t, "ci-floating-tag.yml")), "not SHA-pinned")
	widgetExpectProblem(t, "ci-brew-xcodegen.yml", widgetPinProblems(widgetSeed(t, "ci-brew-xcodegen.yml")), "Homebrew")
}

// widgetGoProblems: setup-go reads go.mod and runs before any Go or Xcode step.
func widgetGoProblems(jobs map[string]widgetJob) []string {
	goAt, firstUse := -1, -1
	for i, s := range jobs["widget"].Steps {
		if strings.HasPrefix(s.Uses, "actions/setup-go@") && s.With["go-version-file"] == "go.mod" && goAt < 0 {
			goAt = i
		}
		if firstUse < 0 && (strings.Contains(s.Run, "go ") || strings.Contains(s.Run, "xcodebuild")) {
			firstUse = i
		}
	}
	switch {
	case goAt < 0:
		return []string{"widget job has no actions/setup-go step with go-version-file: go.mod"}
	case firstUse >= 0 && goAt > firstUse:
		return []string{"setup-go runs after the first step that needs Go"}
	}
	return nil
}

func TestWidgetLaneSetsUpGo(t *testing.T) {
	widgetRequireNone(t, widgetGoProblems(widgetReal(t)))
	widgetExpectProblem(t, "ci-no-setup-go.yml", widgetGoProblems(widgetSeed(t, "ci-no-setup-go.yml")), "no actions/setup-go")
}
