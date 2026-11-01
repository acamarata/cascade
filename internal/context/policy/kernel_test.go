package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// isolate points every home-like variable at a temp directory.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", filepath.Join(home, ".cascade-home"))
}

// wantKind asserts err is a cascade error of kind whose message holds msg.
func wantKind(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	ce, ok := err.(*cascade.Error)
	if !ok {
		t.Fatalf("error %v (%T) is not a *cascade.Error", err, err)
	}
	if ce.Kind != kind || !strings.Contains(ce.Msg, msg) {
		t.Fatalf("error = {%s, %q}, want {%s, ~%q}", ce.Kind, ce.Msg, kind, msg)
	}
}

func TestKernelTokenBudget(t *testing.T) {
	if n := TokenCount(KernelText); n > 120 || n == 0 {
		t.Fatalf("TokenCount(KernelText) = %d, want 1..120", n)
	}
	if TokenCount(strings.Repeat("abcd", 121)) != 121 {
		t.Fatal("TokenCount is not the four-code-point counter")
	}
	lines := strings.Split(strings.TrimSuffix(KernelText, "\n"), "\n")
	want := []string{"SCOPE:", "SIMPLICITY:", "GROUNDING:", "AMBIGUITY:", "VERIFICATION:", "ESCALATION:"}
	if len(lines) != len(want) {
		t.Fatalf("KernelText has %d lines, want %d", len(lines), len(want))
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, want[i]) {
			t.Errorf("line %d = %q, want prefix %q", i+1, l, want[i])
		}
	}
	if KernelVersion != "1" || kernelBlockID != "policy-kernel@1" {
		t.Fatalf("kernel version %q / block %q", KernelVersion, kernelBlockID)
	}
}

func TestPolicyKernelHashConst(t *testing.T) {
	file := RenderKernelFile()
	sum := sha256.Sum256(file)
	if got := hex.EncodeToString(sum[:]); got != provider.PolicyKernelSHA256 {
		t.Fatalf("sha256(RenderKernelFile()) = %s, provider.PolicyKernelSHA256 = %s", got, provider.PolicyKernelSHA256)
	}
	if len(file) > provider.PolicyKernelMaxBytes {
		t.Fatalf("kernel file is %d bytes, over %d", len(file), provider.PolicyKernelMaxBytes)
	}
	want := "<!-- cascade:managed:begin policy-kernel@1 -->\n" + KernelText + "<!-- cascade:managed:end -->\n"
	if string(file) != want {
		t.Fatalf("RenderKernelFile() = %q", file)
	}
}

func TestEnsureKernelFileCreatesThenKeeps(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	created, err := EnsureKernelFile(root)
	if err != nil || !created {
		t.Fatalf("first EnsureKernelFile = %v, %v; want created", created, err)
	}
	p := filepath.Join(root, filepath.FromSlash(provider.PolicyKernelFile))
	if got, _ := os.ReadFile(p); string(got) != string(RenderKernelFile()) {
		t.Fatalf("kernel file = %q", got)
	}
	created, err = EnsureKernelFile(root)
	if err != nil || created {
		t.Fatalf("second EnsureKernelFile = %v, %v; want no-op", created, err)
	}
}

func TestEnsureKernelFileRefusesEditedFile(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	p := filepath.Join(root, filepath.FromSlash(provider.PolicyKernelFile))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("hand kernel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureKernelFile(root)
	wantKind(t, err, cascade.KindConflict, "differs from the canonical kernel")
	if got, _ := os.ReadFile(p); string(got) != "hand kernel\n" {
		t.Fatalf("edited kernel was touched: %q", got)
	}
}

func TestEnsureKernelFileRefusesBadInput(t *testing.T) {
	isolate(t)
	_, err := EnsureKernelFile("")
	wantKind(t, err, cascade.KindInvalidInput, "empty repository root")

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(provider.PolicyKernelFile)), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = EnsureKernelFile(root)
	wantKind(t, err, cascade.KindConflict, "is not a regular file")

	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, ".cascade"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = EnsureKernelFile(blocked)
	if err == nil {
		t.Fatal("EnsureKernelFile under a file named .cascade succeeded")
	}
}
