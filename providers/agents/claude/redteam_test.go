//go:build security && !windows

package claude

// Purpose: the R-21.151 red-team matrix (FILESYSTEM, ENVIRONMENT, PROCESS
//   TREE) plus the R-21.152 canary breadth, each leg driven by a leaking
//   fake harness so it goes red if the driver control behind it is removed.
// Constraints: build tag security. The vendor harness's own tool
//   subprocesses inherit its environment; the driver cannot hand them
//   SpawnEnv.Tool, so that single leg is marked pending PCI
//   s61t2-vendor-tool-subprocess-env-unenforceable rather than faked. SSE
//   frames and telemetry rows are produced by the AD/S-62.T3 host, not by
//   this driver; their canary search belongs to that ticket's matrix.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestClaudeRedTeamLeakMatrix(t *testing.T) {
	t.Run("FILESYSTEM", redTeamFilesystem)
	t.Run("ENVIRONMENT", redTeamEnvironment)
	t.Run("PROCESS_TREE", redTeamProcessTree)
	t.Run("CANARY_BREADTH", redTeamCanaryBreadth)
}

func redTeamFilesystem(t *testing.T) {
	t.Run("symlink into the vendor auth dir is refused", func(t *testing.T) {
		f := newFx(t)
		mustWrite(t, filepath.Join(f.vendor, "credentials"), "secret")
		mustSymlink(t, f.vendor, filepath.Join(f.root, "auth"))
		f.body = "cat auth/credentials\n" + f.body
		if _, err := f.driver(f.config()).Spawn(context.Background(), f.spec()); !errors.Is(err, ErrWorktreeEscape) || exists(f.anyRan) {
			t.Fatalf("Spawn = %v anyRan=%v: the auth dir was tool-reachable", err, exists(f.anyRan))
		}
	})
	t.Run("hook pack cannot be redirected outside the tree", func(t *testing.T) {
		f := newFx(t)
		mustMkdir(t, filepath.Join(f.root, ".claude"))
		mustSymlink(t, f.dir, filepath.Join(f.root, ".claude", "hooks"))
		if _, err := f.driver(f.config()).Spawn(context.Background(), f.spec()); !errors.Is(err, ErrWorktreeEscape) || exists(filepath.Join(f.dir, "h.sh")) {
			t.Fatalf("Spawn = %v: a hook file landed outside the tree", err)
		}
	})
	t.Run("harness file tools are denied the runtime home", func(t *testing.T) {
		f := newFx(t)
		if _, _, err := run(t, f.driver(f.config()), f.spec()); err != nil {
			t.Fatal(err)
		}
		argv := argvOf(t, f)
		for _, rule := range []string{"Read(/" + f.home + "/**)", "Edit(/" + f.home + "/**)", "Write(/" + f.home + "/**)"} {
			if !has(argv, rule) {
				t.Fatalf("argv lacks the deny rule %s: %q", rule, argv)
			}
		}
	})
	t.Run("a read of the auth area that reaches output fails the run", func(t *testing.T) {
		f := newFx(t)
		f.body = readCanary + `echo "$C"` + "\n" + f.body
		if _, _, err := run(t, f.driver(f.config()), f.spec()); !errors.Is(err, ErrCredentialCanary) {
			t.Fatalf("Collect = %v, want the run failed closed", err)
		}
	})
}

func redTeamEnvironment(t *testing.T) {
	t.Run("the trusted child environ is exactly SpawnEnv.Driver", func(t *testing.T) {
		f := newFx(t)
		out := filepath.Join(f.dir, "env")
		f.body = "env > " + out + "\n" + f.body
		t.Setenv("GITHUB_TOKEN", "daemon-only-secret")
		if _, _, err := run(t, f.driver(f.config()), f.spec()); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(out)
		if strings.Contains(string(b), "daemon-only-secret") || !strings.Contains(string(b), VendorConfigDirVar+"=") {
			t.Fatalf("child environ %s", b)
		}
	})
	t.Run("a tool environ carrying the vendor var is refused", func(t *testing.T) {
		f := newFx(t)
		spec := f.spec()
		spec.Env.Tool = append(spec.Env.Tool, VendorConfigDirVar+"="+f.vendor)
		if _, err := f.driver(f.config()).Spawn(context.Background(), spec); !errors.Is(err, ErrSpawnEnvRejected) || exists(f.anyRan) {
			t.Fatalf("Spawn = %v", err)
		}
	})
	t.Run("vendor tool subprocess environ", func(t *testing.T) {
		t.Skip("pending PCI s61t2-vendor-tool-subprocess-env-unenforceable: the harness passes its own " +
			"environment to its tool subprocesses and the driver has no mechanism to substitute SpawnEnv.Tool")
	})
}

