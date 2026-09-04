package policy

import (
	"os"
	"strings"
	"testing"
)

// TestObfuscationCannotDowngradeADestructiveCommand takes ONE genuinely
// destructive command and writes it every way a caller could. Every form
// must land where the plain form lands. This is the test that matters
// most: a classifier is only worth having if the answer does not depend on
// how the attacker chose to spell the command.
func TestObfuscationCannotDowngradeADestructiveCommand(t *testing.T) {
	plain := "rm -rf /tmp/data"
	if got, _ := classify(t, plain); got != L4 {
		t.Fatalf("the plain form Classify(%q) = %s, want L4; the rest of this test is meaningless without it", plain, got)
	}
	for _, form := range obfuscatedDeletionForms() {
		t.Run(form.why, func(t *testing.T) {
			got, _ := classify(t, form.cmd)
			if got != L4 {
				t.Fatalf("Classify(%q) = %s, want L4; %s must not downgrade a destructive command",
					form.cmd, got, form.why)
			}
		})
	}
}

// obfuscatedDeletionForms lists every spelling of the same deletion.
func obfuscatedDeletionForms() []struct{ why, cmd string } {
	return []struct{ why, cmd string }{
		{"fully quoted name", `"rm" -rf /tmp/data`},
		{"single-quoted name", `'rm' -rf /tmp/data`},
		{"partially quoted name", `r"m" -rf /tmp/data`},
		{"partially single-quoted name", `'r'm -rf /tmp/data`},
		{"quoted in the middle", `r'm' -rf /tmp/data`},
		{"backslash before the name", `\rm -rf /tmp/data`},
		{"backslash inside the name", `r\m -rf /tmp/data`},
		{"backslash inside double quotes", `"r\m" -rf /tmp/data`},
		{"absolute path", `/bin/rm -rf /tmp/data`},
		{"relative path", `./rm -rf /tmp/data`},
		{"path with traversal", `/usr/bin/../bin/rm -rf /tmp/data`},
		{"quoted path", `"/bin/rm" -rf /tmp/data`},
		{"chained after a safe command", `ls && rm -rf /tmp/data`},
		{"chained with a semicolon", `ls; rm -rf /tmp/data`},
		{"chained with or", `ls || rm -rf /tmp/data`},
		{"piped into", `ls | rm -rf /tmp/data`},
		{"backgrounded", `rm -rf /tmp/data &`},
		{"negated", `! rm -rf /tmp/data`},
		{"in a subshell", `(rm -rf /tmp/data)`},
		{"in a brace block", `{ rm -rf /tmp/data; }`},
		{"nested in a subshell in a chain", `ls && (ls; { rm -rf /tmp/data; })`},
		{"behind a shell wrapper", `sh -c 'rm -rf /tmp/data'`},
		{"behind two shell wrappers", `sh -c "bash -c 'rm -rf /tmp/data'"`},
		{"behind xargs", `xargs rm -rf`},
		{"behind ssh", `ssh build-host rm -rf /tmp/data`},
		{"behind sudo", `sudo rm -rf /tmp/data`},
		{"inside a command substitution", `ls $(rm -rf /tmp/data)`},
		{"inside a backquoted substitution", "ls `rm -rf /tmp/data`"},
		{"inside a substitution in a quoted word", `ls "$(rm -rf /tmp/data)"`},
		{"with output redirected away", `rm -rf /tmp/data > /dev/null 2>&1`},
		{"with input redirected in", `rm -rf /tmp/data < /dev/null`},
		{"with an environment prefix", `FOO=bar rm -rf /tmp/data`},
		{"with extra whitespace and newlines", "\n  rm   -rf   /tmp/data  \n"},
	}
}

