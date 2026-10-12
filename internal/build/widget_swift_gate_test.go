// Purpose: durable source gates for the macOS widget Swift tree (apps/widget-macos):
// file cap, paired tests, banned APIs, no stubs. Each rule scans a tree root into a
// sorted problem list: the real tree yields none, each seeded tree under
// testdata/seeded-violations/widget-swift/ yields exactly its own problems. Also
// hosts TestWidgetChangesFilterSemantics (widget_ci_test.go is at the 300-line cap).
package build

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type widgetSwiftFile struct {
	dir, name string // name is the slash path inside dir, so nested folders are scanned
	lines     []string
}

func (f widgetSwiftFile) prod() bool {
	return f.dir == "CascadeMenuBar" || f.dir == "CascadeWidgetExtension"
}
func (f widgetSwiftFile) rel() string  { return f.dir + "/" + f.name }
func (f widgetSwiftFile) base() string { return path.Base(f.name) }

func widgetSwiftFiles(t *testing.T, root string) (out []widgetSwiftFile) {
	t.Helper()
	for _, d := range []string{"CascadeMenuBar", "CascadeMenuBarTests", "CascadeWidgetExtension", "CascadeWidgetExtensionTests"} {
		top := filepath.Join(root, d)
		err := filepath.WalkDir(top, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || !strings.HasSuffix(p, ".swift") {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			raw, err := os.ReadFile(p)
			rel, _ := filepath.Rel(top, p)
			out = append(out, widgetSwiftFile{d, filepath.ToSlash(rel), strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")})
			return err
		})
		if err != nil {
			t.Fatalf("widget swift gate: %v", err)
		}
	}
	return out
}

// widgetCode drops a trailing // comment, leaving string literals intact.
func widgetCode(line string) string {
	inStr := false
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && inStr:
			i++
		case line[i] == '"':
			inStr = !inStr
		case !inStr && strings.HasPrefix(line[i:], "//"):
			return line[:i]
		}
	}
	return line
}

func widgetFileCapProblems(files []widgetSwiftFile) (p []string) {
	for _, f := range files {
		if len(f.lines) > 300 {
			p = append(p, fmt.Sprintf("%s: %d lines", f.rel(), len(f.lines)))
		}
	}
	return p
}

// widgetPairedProblems: production X.swift needs a Tests file in the matching target
// named X*Tests.swift. The SwiftUI @main entry point has no behaviour to test.
func widgetPairedProblems(files []widgetSwiftFile) (p []string) {
	for _, f := range files {
		base, found := strings.TrimSuffix(f.base(), ".swift"), false
		for _, c := range files {
			found = found || c.dir == f.dir+"Tests" && strings.HasPrefix(c.base(), base) && strings.HasSuffix(c.name, "Tests.swift")
		}
		if f.prod() && !found && f.base() != "CascadeMenuBarApp.swift" {
			p = append(p, f.rel()+": no paired "+base+"*Tests.swift in "+f.dir+"Tests")
		}
	}
	return p
}

// widgetBanned: re in production code (comments excluded); only limits the rule to
// one target dir or file, except exempts one file.
var widgetBanned = []struct{ name, only, except, re string }{
	{"NSAppleScript", "", "", `NSAppleScript`}, {"sh -c", "", "", `sh -c`}, {"String(format:", "", "", `String\(format:`},
	{"/bin/sh", "", "ReauthCommandFile.swift", `/bin/sh`}, {"show_project_names", "", "", `show_project_names`},
	{"Network or URLSession in the extension", "CascadeWidgetExtension", "", `\bNetwork\b|\bURLSession\b|\bNW[A-Z]\w+`},
	{"NSHomeDirectory in WidgetSelfReport.swift", "WidgetSelfReport.swift", "", `NSHomeDirectory`},
}

func widgetBannedProblems(files []widgetSwiftFile) (p []string) {
	for _, f := range files {
		code := ""
		for _, l := range f.lines {
			code += widgetCode(l) + "\n"
		}
		for _, r := range widgetBanned {
			if f.prod() && (r.only == "" || r.only == f.dir || r.only == f.base()) && r.except != f.base() && regexp.MustCompile(r.re).MatchString(code) {
				p = append(p, f.rel()+": "+r.name)
			}
		}
	}
	sort.Strings(p)
	return p
}

var widgetStubRE = regexp.MustCompile(`\b(TODO|FIXME)\b|(?i)fatalError\(\s*"[^"]*not implemented`)
var widgetDoubleRE = regexp.MustCompile(`\b(class|struct|enum|actor|protocol)\s+\w*(Mock|Fake|Stub)`)

