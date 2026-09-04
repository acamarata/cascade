package policy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// classify runs the production classifier over one command.
func classify(t *testing.T, cmd string) (RiskLevel, error) {
	t.Helper()
	return NewCommandClassifier().Classify(context.Background(), cmd)
}

// mustClassify asserts a command lands on want with no error.
func mustClassify(t *testing.T, cmd string, want RiskLevel) {
	t.Helper()
	got, err := classify(t, cmd)
	if err != nil {
		t.Fatalf("Classify(%q) returned %v; want %s with no error", cmd, err, want)
	}
	if got != want {
		t.Fatalf("Classify(%q) = %s, want %s", cmd, got, want)
	}
}

// mustRefuse asserts a command is refused at L4 with the named refusal.
func mustRefuse(t *testing.T, cmd string, want *ClassifyError) {
	t.Helper()
	got, err := classify(t, cmd)
	if err == nil {
		t.Fatalf("Classify(%q) = %s with no error; want a refusal at L4", cmd, got)
	}
	if got != L4 {
		t.Fatalf("Classify(%q) = %s alongside a refusal; a refusal is ALWAYS L4", cmd, got)
	}
	if !errors.Is(err, want) {
		t.Fatalf("Classify(%q) refused with %v; want %s", cmd, err, want.Code)
	}
}

// TestRepresentativeCommandsPerRung drives one command for each phrase the
// §5.15 ladder uses to describe a rung, so the table is checked against
// what the spec SAYS each rung is for rather than against itself.
func TestRepresentativeCommandsPerRung(t *testing.T) {
	cases := []struct {
		specPhrase string
		cmd        string
		want       RiskLevel
	}{
		{"read-only", "ls -la /tmp", L0},
		{"read-only", "cat README.md", L0},
		{"read-only", "git status", L0},
		{"safe local dev: tests", "go test ./...", L1},
		{"safe local dev: lint", "golangci-lint run ./...", L1},
		{"safe local dev: build", "go build ./...", L1},
		{"workspace mutation", "git add .", L2},
		{"workspace mutation", "git commit -m hello", L2},
		{"workspace mutation", "mkdir -p build", L2},
		{"external side effect: push", "git push origin main", L3},
		{"external side effect: PR", "gh pr create --fill", L3},
		{"external side effect: network", "curl https://example.com", L3},
		{"destructive/privileged", "rm -rf /", L4},
		{"destructive/privileged", "sudo systemctl restart nginx", L4},
		{"destructive/privileged", "dd if=/dev/zero of=/dev/disk0", L4},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got, err := classify(t, tc.cmd)
			if tc.want == L4 {
				if got != L4 {
					t.Fatalf("Classify(%q) = %s, want L4 (%s)", tc.cmd, got, tc.specPhrase)
				}
				return
			}
			if err != nil {
				t.Fatalf("Classify(%q) refused with %v; the spec calls this %s", tc.cmd, err, tc.specPhrase)
			}
			if got != tc.want {
				t.Fatalf("Classify(%q) = %s, want %s (%s)", tc.cmd, got, tc.want, tc.specPhrase)
			}
		})
	}
}

// TestFailClosedInputs covers every "the classifier could not tell" case
// §5.15 names. Each one must land at L4 with a named refusal, never at a
// permissive rung and never at some middle default.
func TestFailClosedInputs(t *testing.T) {
	parseErrors := []string{
		`ls "`,
		`echo $(`,
		`ls | | ls`,
		`)`,
		"ls 'unterminated",
	}
	for _, cmd := range parseErrors {
		t.Run("parse/"+cmd, func(t *testing.T) { mustRefuse(t, cmd, ErrClassifyParseError) })
	}

	unknown := []string{
		"",
		"   ",
		"\n\t ",
		"# only a comment",
		"frobnicate --now",
		"./scripts/deploy.sh",
		"git frobnicate",
		"go run ./cmd/tool",
		"go generate ./...",
		"cargo run",
		"awk 'BEGIN{system(\"rm -rf /\")}'",
		"python3 -c 'import os; os.system(\"rm -rf /\")'",
		"perl -e 'unlink glob \"*\"'",
		"node -e 'require(\"fs\").rmSync(\"/\", {recursive:true})'",
	}
	for _, cmd := range unknown {
		t.Run("unknown/"+cmd, func(t *testing.T) { mustRefuse(t, cmd, ErrClassifyUnknown) })
	}
}

// TestUnmodeledShellFormsAreRefused proves the node-form gate is an allow
// list. Each of these parses cleanly, so a classifier that walked only the
// forms it recognised would return whatever it found and silently ignore
// the rest.
func TestUnmodeledShellFormsAreRefused(t *testing.T) {
	forms := []string{
		"if true; then ls; fi",
		"for f in a b; do ls; done",
		"while true; do ls; done",
		"case x in a) ls ;; esac",
		"f() { ls; }",
		"echo $((1 + 2))",
		"diff <(ls) <(ls)",
		"ls !(keepme)",
		"declare -x SAFE=1",
		"let x=1",
		"time ls",
		"[[ -f go.mod ]]",
		"coproc ls",
	}
	for _, cmd := range forms {
		t.Run(cmd, func(t *testing.T) {
			got, err := classify(t, cmd)
			if got != L4 || err == nil {
				t.Fatalf("Classify(%q) = %s, err=%v; an unmodeled shell form must be refused at L4", cmd, got, err)
			}
		})
	}
}