// TestUnresolvableFormsForceTheTopRung covers the constructs the AST
// genuinely cannot see through. Each must be refused on its own account,
// whether or not anything dangerous is visible.
func TestUnresolvableFormsForceTheTopRung(t *testing.T) {
	forms := []struct {
		why string
		cmd string
	}{
		{"variable as the command name", `$CMD -rf /tmp/data`},
		{"braced variable as the command name", `${CMD} -rf /tmp/data`},
		{"variable spliced into the name", `r${X}`},
		{"substitution as the command name", `$(echo rm) -rf /tmp/data`},
		{"substitution appended to a name", `rm$() -rf /tmp/data`},
		{"C-style quoted name", `$'\x72\x6d' -rf /tmp/data`},
		{"variable subcommand", `git $VERB`},
		{"variable argument to an arg-sensitive command", `find . $ACTION`},
		{"variable redirection target", `echo hello > $TARGET`},
	}
	for _, form := range forms {
		t.Run(form.why, func(t *testing.T) {
			got, err := classify(t, form.cmd)
			if got != L4 || err == nil {
				t.Fatalf("Classify(%q) = %s, err=%v; %s cannot be resolved and must be refused at L4",
					form.cmd, got, err, form.why)
			}
		})
	}
}

// TestEvalIsDestructiveWhateverItsArgument: eval runs a string the
// classifier cannot read, so it sits at L4 by name. It is classified
// rather than refused, so a caller can say WHY it is denied.
func TestEvalIsDestructiveWhateverItsArgument(t *testing.T) {
	for _, cmd := range []string{
		`eval "$PAYLOAD"`,
		`eval "rm -rf /tmp/data"`,
		"eval \"$(echo cm0gLXJmIC8= | base64 -d)\"",
		`. ./script.sh`,
		`source ./script.sh`,
		`exec rm -rf /tmp/data`,
	} {
		t.Run(cmd, func(t *testing.T) {
			if got, _ := classify(t, cmd); got != L4 {
				t.Fatalf("Classify(%q) = %s, want L4", cmd, got)
			}
		})
	}
}

// TestRedirectionRaisesTheRung: a redirection that writes is a workspace
// mutation even when the command in front of it only reads.
func TestRedirectionRaisesTheRung(t *testing.T) {
	cases := []struct {
		cmd  string
		want RiskLevel
	}{
		{"echo hello", L0},
		{"echo hello > notes.txt", L2},
		{"echo hello >> notes.txt", L2},
		{"echo hello >| notes.txt", L2},
		{"echo hello &> notes.txt", L2},
		{"echo hello &>> notes.txt", L2},
		{"echo hello <> notes.txt", L2},
		{"echo hello > /dev/null", L0},
		{"echo hello 2>&1", L0},
		{"echo hello > /dev/null 2>&1", L0},
		{"cat < notes.txt", L0},
		{"cat <<< inline", L0},
		{"go test ./... > results.txt", L2},
		{"git push > log.txt", L3},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) { mustClassify(t, tc.cmd, tc.want) })
	}
}

// TestHeredocIsInputNotAWrite proves a here-document is read as input.
func TestHeredocIsInputNotAWrite(t *testing.T) {
	mustClassify(t, "cat <<EOF\nhello\nEOF\n", L0)
}

// TestLoaderEnvironmentPrefixesAreRefused: an environment prefix that
// changes how a command is resolved or loaded can substitute an
// attacker's binary for the one the argv names, so the argv no longer
// says what will run.
func TestLoaderEnvironmentPrefixesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`PATH=/tmp/evil ls`,
		`PATH=/tmp/evil:$PATH go test ./...`,
		`LD_PRELOAD=/tmp/evil.so ls`,
		`DYLD_INSERT_LIBRARIES=/tmp/evil.dylib ls`,
		`IFS=, ls`,
		`BASH_ENV=/tmp/evil.sh sh -c ls`,
		`PATH=/tmp/evil`,
	} {
		t.Run(cmd, func(t *testing.T) { mustRefuse(t, cmd, ErrClassifyUnknown) })
	}
}

