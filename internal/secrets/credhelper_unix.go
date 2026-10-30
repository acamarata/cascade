//go:build !windows

package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func runCredentialHelper(ctx context.Context, spec HelperSpec) ([]byte, error) {
	if !filepath.IsAbs(spec.Path) {
		return nil, helperError(ErrHelperUnsafe, spec.Path, "exit not started")
	}
	resolved, err := filepath.EvalSymlinks(spec.Path)
	if err != nil {
		return nil, helperError(ErrHelperUnsafe, spec.Path, "exit not started")
	}
	if checkSafePath(resolved) != nil || safeOwnerMode(filepath.Dir(spec.Path), true) != nil {
		return nil, helperError(ErrHelperUnsafe, spec.Path, "exit not started")
	}
	digest, err := hashFile(resolved)
	if err != nil || digest != spec.SHA256 {
		return nil, helperError(ErrHelperChanged, spec.Path, "exit not started")
	}
	return execHelper(ctx, resolved, spec)
}

func helperError(kind error, path, status string) error {
	return fmt.Errorf("%w: %s: %s", kind, path, status)
}

func checkSafePath(path string) error {
	if err := safeOwnerMode(path, false); err != nil {
		return err
	}
	return safeOwnerMode(filepath.Dir(path), true)
}

func safeOwnerMode(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return ErrHelperUnsafe
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return ErrHelperUnsafe
	}
	if info.Mode().Perm()&0o022 != 0 {
		return ErrHelperUnsafe
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 && st.Uid != uint32(os.Geteuid()) {
		return ErrHelperUnsafe
	}
	if !directory && info.Mode().Perm()&0o111 == 0 {
		return ErrHelperUnsafe
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read-only file; nothing to flush
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func execHelper(ctx context.Context, path string, spec HelperSpec) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, spec.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = buildMinimalEnv(spec.Home, spec.User)
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, helperError(ErrHelperFailed, spec.Path, "exit not started")
	}
	defer func() { _ = pr.Close() }() // read end of a pipe; nothing to flush
	cmd.Stdout = pw
	err = cmd.Start()
	_ = pw.Close()
	if err != nil {
		return nil, helperError(ErrHelperFailed, spec.Path, "exit not started")
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }() // best-effort reap; the group may already be gone
	raw, readErr := readHelperOutput(ctx, pr)
	overflow := len(raw) > maxHelperOutput
	if overflow || readErr != nil {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	defer clear(raw)
	if overflow {
		return nil, helperError(ErrHelperOutput, spec.Path, "exit output limit")
	}
	if ctx.Err() != nil {
		return nil, helperError(ErrHelperFailed, spec.Path, "exit timeout")
	}
	if waitErr != nil || readErr != nil {
		return nil, helperError(ErrHelperFailed, spec.Path, exitStatus(waitErr))
	}
	return validateOutput(raw, spec.Path)
}

func readHelperOutput(ctx context.Context, r *os.File) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		b, err := io.ReadAll(io.LimitReader(r, maxHelperOutput+1))
		done <- result{b, err}
	}()
	select {
	case v := <-done:
		return v.data, v.err
	case <-ctx.Done():
		_ = r.Close()
		v := <-done
		return v.data, ctx.Err()
	}
}

func exitStatus(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("exit %d", ee.ExitCode())
	}
	return "exit failed"
}

func buildMinimalEnv(home, user string) []string {
	return []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + home, "USER=" + user, "LOGNAME=" + user, "LANG=C.UTF-8"}
}

func validateOutput(raw []byte, path string) ([]byte, error) {
	if len(raw) > maxHelperOutput || bytes.ContainsAny(raw, "\x00\r") {
		return nil, helperError(ErrHelperOutput, path, "exit 0")
	}
	line := bytes.TrimSuffix(raw, []byte("\n"))
	if len(line) == 0 || bytes.ContainsRune(line, '\n') {
		return nil, helperError(ErrHelperOutput, path, "exit 0")
	}
	return bytes.Clone(line), nil
}
