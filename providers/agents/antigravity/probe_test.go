package antigravity

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func isolatedProbeHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func contractHelp(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/protocol/1/help-tokens.txt")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, token := range strings.Fields(string(b)) {
		if strings.HasPrefix(token, "--") {
			lines = append(lines, "  "+token+" <value>  option")
		}
	}
	return "agy " + PinnedAgyVersion + "\n" + strings.Join(lines, "\n") +
		"\n  --output-format <format>  choices: text, json, stream-json\n"
}

func probeHelp(t *testing.T, help string) ProbeResult {
	t.Helper()
	return Probe(context.Background(), func(name string) (string, error) {
		if name != "agy" {
			t.Fatalf("lookup = %q", name)
		}
		return "/fixture/agy", nil
	}, func(_ context.Context, bin string) ([]byte, error) {
		if bin != "/fixture/agy" {
			t.Fatalf("binary = %q", bin)
		}
		return []byte(help), nil
	})
}

func TestAntigravityProbePresent(t *testing.T) {
	isolatedProbeHome(t)
	got := probeHelp(t, contractHelp(t))
	if got.Outcome != ProbePresent || got.Version != PinnedAgyVersion || got.Reason != "" {
		t.Fatalf("probe = %+v", got)
	}
}

func TestAntigravityProbeAbsentOrChanged(t *testing.T) {
	isolatedProbeHome(t)
	got := Probe(context.Background(), func(string) (string, error) {
		return "", exec.ErrNotFound
	}, func(context.Context, string) ([]byte, error) {
		t.Fatal("help ran for absent binary")
		return nil, nil
	})
	if got.Outcome != ProbeAbsent || got.Reason == "" {
		t.Fatalf("absent = %+v", got)
	}
	for _, token := range strings.Fields(requiredHelpTokens) {
		help := strings.ReplaceAll(contractHelp(t), token, "removed")
		if strings.HasPrefix(token, "--") {
			help += "\n  --other <value>  mentions " + token + " in prose\n"
		}
		got := probeHelp(t, help)
		if got.Outcome != ProbeChanged || !strings.Contains(got.Reason, token) {
			t.Errorf("missing %s = %+v", token, got)
		}
	}
}

func TestAntigravityProbeResultTyped(t *testing.T) {
	isolatedProbeHome(t)
	result := probeHelp(t, contractHelp(t))
	outcome := result.Outcome
	if outcome != ProbeOutcome("present") || ProbeAbsent != "absent" || ProbeChanged != "changed" {
		t.Fatal("typed outcomes changed")
	}
	got := Probe(context.Background(), func(string) (string, error) {
		return "/fixture/agy", nil
	}, func(context.Context, string) ([]byte, error) {
		return nil, errors.New("help failed")
	})
	if got.Outcome != ProbeChanged || got.Reason == "" {
		t.Fatalf("failed help = %+v", got)
	}
}

func TestAntigravityProbeFailures(t *testing.T) {
	isolatedProbeHome(t)
	lookup := func(string) (string, error) { return "/fixture/agy", nil }
	help := func(context.Context, string) ([]byte, error) { return []byte(contractHelp(t)), nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, result := range []ProbeResult{
		Probe(context.Background(), nil, help),
		Probe(context.Background(), lookup, nil),
		Probe(ctx, lookup, help),
		Probe(context.Background(), func(string) (string, error) { return "", nil }, help),
		Probe(context.Background(), func(string) (string, error) {
			return "", errors.New("lookup denied")
		}, help),
	} {
		if result.Outcome != ProbeChanged || result.Reason == "" {
			t.Fatalf("unestablished contract = %+v", result)
		}
	}
}

func TestAntigravityHelpDeclarations(t *testing.T) {
	isolatedProbeHome(t)
	for _, line := range []string{
		"  --output-format <format>  choices: stream-json",
		"  --output-format <format>\n      choices: stream-json",
	} {
		flags := helpFlags(line)
		if !flags["--output-format"] || !flags["stream-json"] {
			t.Fatalf("format choices missing: %q", line)
		}
	}
	for _, line := range []string{
		"  --other <value>  use --print for output",
		"  --print-extra <value>  use --print for output",
		"  --output-format <format>\n  --other <value>  stream-json",
		"  --output-format <format>\nUnrelated stream-json prose",
		"  --output-format <format>  stream-json-extra",
	} {
		flags := helpFlags(line)
		if flags["--print"] || flags["stream-json"] {
			t.Fatalf("description accepted: %q", line)
		}
	}
	if !helpFlags("  -p, --print  output")["--print"] {
		t.Fatal("short alias hid the declaration")
	}
}
