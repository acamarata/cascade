package runtime

import (
	"os"
	"strings"
	"testing"
)

// Purpose: Art.5 platform-parity proof for the SIGHUP handler split
//   (hotreload_signal.go / hotreload_signal_windows.go): asserts the
//   Windows file structurally carries no signal-handling code (rather
//   than merely trusting the build tag), and that GOOS=windows /
//   GOOS=linux both actually compile — the two checks this ticket's
//   contract calls out by name ("SIGHUP handler is absent, build tag
//   asserted"; "GOOS=linux/windows go build passes for all new
//   packages").
// Constraints: this test itself runs on the host GOOS (unconditionally —
//   no build tag), reading both platform files' source as text; it does
//   not attempt to build for windows itself (that is this ticket's
//   `checks` list, run via the real go binary, not a Go test).

func TestRegisterSIGHUP_WindowsFileHasNoSignalHandling(t *testing.T) {
	data, err := os.ReadFile("hotreload_signal_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if !strings.Contains(src, "//go:build windows") {
		t.Fatal("hotreload_signal_windows.go must carry the windows build tag")
	}
	code := stripCommentLines(src)
	// The exported func name RegisterSIGHUP legitimately contains
	// "SIGHUP" (it mirrors the !windows file's API) — what must be
	// structurally absent is any actual signal registration: the
	// "os/signal"/"syscall" imports and a syscall.SIGHUP reference.
	for _, forbidden := range []string{`"os/signal"`, `"syscall"`, "syscall.SIGHUP", "signal.Notify"} {
		if strings.Contains(code, forbidden) {
			t.Fatalf("hotreload_signal_windows.go's CODE (not comments) must not reference %q — the SIGHUP handler must be structurally absent on Windows, not merely a runtime no-op", forbidden)
		}
	}
}

// stripCommentLines removes every line-comment line from src (this file's
// own package is gofmt-formatted, so every doc-comment line starts with
// "//" once trimmed), so a platform-parity check can assert on real code
// shape without false-triggering on prose that happens to name the very
// symbols it is proving absent.
func stripCommentLines(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestRegisterSIGHUP_UnixFileCarriesRealHandler(t *testing.T) {
	data, err := os.ReadFile("hotreload_signal.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if !strings.Contains(src, "//go:build !windows") {
		t.Fatal("hotreload_signal.go must carry the !windows build tag")
	}
	if !strings.Contains(src, "syscall.SIGHUP") {
		t.Fatal("hotreload_signal.go must register a real SIGHUP handler")
	}
}

func TestRegisterSIGHUP_HostPlatformWorks(t *testing.T) {
	hr, _, _, _, _ := newTestHotReloader(t, "[conductor]\nexternal_routing_enabled = false\n")
	stop := RegisterSIGHUP(hr)
	defer stop()
	// No panic / hang on register+stop is the contract on every non-test
	// GOOS this test itself runs under (darwin/linux in CI); the
	// Windows-specific no-op variant is proven structurally above since
	// this test file has no build tag and therefore never runs a real
	// signal.Notify call on a windows GOOS build (that symbol does not
	// exist in hotreload_signal_windows.go).
}
