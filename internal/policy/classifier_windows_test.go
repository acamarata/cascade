//go:build windows

package policy

import "testing"

// TestWindowsNativeSyntaxFallsThroughToL4 is the CI-asserted refusal
// R-14.28 requires (Art.5.2). The classifier parses POSIX shell grammar.
// There is no Windows matcher and no build-tagged classification code:
// Windows-native command forms reach the SAME table every other command
// reaches, miss it, and are refused as (L4, classify-unknown).
//
// What the classifier CAN reason about on Windows: a command line written
// in POSIX shell syntax, which is what every shell-tool invocation in this
// runtime uses, classifies identically on Windows and on Unix: the table
// is one table, compiled into every platform build, and this file adds no
// rows to it.
//
// What it CANNOT reason about: PowerShell and cmd.exe. Their grammars are
// not POSIX shell. A PowerShell pipeline, a cmd.exe builtin, a cmdlet, a
// drive-letter path, and PowerShell's own variable and redirection syntax
// are either parsed as something they are not or not parsed at all, so the
// classifier must not claim to have understood them. Every such form is
// refused here rather than guessed at, which is the fail-closed answer:
// the caller asks for permission and a human decides.
func TestWindowsNativeSyntaxFallsThroughToL4(t *testing.T) {
	forms := []string{
		"Get-ChildItem",
		"Get-ChildItem -Recurse -Force C:\\Users",
		"Remove-Item -Recurse -Force C:\\Users\\data",
		"Get-Content .\\notes.txt",
		"Set-ExecutionPolicy Bypass",
		"Invoke-WebRequest https://example.com -OutFile out.bin",
		"Start-Process powershell -Verb RunAs",
		"dir /s",
		"del /f /q C:\\temp",
		"rmdir /s /q C:\\temp",
		"copy a.txt b.txt",
		"move a.txt b.txt",
		"type notes.txt",
		"cmd.exe /c del C:\\temp",
		"powershell.exe -Command Remove-Item",
		"reg delete HKLM\\Software\\Example /f",
		"net user attacker password /add",
		"schtasks /create /tn evil /tr calc.exe /sc minute",
		"icacls C:\\ /grant Everyone:F",
		"format C: /q",
	}
	for _, cmd := range forms {
		t.Run(cmd, func(t *testing.T) {
			got, err := classify(t, cmd)
			if got != L4 {
				t.Fatalf("Classify(%q) = %s on windows; a Windows-native form must fall through the table to L4", cmd, got)
			}
			if err == nil {
				t.Fatalf("Classify(%q) returned L4 with no error; the refusal must be named, not implied", cmd)
			}
		})
	}
}

// TestWindowsPosixFormsClassifyIdentically proves the fall-through above
// is not the classifier refusing everything on Windows: a POSIX command
// line lands on exactly the rung it lands on everywhere else.
func TestWindowsPosixFormsClassifyIdentically(t *testing.T) {
	mustClassify(t, "ls -la", L0)
	mustClassify(t, "go test ./...", L1)
	mustClassify(t, "git add .", L2)
	mustClassify(t, "git push origin main", L3)
	if got, _ := classify(t, "rm -rf /tmp/data"); got != L4 {
		t.Fatalf("rm classified as %s on windows, want L4", got)
	}
}
