package census

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: cross-platform Census logic (tracked-binary filtering, snapshot
//
//	construction, flag redaction, the planted-credential canary, and the
//	OS-enumeration-failure path) exercised entirely through a mocked
//	procSource, so none of it depends on syscalls or the live process
//	table on any platform.
//
// SPORT: fleet/census (ADD, per T-1 sport_updates).

func fakeCensus(source procSource, accountDirs map[string]string) *census {
	return &census{source: source, accountDirs: accountDirs}
}

func TestEnumerateHappyPath(t *testing.T) {
	source := func() ([]rawProcess, error) {
		return []rawProcess{
			{pid: 100, argv: []string{"/usr/local/bin/claude", "--json", "--debug"}},
		}, nil
	}
	c := fakeCensus(source, map[string]string{"/home/acct-a/.cascade": "acct-a"})

	got, err := c.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	want := Snapshot{Pid: 100, Binary: "claude", Account: "", Flags: []string{"--json", "--debug"}}
	if got[0].Pid != want.Pid || got[0].Binary != want.Binary || got[0].Account != want.Account {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}
	if strings.Join(got[0].Flags, ",") != strings.Join(want.Flags, ",") {
		t.Fatalf("Flags = %v, want %v", got[0].Flags, want.Flags)
	}
}

func TestEnumerateEmpty(t *testing.T) {
	c := fakeCensus(func() ([]rawProcess, error) { return nil, nil }, nil)
	got, err := c.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

func TestEnumerateFiltersUntrackedBinaries(t *testing.T) {
	source := func() ([]rawProcess, error) {
		return []rawProcess{
			{pid: 1, argv: []string{"/bin/bash", "-c", "echo hi"}},
			{pid: 2, argv: []string{"/usr/local/bin/codex", "--print"}},
		}, nil
	}
	c := fakeCensus(source, nil)
	got, err := c.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(got) != 1 || got[0].Pid != 2 {
		t.Fatalf("got %+v, want exactly pid 2", got)
	}
}

func TestEnumerateOSErrorPropagates(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "census: read /proc")
	c := fakeCensus(func() ([]rawProcess, error) { return nil, wantErr }, nil)
	_, err := c.Enumerate()
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Enumerate error kind = %v, want unavailable", err)
	}
}

func TestEnumerateSkipsEmptyArgv(t *testing.T) {
	source := func() ([]rawProcess, error) {
		return []rawProcess{{pid: 5, argv: nil}}, nil
	}
	c := fakeCensus(source, nil)
	got, err := c.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}

func TestRedactFlagsDropsUnknownAndOperands(t *testing.T) {
	argv := []string{"/usr/bin/opencode", "--json", "--api-key=secretvalue", "positional"}
	flags := redactFlags(argv)
	if len(flags) != 1 || flags[0] != "--json" {
		t.Fatalf("redactFlags = %v, want [--json]", flags)
	}
}

// TestPlantedCredentialCanary is R-21.271's canary: a discovered process
// carrying a planted secret in a flag operand must never surface it, in
// exact, base64 or hex form, anywhere on the resulting Snapshot. This
// asserts the first-boundary redaction fails closed on a hit rather than
// merely happening to pass today.
func TestPlantedCredentialCanary(t *testing.T) {
	const canary = "AKIA" + "7YQ2XPLM4RZV6WTB" // split per AGENT-BRIEF.md's push-protection note
	b64 := base64.StdEncoding.EncodeToString([]byte(canary))
	hexed := hex.EncodeToString([]byte(canary))

	argv := []string{
		"/usr/local/bin/claude",
		"--api-key=" + canary,
		"--json",
		"--token-b64=" + b64,
		"--token-hex=" + hexed,
	}
	c := fakeCensus(func() ([]rawProcess, error) {
		return []rawProcess{{pid: 42, argv: argv}}, nil
	}, map[string]string{"/home/acct-a/.cascade": "acct-a"})

	got, err := c.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	snap := got[0]

	forms := []string{canary, b64, hexed}
	for _, form := range forms {
		if strings.Contains(snap.Binary, form) {
			t.Fatalf("canary %q leaked into Binary %q", form, snap.Binary)
		}
		if strings.Contains(snap.Account, form) {
			t.Fatalf("canary %q leaked into Account %q", form, snap.Account)
		}
		for _, f := range snap.Flags {
			if strings.Contains(f, form) {
				t.Fatalf("canary %q leaked into Flags %v", form, snap.Flags)
			}
		}
	}
	if snap.Flags != nil && len(snap.Flags) != 1 {
		t.Fatalf("Flags = %v, want only [--json]", snap.Flags)
	}
	// No configured account directory appears anywhere in argv here, so
	// attribution must resolve to unknown.
	if snap.Account != "" {
		t.Fatalf("Account = %q, want empty (no legitimate directory matched)", snap.Account)
	}
}

func TestIsTrackedBinary(t *testing.T) {
	cases := map[string]bool{
		"/usr/local/bin/claude": true,
		"codex":                 true,
		"/opt/bin/opencode":     true,
		"/bin/bash":             false,
		"":                      false,
	}
	for argv0, want := range cases {
		if got := isTrackedBinary(argv0); got != want {
			t.Errorf("isTrackedBinary(%q) = %v, want %v", argv0, got, want)
		}
	}
}

func TestParseLinuxCmdline(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []string
	}{
		{"empty", nil, nil},
		{"single-no-trailing-nul", []byte("claude"), []string{"claude"}},
		{"two-args", []byte("claude\x00--json\x00"), []string{"claude", "--json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLinuxCmdline(tc.in)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("parseLinuxCmdline(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseDarwinProcArgs2(t *testing.T) {
	buf := []byte{2, 0, 0, 0} // argc=2, little-endian
	buf = append(buf, []byte("/usr/local/bin/claude\x00\x00\x00")...)
	buf = append(buf, []byte("claude\x00--json\x00")...)
	buf = append(buf, []byte("HOME=/root\x00")...) // environment, must never surface

	argv, err := parseDarwinProcArgs2(buf)
	if err != nil {
		t.Fatalf("parseDarwinProcArgs2: %v", err)
	}
	if strings.Join(argv, "|") != "claude|--json" {
		t.Fatalf("argv = %v, want [claude --json]", argv)
	}
	for _, a := range argv {
		if strings.Contains(a, "HOME") {
			t.Fatalf("environment leaked into argv: %v", argv)
		}
	}
}

func TestParseDarwinProcArgs2ShortBuffer(t *testing.T) {
	if _, err := parseDarwinProcArgs2([]byte{1, 2}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err kind = %v, want invalid-input", err)
	}
}

func TestParseDarwinProcArgs2NoTerminator(t *testing.T) {
	buf := []byte{1, 0, 0, 0}
	buf = append(buf, []byte("no-nul-here")...)
	if _, err := parseDarwinProcArgs2(buf); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err kind = %v, want invalid-input", err)
	}
}

func TestSnapshotFieldPinCompiles(_ *testing.T) {
	// snapshotFieldPin's mere existence is the assertion (see census.go);
	// this test just proves it is referenced from a shipping-adjacent
	// path so the compiler actually evaluates the conversion.
	_ = snapshotFieldPin
}
