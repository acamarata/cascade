//go:build windows

// Purpose: refuse hook installation on Windows before touching any file.
package ci

import (
	"time"

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

// GitGateStatusResult reports receive-hook and mirror state.
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

// String renders the platform refusal.
func (GitGateStatusResult) String() string { return ErrWindowsNotSupported.Error() }

// ComputeSHA256 refuses unsupported hook operations on Windows.
func ComputeSHA256(string) (string, error) { return "", ErrWindowsNotSupported }

// GitGateInstall refuses before opening a repository or binary.
func GitGateInstall(string, string, string, string) error { return ErrWindowsNotSupported }

// GitGateStatus refuses unsupported hook operations on Windows.
func GitGateStatus(string, MirrorState) (GitGateStatusResult, error) {
	return GitGateStatusResult{}, ErrWindowsNotSupported
}