func redTeamProcessTree(t *testing.T) {
	t.Run("a descendant left carrying the vendor var fails the run and dies", func(t *testing.T) {
		f := newFx(t)
		gp := filepath.Join(f.dir, "gpid")
		f.body = "sleep 30 >/dev/null 2>&1 </dev/null &\necho $! > " + gp + "\n" + f.body
		d := f.driver(f.config())
		res, _, err := run(t, d, f.spec())
		if !errors.Is(err, ErrVendorAuthEnvLeak) || state(t, d, res.JobID) != provider.AgentRunFailed {
			t.Fatalf("Collect = %v, want ErrVendorAuthEnvLeak", err)
		}
		if g := readPid(t, gp); alive(g) || !errors.Is(syscall.Kill(-res.ProcessGroupID, 0), syscall.ESRCH) {
			t.Fatal("a member of the recorded process group is still running")
		}
	})
	t.Run("a descendant without the vendor var is not a leak", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("darwin does not expose another process's environment; any straggler is unverifiable there")
		}
		f := newFx(t)
		f.body = "env -u " + VendorConfigDirVar + " -u " + KeyVarOAuthToken + " sleep 30 >/dev/null 2>&1 </dev/null &\n" + f.body
		if _, _, err := run(t, f.driver(f.config()), f.spec()); err != nil {
			t.Fatalf("Collect = %v: a stripped straggler was reported as a leak", err)
		}
	})
	t.Run("a descendant carrying only the key var is a leak", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("darwin flags any straggler, so this leg proves nothing there; the linux run does")
		}
		f := newFx(t)
		f.body = "env -u " + VendorConfigDirVar + " sleep 30 >/dev/null 2>&1 </dev/null &\n" + f.body
		if _, _, err := run(t, f.driver(f.config()), f.spec()); !errors.Is(err, ErrVendorAuthEnvLeak) {
			t.Fatalf("Collect = %v, want ErrVendorAuthEnvLeak for a straggler holding the key variable", err)
		}
	})
}

func redTeamCanaryBreadth(t *testing.T) {
	leaks := map[string]string{
		"stdout as a tool-shaped line": `printf '{"type":"tool","name":"%s"}\n' "$C"`,
		"logs on stderr as hex":        `printf '%s' "$C" | od -An -tx1 | tr -d ' \n' >&2`,
		"artifacts as a file name":     `touch "$C"`,
		"artifacts as file content":    `printf '%s' "$C" > canary-seen`,
		"result as base64":             `printf 'ab%s' "$C" | base64`,
	}
	for name, leak := range leaks {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.body = readCanary + leak + "\n" + f.body
			_, cr, err := run(t, f.driver(f.config()), f.spec())
			if err != ErrCredentialCanary || cr.Output != "" || strings.Contains(err.Error(), "cascade-canary-") {
				t.Fatalf("Collect = %v (output %q): a leaked canary must fail closed and stay out of the error", err, cr.Output)
			}
		})
	}
	t.Run("prompts and journal of a clean run", func(t *testing.T) {
		f := newFx(t)
		oracle := filepath.Join(f.dir, "canary-seen") // outside the tree: the test's own copy
		f.body = readCanary + `printf '%s' "$C" > ` + oracle + "\nprintf 'plain\\n' > made.txt\n" + f.body
		_, cr, err := run(t, f.driver(f.config()), f.spec())
		if err != nil {
			t.Fatal(err)
		}
		seen, _ := os.ReadFile(oracle)
		forms := canaryForms(string(seen))
		if len(seen) == 0 || containsCanary([]byte(strings.Join(argvOf(t, f), "\n")), forms) ||
			containsCanary([]byte(cr.Output), forms) || containsCanary([]byte(strings.Join(cr.Artifacts, "\n")), forms) {
			t.Fatal("the canary reached the prompt argv, the journal or the artifacts")
		}
	})
}
