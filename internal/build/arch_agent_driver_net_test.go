package build

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchAgentDriverNetSurface_Seeded(t *testing.T) {
	// Each seeded fixture must yield a violation naming its own surface.
	want := map[string]string{
		"http": "forbidden import net/http", "dial": "forbidden net surface Dial",
		"tcp": "forbidden net surface Listen", "variable": "forbidden net surface Listen",
		"alias": "net import must not have an alias", "dot": "net import must not have an alias",
		"tls": "forbidden import crypto/tls", "syscall": "forbidden import syscall",
		"jsonrpc": "forbidden import net/rpc/jsonrpc", "textproto": "forbidden import net/textproto",
		"syslog": "forbidden import log/syslog", "xsys": "forbidden import golang.org/x/sys/windows",
		"ssh": "forbidden import golang.org/x/crypto/ssh", "cgo": "forbidden import C", "unix": "",
		"module": "forbidden import github.com/jackc/pgx/v5", "unsafe": "forbidden import unsafe",
		"plugin":   "forbidden import plugin",
		"dotless":  "forbidden import evildial/dial",
		"testdata": "import of an unscanned directory github.com/acamarata/cascade/providers/agents/x/testdata/sock",
		"dotdir":   "import of an unscanned directory github.com/acamarata/cascade/providers/agents/x/.hidden",
		"casefold": "import of an unscanned directory github.com/acamarata/cascade/providers/agents/x/TestData/sock",
	}
	for name, what := range want {
		t.Run(name, func(t *testing.T) {
			root := agentNetFixture(t, name)
			got, err := ScanAgentDriverNetSurface(root)
			if err != nil {
				t.Fatal(err)
			}
			if (len(got) > 0) != (what != "") {
				t.Fatalf("%s violations = %v", name, got)
			}
			found := what == ""
			for _, v := range got {
				if v.File != "providers/agents/fixture/callback.go" || v.Line < 1 || v.What == "" {
					t.Fatalf("incomplete violation: %+v", v)
				}
				found = found || v.What == what
			}
			if !found {
				t.Fatalf("%s violations = %v, want %q", name, got, what)
			}
		})
	}
}

func TestArchAgentDriverNetSurface_RealTree(t *testing.T) {
	for _, root := range []string{archModuleRoot(t), agentNetFixture(t, "callback")} {
		got, err := ScanAgentDriverNetSurface(root)
		if err != nil || len(got) != 0 {
			t.Fatalf("%s: violations=%v error=%v", root, got, err)
		}
	}
	// An ignored file still makes the real-tree gate red.
	root := agentNetFixture(t, "http")
	if got, err := ScanAgentDriverNetSurface(root); err != nil || len(got) == 0 {
		t.Fatalf("empty scanner passes the real-tree gate: %v, %v", got, err)
	}
}

func TestArchAgentDriverNetSurface_AssemblyFile(t *testing.T) {
	root := agentNetFixture(t, "unix")
	asm := filepath.Join(root, "providers", "agents", "fixture", "raw_amd64.s")
	if err := os.WriteFile(asm, []byte("TEXT ·raw(SB),0,$0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ScanAgentDriverNetSurface(root)
	if err != nil || len(got) != 1 || got[0].What != "forbidden assembly or object file" {
		t.Fatalf("violations=%v error=%v, want the assembly file", got, err)
	}
}

func agentNetFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "agentdrivernet", name+".go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "providers", "agents", "fixture")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "callback.go"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestArchAgentDriverNetSurface_FoldedStdImports(t *testing.T) {
	// A case-folded std path resolves on a case-insensitive disk.
	want := map[string][]string{
		"syslogfold": {"non-canonical import spelling Log/syslog", "forbidden import Log/syslog"},
		"smtpfold":   {"non-canonical import spelling Net/smtp", "forbidden import Net/smtp"},
	}
	for name, whats := range want {
		got, err := ScanAgentDriverNetSurface(agentNetFixture(t, name))
		if err != nil || len(got) != len(whats) {
			t.Fatalf("%s: violations=%v error=%v", name, got, err)
		}
		for i, v := range got {
			if v.What != whats[i] {
				t.Fatalf("%s: violation %d = %q, want %q", name, i, v.What, whats[i])
			}
		}
	}
}

func TestArchAgentDriverNetSurface_Symlink(t *testing.T) {
	root := agentNetFixture(t, "callback")
	hidden := filepath.Join(root, "providers", "agents", ".hidden")
	if err := os.MkdirAll(hidden, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../.hidden", filepath.Join(root, "providers", "agents", "fixture", "hid")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, err := ScanAgentDriverNetSurface(root)
	want := AgentDriverNetViolation{File: "providers/agents/fixture/hid", What: "symlink in the agent driver tree"}
	if err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("violations=%v error=%v, want %v", got, err, want)
	}
}