func widgetNoStubProblems(files []widgetSwiftFile) (p []string) {
	for _, f := range files {
		for i, l := range f.lines {
			if f.prod() && (widgetStubRE.MatchString(l) || widgetDoubleRE.MatchString(widgetCode(l))) {
				p = append(p, fmt.Sprintf("%s:%d: stub marker", f.rel(), i+1))
			}
		}
	}
	return p
}

// widgetSwiftRun checks the real tree is non-vacuous and clean, then the seed.
func widgetSwiftRun(t *testing.T, scan func([]widgetSwiftFile) []string, seed string, want ...string) {
	t.Helper()
	tree := widgetSwiftFiles(t, filepath.Join(moduleRoot(t), "apps", "widget-macos"))
	if len(tree) < 20 {
		t.Fatalf("widget swift gate: only %d Swift files in the real tree; the scan would be vacuous", len(tree))
	}
	if got := scan(tree); len(got) != 0 {
		t.Errorf("real tree: %s", strings.Join(got, "; "))
	}
	got := scan(widgetSwiftFiles(t, filepath.Join(moduleRoot(t), "internal", "build", "testdata", "seeded-violations", "widget-swift", seed)))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("seed %s: got %q, want %q", seed, got, want)
	}
}

func TestWidgetSwiftFileCap(t *testing.T) {
	widgetSwiftRun(t, widgetFileCapProblems, "filecap", "CascadeMenuBar/TooLong.swift: 301 lines")
}

func TestWidgetSwiftPairedTests(t *testing.T) {
	widgetSwiftRun(t, widgetPairedProblems, "paired", "CascadeWidgetExtension/Orphan.swift: no paired Orphan*Tests.swift in CascadeWidgetExtensionTests")
}

// TestWidgetSwiftBannedAPIs: the seed also carries allowed uses (a comment mention,
// /bin/sh in ReauthCommandFile.swift, Network outside the extension, NSHomeDirectory
// outside WidgetSelfReport.swift, banned words in a test target).
func TestWidgetSwiftBannedAPIs(t *testing.T) {
	widgetSwiftRun(t, widgetBannedProblems, "banned-apis",
		"CascadeMenuBar/AppleScriptBad.swift: NSAppleScript", "CascadeMenuBar/FormatBad.swift: String(format:",
		"CascadeMenuBar/PrivacyBad.swift: show_project_names", "CascadeMenuBar/ShellBad.swift: /bin/sh",
		"CascadeMenuBar/ShellBad.swift: sh -c", "CascadeMenuBar/WidgetSelfReport.swift: NSHomeDirectory in WidgetSelfReport.swift",
		"CascadeWidgetExtension/NetBad.swift: Network or URLSession in the extension")
}

func TestWidgetSwiftNoStubs(t *testing.T) {
	widgetSwiftRun(t, widgetNoStubProblems, "no-stubs", "CascadeMenuBar/StubBad.swift:3: stub marker",
		"CascadeMenuBar/StubBad.swift:4: stub marker", "CascadeMenuBar/StubBad.swift:5: stub marker", "CascadeMenuBar/StubBad.swift:7: stub marker",
		"CascadeMenuBar/Views/NestedBad.swift:2: stub marker")
}

// widgetGlob compiles a paths-filter glob (picomatch semantics, dot files match).
func widgetGlob(g string) *regexp.Regexp {
	b := strings.Builder{}
	for i := 0; i < len(g); i++ {
		switch {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i++
		case g[i] == '*':
			b.WriteString("[^/]*")
		default:
			b.WriteString(regexp.QuoteMeta(string(g[i])))
		}
	}
	return regexp.MustCompile("^" + b.String() + "$")
}

func widgetFilterHit(rules []string, every bool, files []string) bool {
	for _, f := range files {
		ok := every
		for _, r := range rules {
			m := widgetGlob(strings.TrimPrefix(r, "!")).MatchString(f) != strings.HasPrefix(r, "!")
			ok = every && ok && m || !every && (ok || m)
		}
		if ok {
			return true
		}
	}
	return false
}

var widgetFilterCases = []struct {
	files []string
	want  bool
}{
	{[]string{"cmd/cascade/wire_status_widget.go"}, true}, {[]string{"internal/daemon/x.go"}, true},
	{[]string{"internal/secrets/x.go"}, true}, {[]string{"go.mod"}, true}, {[]string{"apps/widget-macos/README.md"}, true},
	{[]string{"docs/a.md"}, false}, {[]string{".github/wiki/B.md"}, false}, {[]string{"README.md"}, false},
	{[]string{"docs/a.md", "internal/daemon/x.go"}, true}, {[]string{"docs/img/a.png"}, false},
}

