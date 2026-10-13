package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/output"
)

func runWith(stdout, stderr *bytes.Buffer, args []string) int {
	return run(args, output.New(stdout, stderr, false, false, false, true))
}

// realRoot is the module root, found from this file's location.
func realRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("no caller info")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..")
}

// withRegistry swaps the committed registry for the test.
func withRegistry(t *testing.T, rows []build.LaneRow) {
	t.Helper()
	saved := build.IntegrationLanes
	build.IntegrationLanes = rows
	t.Cleanup(func() { build.IntegrationLanes = saved })
}

// stream renders a go test -json stream for pkg: each test with its action and
// optional output line.
func stream(pkg string, tests map[string]string, outputs map[string]string) string {
	full := "github.com/acamarata/cascade/" + strings.TrimPrefix(pkg, "./")
	var b strings.Builder
	order := []string{}
	for name := range tests {
		order = append(order, name)
	}
	for _, name := range order {
		fmt.Fprintf(&b, `{"Action":"run","Package":%q,"Test":%q}`+"\n", full, name)
		if o := outputs[name]; o != "" {
			fmt.Fprintf(&b, `{"Action":"output","Package":%q,"Test":%q,"Output":%q}`+"\n", full, name, o)
		}
		fmt.Fprintf(&b, `{"Action":%q,"Package":%q,"Test":%q}`+"\n", tests[name], full, name)
	}
	fmt.Fprintf(&b, `{"Action":"pass","Package":%q}`+"\n", full)
	return b.String()
}

func writeRow(t *testing.T, dir string, n int, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", n)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUsageAndFlagErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"nope"}, {"plan", "--bogus"}, {"report"}, {"report", "--dir", "x"}, {"assert-red"}} {
		var stdout, stderr bytes.Buffer
		if code := runWith(&stdout, &stderr, args); code != 2 {
			t.Errorf("%v exit %d, want 2 (%s)", args, code, stderr.String())
		}
	}
}

func TestPlanSeededPrintsOnlyTheSeededRow(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runWith(&stdout, &stderr, []string{"plan", "--seeded"}); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	want := build.SeededRedRow.Package + "\tintegration seededred\t^TestSeededRed$\n"
	if stdout.String() != want {
		t.Errorf("got %q want %q", stdout.String(), want)
	}
}

func TestPlanRefusesAStaleRegistryAndPrintsRunnerRows(t *testing.T) {
	withRegistry(t, nil)
	var stdout, stderr bytes.Buffer
	if code := runWith(&stdout, &stderr, []string{"plan", "--root", t.TempDir()}); code != 1 || !strings.Contains(stderr.String(), "not a module root") {
		t.Fatalf("a root without ci.yml must exit 1, got %d: %s", code, stderr.String())
	}
	rows := []build.LaneRow{
		{Package: "./a", Tags: "integration", RunRegex: "^TestA$", Job: build.LaneJob, OS: []string{build.RunnerLinux, build.RunnerMacOS}},
		{Package: "./b", Tags: "postgres integration", RunRegex: ".", Job: build.LaneJob, OS: []string{build.RunnerWindows}},
		{Package: "./c", Tags: "integration", Job: "elsewhere", OS: []string{build.RunnerLinux}},
	}
	withRegistry(t, rows)
	if got := laneRows(build.RunnerLinux); len(got) != 1 || got[0].Package != "./a" {
		t.Errorf("linux lane rows = %v, want only ./a", got)
	}
	if got := laneRows(build.RunnerWindows); len(got) != 1 || got[0].Package != "./b" {
		t.Errorf("windows lane rows = %v, want only ./b", got)
	}
	if defaultRunner() == "" {
		t.Error("no default runner")
	}
}

func TestPlanOverTheRealTree(t *testing.T) {
	for _, runner := range []string{build.RunnerLinux, build.RunnerMacOS, build.RunnerWindows} {
		var stdout, stderr bytes.Buffer
		if code := runWith(&stdout, &stderr, []string{"plan", "--root", realRoot(t), "--os", runner}); code != 0 {
			t.Fatalf("%s: exit %d: %s", runner, code, stderr.String())
		}
		lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		if len(lines) != len(laneRows(runner)) || len(lines) == 0 {
			t.Fatalf("%s: %d plan lines for %d rows", runner, len(lines), len(laneRows(runner)))
		}
		for _, l := range lines {
			if len(strings.Split(l, "\t")) != 3 {
				t.Fatalf("plan line is not package<TAB>tags<TAB>regex: %q", l)
			}
		}
	}
}