// TestBraceExpansionIsNotAnEscape: mvdan.cc/sh does not expand braces
// during parsing, so `{a,b}` arrives as an ordinary literal. That is safe
// in an argument (the command in front still decides the rung) and refused
// in a command name, which is the position where an expansion would
// change what runs.
func TestBraceExpansionIsNotAnEscape(t *testing.T) {
	mustClassify(t, "echo {a,b}", L0)
	mustClassify(t, "rm -rf /{a,b}", L4)
	mustRefuse(t, "{ls,rm} -rf /", ErrClassifyUnknown)
}

// TestRefusalNamesWhatWasNotUnderstood keeps the refusal actionable: a
// user who is told only "denied" cannot fix their command.
func TestRefusalNamesWhatWasNotUnderstood(t *testing.T) {
	_, err := classify(t, "frobnicate --now")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	var refusal *ClassifyError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal is %T, want *ClassifyError", err)
	}
	if !strings.Contains(refusal.Error(), "frobnicate") {
		t.Errorf("refusal %q does not name the command that was not understood", refusal.Error())
	}
}

// TestCanceledContextRefuses proves ctx is honoured and that honouring it
// fails closed rather than returning a level nobody computed.
func TestCanceledContextRefuses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := NewCommandClassifier().Classify(ctx, "ls")
	if got != L4 || err == nil {
		t.Fatalf("Classify with a canceled context = %s, err=%v; want L4 and a refusal", got, err)
	}
}

// TestClassifyIsDeterministic: the same command must classify the same way
// every time (Art.7). Map iteration order inside the table must not leak
// into the result.
func TestClassifyIsDeterministic(t *testing.T) {
	cmds := []string{"ls", "go test ./...", "git add .", "git push", "rm -rf /", "sh -c 'git push'"}
	for _, cmd := range cmds {
		first, firstErr := classify(t, cmd)
		for i := 0; i < 20; i++ {
			got, err := classify(t, cmd)
			if got != first || (err == nil) != (firstErr == nil) {
				t.Fatalf("Classify(%q) is not deterministic: %s/%v then %s/%v", cmd, first, firstErr, got, err)
			}
		}
	}
}

// fuzzSeedDir is relative to this package's own directory, matching the
// module convention of keeping every corpus under internal/testdata/fuzz.
const fuzzSeedDir = "../testdata/fuzz/FuzzClassifier"

// TestFuzzClassifierSeedProvenanceExists asserts the corpus README the
// contract requires is present.
func TestFuzzClassifierSeedProvenanceExists(t *testing.T) {
	info, err := os.Stat(filepath.Join(fuzzSeedDir, "README.md"))
	if err != nil {
		t.Fatalf("provenance README missing: %v", err)
	}
	if info.IsDir() {
		t.Fatal("README.md is a directory, want a file")
	}
}

// loadFuzzSeeds reads every seed file in the corpus directory.
func loadFuzzSeeds(f *testing.F) []string {
	f.Helper()
	entries, err := os.ReadDir(fuzzSeedDir)
	if err != nil {
		f.Fatalf("reading fuzz seed dir %s: %v", fuzzSeedDir, err)
	}
	var seeds []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == "README.md" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(fuzzSeedDir, e.Name()))
		if readErr != nil {
			f.Fatalf("reading seed file %s: %v", e.Name(), readErr)
		}
		seeds = append(seeds, string(data))
	}
	if len(seeds) == 0 {
		f.Fatalf("no seed files found in %s", fuzzSeedDir)
	}
	return seeds
}

// FuzzClassifier drives arbitrary bytes through the parser and the table.
// The classifier reads attacker-controlled strings, so the properties
// asserted here are the ones a caller relies on: it never panics, it
// always returns a rung that is on the ladder, and it never pairs a
// refusal with a permissive rung.
func FuzzClassifier(f *testing.F) {
	for _, seed := range loadFuzzSeeds(f) {
		f.Add(seed)
	}
	for _, seed := range []string{"", "\x00", "ls\n\nrm -rf /", "$(", "\\rm -rf /"} {
		f.Add(seed)
	}
	classifier := NewCommandClassifier()
	f.Fuzz(func(t *testing.T, cmd string) {
		ctx := context.Background()
		level, err := classifier.Classify(ctx, cmd)
		if !level.Valid() {
			t.Fatalf("Classify(%q) returned the invalid level %d", cmd, level)
		}
		if err != nil && level != L4 {
			t.Fatalf("Classify(%q) refused with %v but returned %s; a refusal is always L4", cmd, err, level)
		}
		if err != nil {
			var refusal *ClassifyError
			if !errors.As(err, &refusal) {
				t.Fatalf("Classify(%q) returned %T, want *ClassifyError", cmd, err)
			}
		}
		again, againErr := classifier.Classify(ctx, cmd)
		if again != level || (againErr == nil) != (err == nil) {
			t.Fatalf("Classify(%q) is not deterministic", cmd)
		}
	})
}
