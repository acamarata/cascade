//go:build integration && !windows

// Purpose: prove the installed claude binary still carries the two prompt
//
//	flags the generated policy kernel relies on, by reading its bytes.
//
// Inputs: the claude binary found on PATH (never executed).
// Constraints: the test skips when claude is not installed.
// SPORT: cmd/cascade claude kernel flags (TEST).
package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
)

// kernelFlagScanChunk is the read size when scanning a binary for flags.
const kernelFlagScanChunk = 4 << 20

// binaryHasFlags reports, per flag, whether any byte run of the file at
// path contains it. Chunks overlap so a flag split across a read boundary
// is still found.
func binaryHasFlags(path string, flags []string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	maxLen := 0
	for _, fl := range flags {
		maxLen = max(maxLen, len(fl))
	}
	found := map[string]bool{}
	buf := make([]byte, kernelFlagScanChunk+maxLen)
	keep := 0
	for {
		n, rerr := io.ReadFull(f, buf[keep:])
		window := buf[:keep+n]
		for _, fl := range flags {
			if bytes.Contains(window, []byte(fl)) {
				found[fl] = true
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				return found, nil
			}
			return nil, rerr
		}
		keep = min(maxLen-1, len(window))
		copy(buf, window[len(window)-keep:])
	}
}

func TestBinaryHasFlagsFindsSplitFlag(t *testing.T) {
	flag := "--append-system-prompt"
	pad := bytes.Repeat([]byte{'x'}, kernelFlagScanChunk+8-5)
	data := append(append(pad, []byte(flag)...), []byte("tail")...)
	p := t.TempDir() + "/bin"
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := binaryHasFlags(p, []string{flag, "--absent-flag"})
	if err != nil || !got[flag] || got["--absent-flag"] {
		t.Fatalf("binaryHasFlags = %v, %v; want only %s found", got, err, flag)
	}
}

func TestInstalledClaudeHasKernelPromptFlags(t *testing.T) {
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude is not installed: %v", err)
	}
	flags := []string{"--append-system-prompt", "--append-subagent-system-prompt"}
	got, err := binaryHasFlags(path, flags)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, fl := range flags {
		if !got[fl] {
			t.Errorf("%s does not contain %s", path, fl)
		}
	}
}
