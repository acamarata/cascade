package workflows

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

type fakeEnv struct {
	exists   map[string]bool
	runErr   error
	runCalls []runCall
}

type runCall struct {
	name string
	args []string
}

func (f *fakeEnv) Exists(path string) bool {
	return f.exists[path]
}

func (f *fakeEnv) Run(name string, args ...string) error {
	f.runCalls = append(f.runCalls, runCall{name, args})
	return f.runErr
}

func TestEnsureVenvIdempotent(t *testing.T) {
	home := "/tmp/testdata"
	python := "/opt/homebrew/bin/python3"
	target := filepath.Join(home, "pytools")

	t.Run("existing complete target returns unchanged path with no Run/output", func(t *testing.T) {
		env := &fakeEnv{
			exists: map[string]bool{
				target:                                 true,
				filepath.Join(target, "pyvenv.cfg"):    true,
				filepath.Join(target, "bin", "python"): true,
				filepath.Join(target, "bin", "pip"):    true,
			},
		}
		out := &bytes.Buffer{}
		got, err := EnsureVenv(home, python, false, env, out)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != target {
			t.Fatalf("expected %q, got %q", target, got)
		}
		if len(env.runCalls) != 0 {
			t.Fatalf("expected no Run calls, got %v", env.runCalls)
		}
		if out.Len() != 0 {
			t.Fatalf("expected no output, got %q", out.String())
		}
	})

	t.Run("incomplete target matches ErrVenvMissing", func(t *testing.T) {
		env := &fakeEnv{
			exists: map[string]bool{
				target: true,
			},
		}
		out := &bytes.Buffer{}
		_, err := EnsureVenv(home, python, false, env, out)
		if !errors.Is(err, ErrVenvMissing) {
			t.Fatalf("expected ErrVenvMissing, got %v", err)
		}
	})

	t.Run("malformed home matches ErrVenvInstall", func(t *testing.T) {
		env := &fakeEnv{exists: map[string]bool{}}
		out := &bytes.Buffer{}
		_, err := EnsureVenv("relative/path", python, false, env, out)
		if !errors.Is(err, ErrVenvInstall) {
			t.Fatalf("expected ErrVenvInstall, got %v", err)
		}
	})

	t.Run("invalid python matches ErrVenvInstall", func(t *testing.T) {
		env := &fakeEnv{exists: map[string]bool{}}
		out := &bytes.Buffer{}
		_, err := EnsureVenv(home, "/usr/local/bin/python3", false, env, out)
		if !errors.Is(err, ErrVenvInstall) {
			t.Fatalf("expected ErrVenvInstall, got %v", err)
		}
	})

	t.Run("fake creation failure matches ErrVenvInstall", func(t *testing.T) {
		env := &fakeEnv{
			exists: map[string]bool{},
			runErr: errors.New("boom"),
		}
		out := &bytes.Buffer{}
		_, err := EnsureVenv(home, python, false, env, out)
		if !errors.Is(err, ErrVenvInstall) {
			t.Fatalf("expected ErrVenvInstall, got %v", err)
		}
	})

	t.Run("fresh creation succeeds and writes only target", func(t *testing.T) {
		env := &fakeEnv{
			exists: map[string]bool{},
		}
		out := &bytes.Buffer{}
		got, err := EnsureVenv(home, python, false, env, out)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != target {
			t.Fatalf("expected %q, got %q", target, got)
		}
		if len(env.runCalls) != 2 {
			t.Fatalf("expected 2 Run calls, got %v", env.runCalls)
		}
	})
}
