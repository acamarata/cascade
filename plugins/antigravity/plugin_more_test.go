package antigravity

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestHandlersRejectToolsIntentsAndUnknownCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := t.Context()
	out, err := (handlers{}).DispatchTool(ctx, "scan", []byte("{}"))
	if out != nil || !cascade.HasKind(err, cascade.KindUnsupported) || !strings.Contains(err.Error(), `"scan"`) {
		t.Fatalf("DispatchTool = %q, %v; want unsupported naming the tool", out, err)
	}
	out, err = (handlers{}).DispatchIntent(ctx, "plan", nil)
	if out != nil || !cascade.HasKind(err, cascade.KindUnsupported) || !strings.Contains(err.Error(), `"plan"`) {
		t.Fatalf("DispatchIntent = %q, %v; want unsupported naming the intent", out, err)
	}
	err = (handlers{}).RunCommand(ctx, "install", nil)
	if !cascade.HasKind(err, cascade.KindUnsupported) || !strings.Contains(err.Error(), `"install"`) {
		t.Fatalf("RunCommand(install) = %v, want unsupported", err)
	}
}

func TestRunCommandDetectSucceedsWhenAgyIsPresentOrAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	previous := lookPath
	t.Cleanup(func() { lookPath = previous })
	for _, lookErr := range []error{nil, os.ErrNotExist} {
		lookPath = func(string) (string, error) { return "/x/agy", lookErr }
		if err := (handlers{}).RunCommand(t.Context(), cmdDetect, nil); err != nil {
			t.Errorf("RunCommand(detect) with lookup error %v = %v, want nil", lookErr, err)
		}
	}
}

func TestDefaultLookPathSkipsNonExecutablesAndKeepsFilesystemFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory and mode semantics differ on windows")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	dirWithAgy := filepath.Join(root, "bin")
	if err := os.MkdirAll(filepath.Join(dirWithAgy, "agy"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dirWithAgy)
	if _, err := defaultLookPath("agy"); !errors.Is(err, cascade.ErrNotFound) {
		t.Fatalf("a directory named agy = %v, want not-found", err)
	}
	notDir := filepath.Join(root, "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", notDir)
	_, err := defaultLookPath("agy")
	if err == nil || errors.Is(err, cascade.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PATH entry that is a file = %v, want the stat failure kept, not absence", err)
	}
	if got := candidateNames("agy"); len(got) != 1 || got[0] != "agy" {
		t.Fatalf("candidateNames = %v, want [agy] off windows", got)
	}
}
