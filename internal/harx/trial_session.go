// Package harx provides trial session management for sandbox execution.
package harx

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
)

// Lane represents an execution lane identifier.
type Lane string

// TrialFile represents a file to be included in a trial.
type TrialFile struct {
	// Path is the relative path of the file.
	Path string
	// Content is the file content.
	Content string
}

// TrialRequest describes a request to launch a trial.
type TrialRequest struct {
	// Lane is the execution lane.
	Lane Lane
	// SessionDir is the requested session directory.
	SessionDir string
	// Prompt is the trial prompt.
	Prompt string
	// Env holds environment variables.
	Env map[string]string
	// Files lists files to include.
	Files []TrialFile
	// CheckerPath is the path to the checker binary.
	CheckerPath string
	// CheckerSource is the checker source code.
	CheckerSource string
	// CheckerOutput is the expected checker output.
	CheckerOutput string
}

// SandboxRequest is the request sent to a SandboxRunner.
type SandboxRequest struct {
	// Lane is the execution lane.
	Lane Lane
	// SessionDir is the session directory.
	SessionDir string
	// Prompt is the prompt.
	Prompt string
	// Env holds environment variables.
	Env map[string]string
	// Files lists files to include.
	Files []TrialFile
	// Sandbox indicates sandbox mode.
	Sandbox bool
	// DeleteAfter indicates cleanup after run.
	DeleteAfter bool
}

// TrialSession represents the result of a launched trial.
type TrialSession struct {
	// SessionDir is the session directory.
	SessionDir string
	// DeleteAfter indicates whether to delete after use.
	DeleteAfter bool
}

// SandboxRunner executes sandbox requests.
type SandboxRunner interface {
	// Run executes the sandbox request and returns a session.
	Run(ctx context.Context, req SandboxRequest) (TrialSession, error)
}

// ErrCheckerVisible is returned when checker internals are exposed.
var ErrCheckerVisible = errors.New("CHECKER_VISIBLE")

// ErrInvalidTrial is returned when trial parameters are invalid.
var ErrInvalidTrial = errors.New("invalid trial")

// LaunchTrial validates the request and delegates to the runner.
func LaunchTrial(ctx context.Context, runner SandboxRunner, req TrialRequest) (TrialSession, error) {
	if ctx == nil {
		return TrialSession{}, ErrInvalidTrial
	}
	if runner == nil {
		return TrialSession{}, ErrInvalidTrial
	}
	v := reflect.ValueOf(runner)
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		if v.IsNil() {
			return TrialSession{}, ErrInvalidTrial
		}
	}

	lane := string(req.Lane)
	if lane == "" {
		return TrialSession{}, ErrInvalidTrial
	}
	for _, c := range lane {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return TrialSession{}, ErrInvalidTrial
		}
	}

	sd := filepath.ToSlash(filepath.Clean(req.SessionDir))
	if sd == "." || sd == ".." || strings.HasPrefix(sd, "/") || strings.HasPrefix(sd, "../") || strings.Contains(sd, "/../") || strings.HasSuffix(sd, "/..") {
		return TrialSession{}, ErrInvalidTrial
	}

	if req.CheckerPath != "" {
		cp := filepath.ToSlash(filepath.Clean(req.CheckerPath))
		if cp == "." || cp == ".." || strings.HasPrefix(cp, "/") || strings.HasPrefix(cp, "../") || strings.Contains(cp, "/../") || strings.HasSuffix(cp, "/..") {
			return TrialSession{}, ErrInvalidTrial
		}
	}

	seen := make(map[string]bool)
	for _, f := range req.Files {
		fp := filepath.ToSlash(filepath.Clean(f.Path))
		if fp == "." || fp == ".." || strings.HasPrefix(fp, "/") || strings.HasPrefix(fp, "../") || strings.Contains(fp, "/../") || strings.HasSuffix(fp, "/..") {
			return TrialSession{}, ErrInvalidTrial
		}
		if seen[fp] {
			return TrialSession{}, ErrInvalidTrial
		}
		seen[fp] = true
	}

	sreq := SandboxRequest{
		Lane:        req.Lane,
		SessionDir:  sd,
		Prompt:      req.Prompt,
		Env:         req.Env,
		Files:       req.Files,
		Sandbox:     true,
		DeleteAfter: true,
	}

	_, err := runner.Run(ctx, sreq)
	if err != nil {
		return TrialSession{}, err
	}

	return TrialSession{
		SessionDir:  sd,
		DeleteAfter: true,
	}, nil
}
