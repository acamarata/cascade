//go:build !windows

package secrets

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeHelper(t *testing.T, body string) HelperSpec {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	p := filepath.Join(dir, "program")
	data := []byte("#!/bin/sh\n" + body + "\n")
	if err := os.WriteFile(p, data, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return HelperSpec{Path: p, SHA256: fmt.Sprintf("%x", digest), Home: dir, User: "test-user", Timeout: time.Second}
}

func helperCanary(t *testing.T) string {
	t.Helper()
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", b)
}

func assertHelperError(t *testing.T, err, want error, path string) {
	t.Helper()
	// Unwrap by identity: cascade errors compare kinds in their Is method.
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == want {
			if !strings.Contains(err.Error(), want.Error()) || !strings.Contains(err.Error(), path) {
				t.Fatal("missing error context")
			}
			return
		}
	}
	t.Fatalf("expected sentinel %v, got %v", want, err)
}

func TestHelperRunsPinnedExecutable(t *testing.T) {
	token := helperCanary(t)
	spec := fakeHelper(t, "printf '%s\\n' '"+token+"'")
	b, err := RunCredentialHelper(context.Background(), spec)
	if err != nil || string(b) != token {
		t.Fatalf("pinned helper failed: %v", err)
	}
}

func TestHelperRefusesDigestMismatch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	spec := fakeHelper(t, "touch '"+marker+"'; printf x")
	f, err := os.OpenFile(spec.Path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = RunCredentialHelper(context.Background(), spec)
	assertHelperError(t, err, ErrHelperChanged, spec.Path)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("changed helper ran")
	}
}

func TestHelperRefusesUnsafeFile(t *testing.T) {
	for _, mode := range []string{"relative", "group", "world", "parent", "directory", "dangling"} {
		t.Run(mode, func(t *testing.T) {
			spec := fakeHelper(t, "printf x")
			var err error
			switch mode {
			case "relative":
				spec.Path = "program"
			case "group":
				err = os.Chmod(spec.Path, 0o720)
			case "world":
				err = os.Chmod(spec.Path, 0o702)
			case "parent":
				err = os.Chmod(filepath.Dir(spec.Path), 0o720)
			case "directory":
				spec.Path = filepath.Dir(spec.Path)
			case "dangling":
				p := filepath.Join(filepath.Dir(spec.Path), "link")
				err = os.Symlink(p+"-absent", p)
				spec.Path = p
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = RunCredentialHelper(context.Background(), spec)
			assertHelperError(t, err, ErrHelperUnsafe, spec.Path)
		})
	}
}

func TestHelperOutputRules(t *testing.T) {
	cases := []struct{ name, body string }{
		{"empty", "exit 0"}, {"two lines", "printf 'x\\ny\\n'"},
		{"oversize", "i=0; while [ $i -lt 4097 ]; do printf x; i=$((i+1)); done"},
		{"nul", "printf 'x\\000y'"}, {"cr", "printf 'x\\r'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := fakeHelper(t, c.body)
			_, err := RunCredentialHelper(context.Background(), spec)
			assertHelperError(t, err, ErrHelperOutput, spec.Path)
		})
	}
	t.Run("one newline", TestHelperRunsPinnedExecutable)
}

func TestHelperOutputUnbounded(t *testing.T) {
	spec := fakeHelper(t, "exec /usr/bin/yes")
	spec.Timeout = 2 * time.Second
	started := time.Now()
	_, err := RunCredentialHelper(context.Background(), spec)
	assertHelperError(t, err, ErrHelperOutput, spec.Path)
	if time.Since(started) >= spec.Timeout {
		t.Fatal("overflow waited for timeout")
	}
}

func TestHelperExecsResolvedTarget(t *testing.T) {
	spec := fakeHelper(t, "printf '%s' \"$0\"")
	target := spec.Path
	link := filepath.Join(filepath.Dir(target), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	spec.Path = link
	// The temp dir itself may sit under a symlink (/tmp on darwin when
	// TMPDIR is unset), so the expected exec path is the fully resolved one.
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RunCredentialHelper(context.Background(), spec)
	if err != nil || string(b) != resolved || resolved == link {
		t.Fatalf("did not execute resolved target: %q, %v", b, err)
	}
}

func TestHelperTimeoutKillsGroup(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "pid")
	spec := fakeHelper(t, "sleep 30 &\nchild=$!\nprintf '%s' \"$child\" > '"+pidPath+"'\nwait")
	spec.Timeout = 200 * time.Millisecond
	_, err := RunCredentialHelper(context.Background(), spec)
	assertHelperError(t, err, ErrHelperFailed, spec.Path)
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
		// A killed orphan can await reaping by the container's init process.
		state, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper child still exists")
}

func TestHelperEnvMinimal(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "environment")
	canary := helperCanary(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", canary)
	spec := envHelper(t, dest)
	if _, err := RunCredentialHelper(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		got[k] = v
	}
	want := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + spec.Home, "USER=" + spec.User, "LOGNAME=" + spec.User, "LANG=C.UTF-8"}
	if len(got) != len(want) {
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		t.Fatalf("unexpected environment variable names: %v", keys)
	}
	for _, line := range want {
		k, v, _ := strings.Cut(line, "=")
		if got[k] != v {
			t.Fatalf("wrong environment for %s", k)
		}
	}
	if strings.Contains(string(data), canary) {
		t.Fatal("inherited environment")
	}
}

func envHelper(t *testing.T, dest string) HelperSpec {
	t.Helper()
	spec := fakeHelper(t, "printf x")
	spec.Path += "-binary"
	source := filepath.Join(filepath.Dir(spec.Path), "main.go")
	body := fmt.Sprintf("package main\nimport (\"os\"; \"strings\")\nfunc main(){os.WriteFile(%q, []byte(strings.Join(os.Environ(), \"\\n\")),0600); os.Stdout.Write([]byte(\"x\"))}\n", dest)
	if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", spec.Path, source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test executable: %v: %s", err, out)
	}
	data, err := os.ReadFile(spec.Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	spec.SHA256 = fmt.Sprintf("%x", sum)
	return spec
}

func TestHelperErrorsCarryNoOutput(t *testing.T) {
	canary := helperCanary(t)
	spec := fakeHelper(t, "printf '%s' '"+canary+"'; printf '%s' '"+canary+"' >&2; exit 3")
	_, err := RunCredentialHelper(context.Background(), spec)
	assertHelperError(t, err, ErrHelperFailed, spec.Path)
	if strings.Contains(err.Error(), canary) || !strings.Contains(err.Error(), "exit 3") {
		t.Fatal("unsafe exit error")
	}
}