func TestReportRules(t *testing.T) {
	const gate = "gate: skipped on this runner"
	rows := []build.LaneRow{
		{Package: "./a", Tags: "integration", RunRegex: "^(TestA|TestB)$", Job: build.LaneJob, OS: []string{build.RunnerLinux}},
		{Package: "./s", Tags: "integration", RunRegex: "^TestS$", Job: build.LaneJob, OS: []string{build.RunnerLinux}, SkipOn: map[string]string{build.RunnerLinux: gate}},
	}
	withRegistry(t, rows)
	cases := map[string]struct {
		a, s string
		want int
	}{
		"all green, declared skip": {stream("./a", map[string]string{"TestA": "pass", "TestB": "pass"}, nil), stream("./s", map[string]string{"TestS": "skip"}, map[string]string{"TestS": "    x_test.go:1: " + gate + "\n"}), 0},
		"skip without the message": {stream("./a", map[string]string{"TestA": "pass", "TestB": "pass"}, nil), stream("./s", map[string]string{"TestS": "skip"}, map[string]string{"TestS": "    x_test.go:1: other\n"}), 1},
		"renamed test dropped":     {stream("./a", map[string]string{"TestA": "pass"}, nil), stream("./s", map[string]string{"TestS": "skip"}, map[string]string{"TestS": gate + "\n"}), 1},
		"wrong package events":     {stream("./a", map[string]string{"TestA": "pass", "TestB": "pass"}, nil), stream("./a", map[string]string{"TestS": "pass"}, nil), 1},
		"failing test":             {stream("./a", map[string]string{"TestA": "fail", "TestB": "pass"}, nil), stream("./s", map[string]string{"TestS": "skip"}, map[string]string{"TestS": gate + "\n"}), 1},
	}
	for name, c := range cases {
		dir := t.TempDir()
		writeRow(t, dir, 1, c.a)
		writeRow(t, dir, 2, c.s)
		out := filepath.Join(dir, "report.json")
		var stdout, stderr bytes.Buffer
		if code := runWith(&stdout, &stderr, []string{"report", "--dir", dir, "--out", out, "--os", build.RunnerLinux}); code != c.want {
			t.Errorf("%s: exit %d want %d\n%s%s", name, code, c.want, stdout.String(), stderr.String())
		}
		if _, err := os.Stat(out); err != nil {
			t.Errorf("%s: no report written: %v", name, err)
		}
	}
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runWith(&stdout, &stderr, []string{"report", "--dir", dir, "--out", filepath.Join(dir, "r.json"), "--os", build.RunnerLinux}); code != 1 {
		t.Errorf("rows with no captured output must fail, got %d", code)
	}
	if code := runWith(&stdout, &stderr, []string{"report", "--dir", dir, "--out", filepath.Join(dir, "no", "such", "r.json"), "--os", build.RunnerLinux}); code != 1 {
		t.Errorf("an unwritable report must exit 1, got %d", code)
	}
}

func TestSeededReportAndAssertRed(t *testing.T) {
	pkg := build.SeededRedRow.Package
	dir := t.TempDir()
	out := filepath.Join(dir, "report.json")
	var stdout, stderr bytes.Buffer
	args := func(cmd ...string) []string { return cmd }
	writeRow(t, dir, 1, stream(pkg, map[string]string{"TestSeededRed": "fail"}, nil))
	if code := runWith(&stdout, &stderr, args("report", "--dir", dir, "--out", out)); code != 1 {
		t.Fatalf("a failing seeded row must make report exit 1, got %d", code)
	}
	if code := runWith(&stdout, &stderr, args("assert-red", "--report", out)); code != 0 {
		t.Fatalf("assert-red must accept the genuine red report, got %d: %s", code, stderr.String())
	}
	for name, body := range map[string]string{
		"passes":        stream(pkg, map[string]string{"TestSeededRed": "pass"}, nil),
		"zero events":   `{"Action":"fail","Package":"github.com/acamarata/cascade/` + strings.TrimPrefix(pkg, "./") + `"}` + "\n",
		"other failure": stream(pkg, map[string]string{"TestOther": "fail"}, nil),
	} {
		d := t.TempDir()
		writeRow(t, d, 1, body)
		o := filepath.Join(d, "report.json")
		runWith(&stdout, &stderr, args("report", "--dir", d, "--out", o))
		if code := runWith(&stdout, &stderr, args("assert-red", "--report", o)); code != 1 {
			t.Errorf("%s: assert-red must refuse, got %d", name, code)
		}
	}
	if code := runWith(&stdout, &stderr, args("assert-red", "--report", filepath.Join(dir, "missing.json"))); code != 1 {
		t.Errorf("a missing report must exit 1, got %d", code)
	}
}

func TestWindowsSkipNeedsADeclaredMessage(t *testing.T) {
	const gate = "daemon unix socket: Windows tier-2"
	withRegistry(t, []build.LaneRow{
		{Package: "./u", Tags: "integration", RunRegex: ".", Job: build.LaneJob, OS: []string{build.RunnerWindows}},
		{Package: "./d", Tags: "integration", RunRegex: ".", Job: build.LaneJob, OS: []string{build.RunnerWindows}, SkipOn: map[string]string{build.RunnerWindows: gate}},
	})
	mixed := func(pkg, out string) string {
		return stream(pkg, map[string]string{"TestA": "pass", "TestS": "skip"}, map[string]string{"TestS": out})
	}
	cases := map[string]struct {
		u, d string
		want int
	}{
		"declared skip beside a pass":    {stream("./u", map[string]string{"TestA": "pass"}, nil), mixed("./d", gate+"\n"), 0},
		"undeclared skip on windows":     {mixed("./u", "some reason\n"), mixed("./d", gate+"\n"), 1},
		"declared row, other skip text":  {stream("./u", map[string]string{"TestA": "pass"}, nil), mixed("./d", "other\n"), 1},
		"gate text from another test":    {stream("./u", map[string]string{"TestA": "pass"}, nil), stream("./d", map[string]string{"TestA": "pass", "TestS": "skip"}, map[string]string{"TestA": gate + "\n", "TestS": "unrelated\n"}), 1},
		"declared row, all skipped, say": {stream("./u", map[string]string{"TestA": "pass"}, nil), stream("./d", map[string]string{"TestS": "skip"}, map[string]string{"TestS": gate + "\n"}), 0},
	}
	for name, c := range cases {
		dir := t.TempDir()
		writeRow(t, dir, 1, c.u)
		writeRow(t, dir, 2, c.d)
		var stdout, stderr bytes.Buffer
		args := []string{"report", "--dir", dir, "--out", filepath.Join(dir, "r.json"), "--os", build.RunnerWindows}
		if code := runWith(&stdout, &stderr, args); code != c.want {
			t.Errorf("%s: exit %d want %d\n%s", name, code, c.want, stdout.String())
		}
	}
}