// widgetFilterProblems evaluates the paths-filter spec and the output expression
// read from the changes job against widgetFilterCases and the missing-base case.
func widgetFilterProblems(jobs map[string]widgetJob) []string {
	var spec widgetStep
	for _, s := range jobs["changes"].Steps {
		if strings.HasPrefix(s.Uses, "dorny/paths-filter@") {
			spec = s
		}
	}
	filters := map[string][]string{}
	if err := yaml.Unmarshal([]byte(spec.With["filters"]), &filters); err != nil || len(filters) == 0 {
		return []string{fmt.Sprintf("changes job has no readable paths-filter spec (%v)", err)}
	}
	if q := spec.With["predicate-quantifier"]; q != "every" && q != "" && q != "some" {
		return []string{"unsupported predicate-quantifier " + q}
	}
	expr, p := jobs["changes"].Outputs["widget"], []string(nil)
	eval := func(known string, files []string) (bool, error) {
		vals := map[string]string{"base.known": known}
		for name, rules := range filters {
			vals[spec.ID+"."+name] = fmt.Sprint(known == "true" && widgetFilterHit(rules, spec.With["predicate-quantifier"] == "every", files))
		}
		return widgetEvalOutput(expr, vals)
	}
	for _, c := range widgetFilterCases {
		if got, err := eval("true", c.files); err != nil || got != c.want {
			p = append(p, fmt.Sprintf("%v evaluates to widget=%v (err %v), want %v", c.files, got, err, c.want))
		}
	}
	if got, err := eval("false", nil); err != nil || !got {
		p = append(p, "an unknown diff base does not yield widget=true")
	}
	return p
}

// widgetEvalOutput evaluates an `||` chain of `steps.<id>.outputs.<n> ==|!= 'v'`.
func widgetEvalOutput(expr string, vals map[string]string) (bool, error) {
	expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(expr), "${{"), "}}"))
	clause := regexp.MustCompile(`^steps\.([\w-]+)\.outputs\.([\w-]+)\s*(==|!=)\s*'([^']*)'$`)
	for _, c := range strings.Split(expr, "||") {
		m := clause.FindStringSubmatch(strings.TrimSpace(c))
		if m == nil {
			return false, fmt.Errorf("unsupported clause %q", strings.TrimSpace(c))
		}
		if (vals[m[1]+"."+m[2]] == m[4]) == (m[3] == "==") {
			return true, nil
		}
	}
	return false, nil
}

// TestWidgetChangesFilterSemantics: the filter spec in ci.yml skips the lane only
// for documentation-only changes (the specified paths evaluate as listed), a
// missing or all-zero base yields widget=true, and the base step really does that.
func TestWidgetChangesFilterSemantics(t *testing.T) {
	widgetRequireNone(t, widgetFilterProblems(widgetReal(t)))
	widgetExpectProblem(t, "ci-filter-allowlist.yml", widgetFilterProblems(widgetSeed(t, "ci-filter-allowlist.yml")), "cmd/cascade/wire_status_widget.go")
	widgetExpectProblem(t, "ci-filter-no-fallback.yml", widgetFilterProblems(widgetSeed(t, "ci-filter-no-fallback.yml")), "unknown diff base")
	var base widgetStep
	for _, s := range widgetReal(t)["changes"].Steps {
		if s.ID == "base" {
			base = s
		}
	}
	repo := t.TempDir()
	sha, code := widgetBash(t, repo, "set -eu; git init -q .; git -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m a; git rev-parse HEAD", nil)
	if sha = strings.TrimSpace(sha); code != 0 || len(sha) != 40 {
		t.Fatalf("fixture repo: %q rc=%d", sha, code)
	}
	zero, absent := strings.Repeat("0", 40), strings.Repeat("a", 40)
	for _, c := range []struct{ event, before, pr, want string }{
		{"push", sha, "", "true"}, {"push", "", "", "false"}, {"push", zero, "", "false"}, {"push", absent, "", "false"},
		{"pull_request", sha, "", "false"}, {"pull_request", zero, sha, "true"}, {"pull_request", "", absent, "false"},
	} {
		out, _ := widgetBash(t, repo, base.Run, []string{"GITHUB_OUTPUT=/dev/stdout", "EVENT_NAME=" + c.event, "BEFORE=" + c.before, "PR_BASE=" + c.pr})
		if !strings.Contains(out, "known="+c.want+"\n") {
			t.Errorf("base step %s before=%.8s pr=%.8s: want known=%s, got %q", c.event, c.before, c.pr, c.want, out)
		}
	}
}
