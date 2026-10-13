// Purpose: gates for the integration-lane registry: tag parsing, discovery over
// a synthetic tree, coverage of the real tree against the real ci.yml, the
// allowed-fail refusal, the red-proof workflow shape and the assert-red guard.
package build

import (
	"fmt"
	"go/build/constraint"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func realCI(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(moduleRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func validCI(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "integrationlanes", "ci-valid.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeTree writes files (path -> content) under a fresh temp dir with HOME and
// USERPROFILE redirected there, and returns the dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestIntegrationLanesTagParse(t *testing.T) {
	cases := map[string]bool{
		"integration":                                   true,
		"!windows && integration":                       true,
		"postgres && integration":                       true,
		"unix && integration":                           true,
		"linux && cgo && pam && integration":            true,
		"(linux && windows) || (!linux && integration)": true,
		"!integration":                                  false,
		"integration || linux":                          false,
		"windows":                                       false,
		"postgres":                                      false,
		"darwin && windows && integration":              false,
		"integration && !integration":                   false,
		"t1 || t2 || t3 || t4 || t5 || t6 || t7 || t8 || t9 || t10 || t11 || t12 || t13 || t14 || t15 || t16 || t17 || integration": false,
	}
	for line, want := range cases {
		expr, err := constraint.Parse("//go:build " + line)
		if err != nil {
			t.Fatal(err)
		}
		got, err := RequiresTag(expr, "integration")
		if err != nil || got != want {
			t.Errorf("RequiresTag(%q) = %v, %v; want %v", line, got, err, want)
		}
	}
	// A substring match would call this package integration-constrained.
	dir := writeTree(t, map[string]string{
		"a/a_test.go": "//go:build !integrationless\n\npackage a\n",
		"b/b_test.go": "// //go:build integration\npackage b\n",
		"c/c_test.go": "//go:build integration\n\npackage c\n",
	})
	got, err := DiscoverIntegrationPackages(dir)
	if err != nil || strings.Join(got, ",") != "./c" {
		t.Errorf("Discover = %v, %v; want only ./c", got, err)
	}
}

func TestIntegrationLanesCoverTree(t *testing.T) {
	pkgs, err := DiscoverIntegrationPackages(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) == 0 {
		t.Fatal("the real tree has no integration package; the discovery is blind")
	}
	for _, p := range CheckLaneCoverage(pkgs, IntegrationLanes, IntegrationExcluded, realCI(t)) {
		t.Error(p)
	}
}

func TestIntegrationLanesSeededUnmappedRed(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"x/y/y_test.go":              "//go:build integration\n\npackage y\n",
		"testdata/seeded/s_test.go":  "//go:build integration\n\npackage s\n",
		"z/z_test.go":                "//go:build postgres\n\npackage z\n",
		"nested/go.mod":              "module nested\n",
		"nested/n_test.go":           "//go:build integration\n\npackage n\n",
		"_skip/k_test.go":            "//go:build integration\n\npackage k\n",
		"x/y/testdata/t/t_test.go":   "//go:build integration\n\npackage t\n",
		"x/y/plain_test.go":          "package y\n",
		"x/y/untagged/plain_test.go": "package u\n",
	})
	pkgs, err := DiscoverIntegrationPackages(dir)
	if err != nil || strings.Join(pkgs, ",") != "./x/y" {
		t.Fatalf("Discover = %v, %v; want only ./x/y (testdata, nested module, underscore dir and other tags ignored)", pkgs, err)
	}
	got := strings.Join(CheckLaneCoverage(pkgs, nil, nil, []byte(validCI(t))), "\n")
	if !strings.Contains(got, "unmapped integration package ./x/y") {
		t.Errorf("an unmapped package must be named; got:\n%s", got)
	}
	rows := []LaneRow{{Package: "./gone", Tags: "integration", RunRegex: ".", Job: LaneJob, OS: allRunners}}
	got = strings.Join(CheckLaneCoverage(pkgs, rows, map[string]string{"./x/y": ""}, []byte(validCI(t))), "\n")
	for _, want := range []string{"stale integration row: ./gone", "has no reason"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestIntegrationLanesRefuseAllowedFail(t *testing.T) {
	row := LaneRow{Package: "./providers/postgres", Tags: "postgres integration", Job: "pg", OS: []string{RunnerLinux}}
	pkgs := []string{row.Package}
	ok := validCI(t)
	if p := CheckLaneCoverage(pkgs, []LaneRow{row}, nil, []byte(ok)); len(p) != 0 {
		t.Fatalf("valid workflow reported %v", p)
	}
	cases := map[string]struct{ ci, want string }{
		"continue-on-error":  {strings.Replace(ok, "  pg:\n", "  pg:\n    continue-on-error: true\n", 1), "continue-on-error"},
		"not in ci-gate":     {strings.Replace(ok, ", pg]", "]", 1), "not in ci-gate needs"},
		"job absent":         {strings.Replace(ok, "  pg:\n", "  other:\n", 1), `job "pg" is absent`},
		"package not run":    {strings.Replace(ok, "./providers/postgres/...", "./providers/other/...", 1), "has no step running it"},
		"tag missing":        {strings.Replace(ok, `"postgres integration"`, "postgres", 1), "has no step running it"},
		"windows excluded":   {strings.Replace(ok, "      - run: go test -tags=\"postgres", "      - if: matrix.goos != 'windows'\n        run: go test -tags=\"postgres", 1), "has no step running it"},
		"lane soft-fails":    {strings.Replace(ok, "  integration-lanes:\n", "  integration-lanes:\n    continue-on-error: true\n", 1), "integration-lanes must exist"},
		"lane not in needs":  {strings.Replace(ok, "[integration-lanes, ", "[", 1), "integration-lanes must exist"},
		"scalar needs shape": {strings.Replace(ok, "needs: [integration-lanes, integration-lanes-red-assert, pg]", "needs: pg", 1), "is not in ci-gate needs"},
	}
	for name, c := range cases {
		withWin := row
		if name == "windows excluded" {
			withWin.OS = allRunners
		}
		got := strings.Join(CheckLaneCoverage(pkgs, []LaneRow{withWin}, nil, []byte(c.ci)), "\n")
		if got == "" || !strings.Contains(got, c.want) {
			t.Errorf("%s: want a problem containing %q, got %q", name, c.want, got)
		}
	}
	bad := LaneRow{Package: "./p", Tags: "x", RunRegex: "(", Job: LaneJob, OS: []string{"plan9", RunnerLinux}, SkipOn: map[string]string{RunnerMacOS: ""}}
	got := strings.Join(CheckLaneCoverage([]string{"./p"}, []LaneRow{bad, bad}, nil, []byte(ok)), "\n")
	for _, want := range []string{"lack integration", `unknown runner "plan9"`, "SkipOn", "invalid", "two rows for ./p on ubuntu-latest"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestIntegrationLanesRedProofShape(t *testing.T) {
	ok := validCI(t)
	if p := CheckLaneCoverage(nil, nil, nil, []byte(ok)); len(p) != 0 {
		t.Fatalf("valid workflow reported %v", p)
	}
	cases := map[string]string{
		"has continue-on-error":    strings.Replace(ok, "  integration-lanes-red-proof:\n", "  integration-lanes-red-proof:\n    continue-on-error: true\n", 1),
		"must not be in ci-gate":   strings.Replace(ok, "[integration-lanes,", "[integration-lanes, integration-lanes-red-proof,", 1),
		"job is missing":           strings.Replace(ok, "  integration-lanes-red-proof:\n", "  integration-lanes-red-proofx:\n", 1),
		"upload step lacks":        strings.Replace(ok, "        if: always()\n  integration-lanes-red-assert", "  integration-lanes-red-assert", 1),
		"assert is not in ci-gate": strings.Replace(ok, "integration-lanes-red-assert, pg]", "pg]", 1),
		"assert lacks if":          strings.Replace(ok, "    if: always()\n    runs-on: ubuntu-latest\n    steps:\n      - run: go run ./internal/build/gen/integrationlane assert-red", "    runs-on: ubuntu-latest\n    steps:\n      - run: go run ./internal/build/gen/integrationlane assert-red", 1),
		"does not need":            strings.Replace(ok, "needs: integration-lanes-red-proof", "needs: integration-lanes", 1),
	}
	for want, ci := range cases {
		if ci == ok {
			t.Fatalf("%s: the mutation did not change the workflow", want)
		}
		if got := strings.Join(CheckLaneCoverage(nil, nil, nil, []byte(ci)), "\n"); !strings.Contains(got, want) {
			t.Errorf("want a problem containing %q, got %q", want, got)
		}
	}
	if p := CheckLaneCoverage(nil, nil, nil, []byte("jobs: [")); len(p) != 1 || !strings.Contains(p[0], "does not parse") {
		t.Errorf("a broken workflow must be reported, got %v", p)
	}
}

func TestAssertRedZeroMatchGuard(t *testing.T) {
	rep := func(rows ...string) []byte { return []byte(`{"rows":[` + strings.Join(rows, ",") + `],"ok":false}`) }
	row := func(pkg string, run, failed int, tests string, ok bool, reason string) string {
		return fmt.Sprintf(`{"package":%q,"run":%d,"failed":%d,"failed_tests":%s,"ok":%v,"reason":%q}`, pkg, run, failed, tests, ok, reason)
	}
	seeded := SeededRedRow.Package
	red := row(seeded, 1, 1, `["TestSeededRed"]`, false, "1 failed")
	cases := map[string]struct {
		data []byte
		want string
	}{
		"zero run events": {rep(row(seeded, 0, 0, `[]`, false, "zero tests ran")), "ran no tests"},
		"build failure":   {rep(row(seeded, 0, 0, `null`, false, "package failed (build error)")), "ran no tests"},
		"pass":            {rep(row(seeded, 1, 0, `[]`, true, "")), "passed"},
		"two failing":     {rep(red, row("./other", 1, 1, `["TestX"]`, false, "1 failed")), "2 failing rows"},
		"extra passing":   {rep(red, row("./other", 1, 0, `[]`, true, "")), "besides"},
		"wrong test":      {rep(row(seeded, 2, 1, `["TestOther"]`, false, "1 failed")), "not through TestSeededRed"},
		"panic no fail":   {rep(row(seeded, 1, 0, `[]`, false, "package failed")), "not through TestSeededRed"},
		"other package":   {rep(row("./other", 1, 1, `["TestSeededRed"]`, false, "x")), "want " + seeded},
		"empty":           {rep(), "no rows"},
		"garbage":         {[]byte("{"), "does not parse"},
	}
	for name, c := range cases {
		if err := AssertRedProof(c.data); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", name, c.want, err)
		}
	}
	if err := AssertRedProof(rep(red)); err != nil {
		t.Errorf("the genuine red report must be accepted: %v", err)
	}
}