// TestOrdinaryEnvironmentPrefixIsLocalDev keeps the loader rule narrow:
// an ordinary variable is shell-local state, not a privilege change.
func TestOrdinaryEnvironmentPrefixIsLocalDev(t *testing.T) {
	mustClassify(t, "CGO_ENABLED=0 go build ./...", L1)
	mustClassify(t, "FOO=bar", L1)
	mustClassify(t, "FOO=bar ls", L1)
}

// TestArgumentSensitiveCommandsEscalate: a formatter that rewrites files
// in place is a workspace mutation, and find(1) that runs a command or
// deletes files is destructive.
func TestArgumentSensitiveCommandsEscalate(t *testing.T) {
	cases := []struct {
		cmd  string
		want RiskLevel
	}{
		{"gofmt -l ./internal", L0},
		{"gofmt -w ./internal", L2},
		{"prettier --write src", L2},
		{"prettier --check src", L0},
		{"find . -name '*.go'", L0},
		{"find . -name '*.go' -delete", L4},
		{"find . -type f -exec rm {} ;", L4},
		{"find . -type f -okdir rm {} ;", L4},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got, _ := classify(t, tc.cmd)
			if got != tc.want {
				t.Fatalf("Classify(%q) = %s, want %s", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestSubcommandTableRefusesUnknownVerbs: a verb the table does not carry
// is refused rather than inheriting the command's other verbs.
func TestSubcommandTableRefusesUnknownVerbs(t *testing.T) {
	for _, cmd := range []string{"git nonesuch", "go nonesuch", "cargo nonesuch", "git", "go"} {
		t.Run(cmd, func(t *testing.T) { mustRefuse(t, cmd, ErrClassifyUnknown) })
	}
}

// TestNoBuildTaggedClassificationCode enforces R-14.28 structurally: the
// Windows caveat is a fall-through in the shared table, not a platform
// file. A production file in this package carrying a GOOS build
// constraint would mean the classification a caller gets depends on where
// it runs, which is exactly what the ruling struck.
func TestNoBuildTaggedClassificationCode(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, suffix := range []string{"_windows.go", "_linux.go", "_darwin.go", "_unix.go", "_other.go"} {
			if strings.HasSuffix(name, suffix) {
				t.Errorf("%s is a platform file; classification must not vary by GOOS", name)
			}
		}
		data, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatalf("reading %s: %v", name, readErr)
		}
		if strings.Contains(string(data), "//go:build") {
			t.Errorf("%s carries a build constraint; classification must not vary by GOOS", name)
		}
	}
}

// TestUnescaping covers the two escaping rules directly, because they are
// what stands between `r\m` and a table miss that would have run rm.
func TestUnescaping(t *testing.T) {
	unquoted := map[string]string{
		`rm`: "rm", `\rm`: "rm", `r\m`: "rm", `\\rm`: `\rm`,
		`r\`: "r", `\`: "", `a\ b`: "a b",
	}
	for in, want := range unquoted {
		if got := unescapeUnquoted(in); got != want {
			t.Errorf("unescapeUnquoted(%q) = %q, want %q", in, got, want)
		}
	}
	double := map[string]string{
		`rm`: "rm", `r\m`: `r\m`, `r\\m`: `r\m`, `\$rm`: "$rm",
		"r\\`m": "r`m", `r\"m`: `r"m`, `rm\`: `rm\`,
	}
	for in, want := range double {
		if got := unescapeDouble(in); got != want {
			t.Errorf("unescapeDouble(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEscapedNameInsideDoubleQuotesStillResolves ties the rule above back
// to a command line.
func TestEscapedNameInsideDoubleQuotesStillResolves(t *testing.T) {
	// Inside double quotes a backslash before "r" is literal, so this
	// names a file called \rm, not rm. The classifier follows the shell
	// exactly, and the unknown name is refused.
	mustRefuse(t, `"\rm" -rf /tmp/data`, ErrClassifyUnknown)
	mustClassify(t, `\rm -rf /tmp/data`, L4)
	mustClassify(t, `"g"i"t" push origin main`, L3)
}
