//go:build capmap

package capmap

import (
	"os"
	"strings"
	"testing"
)

func init() {
	register("TestCapmap_RpcDaemonApiStatus", probeRPCDaemonStatus)
	register("TestCapmap_CliOutputEnvelope", probeCLIOutputEnvelope)
}

// probeRPCDaemonStatus proves the daemon answers on its owner-only socket.
// Authorization: an owner call is served, a browser-shaped call gets HTTP
// 403. Routing: the daemon's own manifest reports rpc-registry and
// ipc-socket running on that socket. Side effect: the socket file exists,
// is not group or world accessible, and is gone after stop. Result: the
// status envelope carries a pid and health ok.
func probeRPCDaemonStatus(t *testing.T) {
	c := newCLI(t)
	if _, up := c.status(); up {
		t.Fatal("status answers before the daemon was started")
	}
	st := c.startDaemon()
	sock := st.Daemon.SocketPath
	if st.Health != "ok" || st.Daemon.PID <= 0 || sock == "" {
		t.Fatalf("status = %+v, want health ok, a pid and a socket path", st)
	}
	seen := map[string]string{}
	for _, s := range st.Subsystems {
		seen[s.Name] = s.State
		if s.Name == "ipc-socket" && s.Detail != sock {
			t.Fatalf("ipc-socket detail %q, want the socket path %q", s.Detail, sock)
		}
	}
	for _, name := range []string{"rpc-registry", "ipc-socket"} {
		if seen[name] != "running" {
			t.Fatalf("subsystem %s is %q, want running (manifest %v)", name, seen[name], seen)
		}
	}
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("socket %s: info %v err %v, want an owner-only socket", sock, fi, err)
	}
	mustRPC(t, sock, "memory.list", map[string]any{})
	mustRefuseBrowser(t, sock, "memory.list", map[string]any{})
	c.stopDaemon()
	if _, err := os.Stat(sock); err == nil {
		t.Fatalf("socket %s still exists after daemon stop", sock)
	}
}

// probeCLIOutputEnvelope proves the --json contract on the built binary.
// Authorization: an elevated verb refused in daemonless mode surfaces as a
// typed elevation-required error, not as free text. Routing: stdout holds
// exactly one versioned envelope and the error code is the frozen
// taxonomy code for its kind. Side effect: the process exit code is the
// taxonomy exit code for that kind, and success exits zero. Result: the ok
// envelope of a read verb carries data.
func probeCLIOutputEnvelope(t *testing.T) {
	c := newCLI(t)
	cases := []struct {
		args       []string
		kind       string
		code, exit int
	}{
		{[]string{"status", "--json"}, "unavailable", -32004, 5},
		{[]string{"memory", "forget", "nosuch", "--json"}, "invalid-input", -32002, 2},
	}
	for _, tc := range cases {
		r := c.run("", tc.args...)
		e := decodeEnvelope(t, r)
		if e.Version != 1 || e.OK || e.Error == nil || e.Error.Kind != tc.kind || e.Error.Code != tc.code || r.code != tc.exit {
			t.Fatalf("%v: envelope %+v exit %d, want kind %s code %d exit %d", tc.args, e, r.code, tc.kind, tc.code, tc.exit)
		}
	}
	c.mustOK("s3cret-capmap-value", "vault", "set", "gate1")
	r := c.run("", "vault", "get", "gate1", "--json")
	e := decodeEnvelope(t, r)
	if e.OK || e.Error == nil || e.Error.Kind != "elevation-required" || e.Error.Code != -32008 || r.code != 8 {
		t.Fatalf("vault get: envelope %+v exit %d, want elevation-required -32008 exit 8", e, r.code)
	}
	if strings.Contains(r.stdout, "s3cret-capmap-value") || strings.Contains(r.stderr, "s3cret-capmap-value") {
		t.Fatal("a refused vault get leaked the secret")
	}
	ok := decodeEnvelope(t, c.mustOK("", "memory", "list", "--json"))
	if ok.Version != 1 || !ok.OK || ok.Error != nil || len(ok.Data) == 0 {
		t.Fatalf("memory list envelope = %+v, want ok with data", ok)
	}
}
