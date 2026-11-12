//go:build !windows

// Purpose: install a deterministic receive hook that checks its pinned executable.
// Constraints: no PATH lookup for the executable or digest utility.

package ci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	contextgit "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrWindowsNotSupported identifies platforms without receive-hook support.
var ErrWindowsNotSupported = cascade.New(cascade.KindUnavailable, "git gate: Windows is not supported")

// MirrorPush describes the last mirror attempt.
type MirrorPush struct {
	OK    bool      `json:"ok"`
	At    time.Time `json:"at"`
	Error string    `json:"error"`
}

// MirrorState is the mirror state supplied by the caller.
type MirrorState struct {
	Enabled        bool
	LastMirrorPush MirrorPush
	Backlog        int
}

// GitGateStatusResult reports the installed pin and supplied mirror state.
type GitGateStatusResult struct {
	HookInstalled  bool       `json:"hook_installed"`
	HookSHA256     string     `json:"hook_sha256"`
	BinaryPath     string     `json:"binary_path"`
	BinaryDrift    bool       `json:"binary_drift"`
	RepoID         string     `json:"repo_id"`
	RepoIDBound    bool       `json:"repo_id_bound"`
	MirrorEnabled  bool       `json:"mirror_enabled"`
	LastMirrorPush MirrorPush `json:"last_mirror_push"`
	Backlog        int        `json:"backlog"`
}

// String renders status through internal/output's result interface.
func (s GitGateStatusResult) String() string {
	return fmt.Sprintf("hook_installed=%t binary_path=%s binary_drift=%t repo_id=%s repo_id_bound=%t", s.HookInstalled, s.BinaryPath, s.BinaryDrift, s.RepoID, s.RepoIDBound)
}

// ComputeSHA256 hashes a file without loading the executable into memory.
func ComputeSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "git gate: open binary")
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "git gate: hash binary")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func gateHookPath(repo string) (string, error) {
	dirs, err := contextgit.GitCommonDir(context.Background(), repo)
	if err != nil {
		return "", err
	}
	return filepath.Join(dirs.CommonDir, "hooks", "pre-receive"), nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func renderGateHook(bin, digest string) []byte {
	utility := "/usr/bin/sha256sum"
	if runtime.GOOS == "darwin" {
		utility = "/usr/bin/shasum -a 256"
	}
	return []byte(fmt.Sprintf(`#!/bin/sh
set -eu
binary=%s
expected=%s
refuse() {
  printf 'git gate: binary changed; re-run cascade node git-gate install after self-update\n' >&2
  exit 1
}
actual=$(%s < "$binary") || refuse
actual=${actual%%%% *}
[ "$actual" = "$expected" ] || refuse
exec "$binary" node git-gate pre-receive
`, shellQuote(bin), shellQuote(digest), utility))
}

// GitGateInstall pins the absolute binary and digest. Controller callers bind
// the canonical directory through BindGateRepoID before invoking this function.
func GitGateInstall(repoPath, repoID, cascadeBin, binSHA256 string) error {
	if repoID == "" || !filepath.IsAbs(cascadeBin) || strings.ContainsAny(cascadeBin, "\r\n\x00") {
		return cascade.New(cascade.KindInvalidInput, "git gate: repository id and absolute binary path required")
	}
	digest, err := ComputeSHA256(cascadeBin)
	if err != nil {
		return err
	}
	if digest != binSHA256 {
		return cascade.New(cascade.KindIntegrity, "git gate: binary digest mismatch")
	}
	path, err := gateHookPath(repoPath)
	if err != nil {
		return err
	}
	script := renderGateHook(cascadeBin, digest)
	if err := BindGateRepoID(context.Background(), repoPath, repoID, nil); err != nil {
		return err
	}
	old, readErr := os.ReadFile(path)
	if readErr == nil && bytes.Equal(old, script) {
		// Preserve the rendered bytes while repairing an accidentally disabled hook.
		if err := os.Chmod(path, 0755); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "git gate: enable hook")
		}
		return nil
	}
	if readErr != nil && !os.IsNotExist(readErr) {
		return cascade.Wrap(cascade.KindUnavailable, readErr, "git gate: read hook")
	}
	return writeGateHook(path, script)
}

func writeGateHook(path string, script []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "git gate: create hooks directory")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pre-receive-*")
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "git gate: create hook")
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(script); err == nil {
		err = f.Chmod(0755)
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "git gate: install hook")
	}
	return nil
}

// GitGateStatus reads hook/config state without writing the repository.
// RepoIDBound stays false until the caller checks the controller registry.
func GitGateStatus(repoPath string, mirror MirrorState) (GitGateStatusResult, error) {
	s := GitGateStatusResult{MirrorEnabled: mirror.Enabled, LastMirrorPush: mirror.LastMirrorPush, Backlog: mirror.Backlog}
	path, err := gateHookPath(repoPath)
	if err != nil {
		return s, err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return s, cascade.Wrap(cascade.KindUnavailable, err, "git gate: read hook")
	}
	if err == nil {
		s.HookInstalled = true
		s.HookSHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
		bin, digest := hookPin(data)
		s.BinaryPath = bin
		actual, hashErr := ComputeSHA256(bin)
		s.BinaryDrift = hashErr != nil || actual != digest || !bytes.Equal(data, renderGateHook(bin, digest))
	}
	s.RepoID, err = GateRepoIDFromConfig(context.Background(), repoPath)
	if err == ErrGateRepoIDMissing {
		err = nil
	}
	return s, err
}

func hookPin(data []byte) (bin, digest string) {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "binary='") && strings.HasSuffix(line, "'") {
			bin = strings.ReplaceAll(line[8:len(line)-1], "'\"'\"'", "'")
		}
		if strings.HasPrefix(line, "expected='") && strings.HasSuffix(line, "'") {
			digest = line[10 : len(line)-1]
		}
	}
	return
}
