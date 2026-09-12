//go:build integration

// Purpose: Art.2's real-counterpart proof for the chunked transfer — the
//   wire framing (chunk.go) and the transfer loop (transfer.go) driven
//   end to end against a REAL /usr/sbin/sshd on loopback, over
//   internal/nodes' EXISTING ExecDialer/ExecSession (S-36.T5's real ssh
//   transport, reused as-is — no new dialer, no new ssh handshake code
//   anywhere in this file). Mirrors internal/nodes' own
//   provision_integration_test.go/tunnel_test.go real-sshd precedent
//   (same spawn shape, independently reproduced here since the helper is
//   unexported in that package).
// SPORT: internal/sync TestSyncChunkedTransferRealSSHD (P1-E17-W4-S38-T1).

package sync

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/acamarata/cascade/internal/nodes"
)

// TestSyncChunkedTransferRealSSHD proves the chunked transfer against a
// real sshd: each frame (chunk.go's Encode) is written to a remote file
// over a REAL ssh connection (nodes.NewSSHExecDialer/ExecSession,
// S-36.T5's production transport, unmodified), then read back through a
// REAL remote `cat` execution — genuine round-trip bytes through sshd,
// not a self-authored dialect. It also proves the resumable model: a
// transfer interrupted after N chunks lands only a partial remote file,
// and a second, resumed transfer starting at chunk N completes it without
// re-sending or silently dropping the first N.
func TestSyncChunkedTransferRealSSHD(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	port, hostFingerprint := spawnLoopbackSSHDForSync(t, clientPub)

	signer, err := ssh.NewSignerFromKey(clientPriv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	dialer := nodes.NewSSHExecDialer(signer, 5*time.Second)
	verify := func(fp string) error {
		if fp != hostFingerprint {
			t.Fatalf("host key fingerprint mismatch: got %q want %q", fp, hostFingerprint)
		}
		return nil
	}
	target := nodes.Target{NodeID: "loopback", User: u.Username, Addr: "127.0.0.1:" + strconv.Itoa(port)}

	ctx := context.Background()
	session, err := dialer.Dial(ctx, target, verify)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = session.Close() }()

	remoteDir := t.TempDir() // shared fs (loopback = same machine): the "remote" path
	remotePath := filepath.Join(remoteDir, "sync-stream.bin")

	payload := bytes.Repeat([]byte("cascade real-sshd chunked transfer payload; "), 200)
	const chunkSize = 512
	const streamID = uint64(4242)
	total := chunkCount(len(payload), chunkSize)

	// Interrupt after 3 chunks: encode only [0,3) locally, ship that
	// partial frame stream to the remote file over the real ssh
	// connection, and confirm (via a REAL remote `wc -c`) that exactly
	// that many bytes landed — nothing more, nothing silently completed.
	// SendStream over a bytes.Buffer never errors before Total is
	// exhausted; the interruption is simulated below by truncating what
	// it wrote to exactly the first three frames.
	var partial bytes.Buffer
	if err := SendStream(ctx, &partial, streamID, total, 0, func(seq uint64) ([]byte, error) {
		return chunkPayloadAt(payload, chunkSize, seq), nil
	}); err != nil {
		t.Fatalf("SendStream (full, before truncation): %v", err)
	}
	// Truncate to the first 3 encoded frames worth of bytes by re-decoding
	// locally to find the byte offset after chunk index 2.
	interruptOffset := frameBoundaryAfter(t, partial.Bytes(), 2)
	firstPart := partial.Bytes()[:interruptOffset]

	if err := session.WriteFile(ctx, remotePath, firstPart); err != nil {
		t.Fatalf("WriteFile (interrupted partial): %v", err)
	}
	out, err := session.Output(ctx, "wc -c < "+shellQuoteForTest(remotePath))
	if err != nil {
		t.Fatalf("Output(wc -c): %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != strconv.Itoa(len(firstPart)) {
		t.Fatalf("remote partial file size = %q, want %d", got, len(firstPart))
	}

	// Locally decode what "arrived" (mirrors the receiver reading the
	// remote file back) to find the resume point: durable-received count.
	received, rerr := ReceiveStream(ctx, bytes.NewReader(firstPart), streamID, 0, func(uint64, uint64, []byte) error { return nil })
	if rerr == nil {
		t.Fatal("expected the partial stream (3 of more chunks) to end with an error, not complete")
	}
	if received != 3 {
		t.Fatalf("received = %d, want 3 (the interruption point)", received)
	}

	// Resume: send the REMAINING chunks [3, total) to a second remote
	// file, then have the real sshd concatenate them (a real remote
	// command, not local Go), and read the assembled bytes back through
	// one more real remote `cat`.
	var rest bytes.Buffer
	if err := SendStream(ctx, &rest, streamID, total, received, func(seq uint64) ([]byte, error) {
		return chunkPayloadAt(payload, chunkSize, seq), nil
	}); err != nil {
		t.Fatalf("resumed SendStream: %v", err)
	}
	restPath := remotePath + ".rest"
	if err := session.WriteFile(ctx, restPath, rest.Bytes()); err != nil {
		t.Fatalf("WriteFile (resume tail): %v", err)
	}
	assembledPath := remotePath + ".assembled"
	if _, err := session.Output(ctx, "cat "+shellQuoteForTest(remotePath)+" "+shellQuoteForTest(restPath)+" > "+shellQuoteForTest(assembledPath)); err != nil {
		t.Fatalf("Output(cat assemble): %v", err)
	}
	assembled, err := session.Output(ctx, "cat "+shellQuoteForTest(assembledPath))
	if err != nil {
		t.Fatalf("Output(cat assembled): %v", err)
	}

	full, finalReceived, ferr := ReceiveBytes(ctx, bytes.NewReader(assembled), streamID, 0)
	if ferr != nil {
		t.Fatalf("ReceiveBytes on assembled real-sshd stream: %v", ferr)
	}
	if finalReceived != total {
		t.Fatalf("finalReceived = %d, want %d", finalReceived, total)
	}
	if !bytes.Equal(full, payload) {
		t.Fatal("payload reassembled from the real sshd round trip does not match the original")
	}
}

func chunkPayloadAt(payload []byte, chunkSize int, seq uint64) []byte {
	start := int(seq) * chunkSize
	end := start + chunkSize
	if end > len(payload) {
		end = len(payload)
	}
	return payload[start:end]
}

// frameBoundaryAfter decodes frames from raw sequentially and returns the
// byte offset immediately after the (lastSeq+1)-th frame (0-based
// lastSeq), i.e. the truncation point that keeps exactly chunks
// [0, lastSeq] intact.
func frameBoundaryAfter(t *testing.T, raw []byte, lastSeq uint64) int {
	t.Helper()
	r := bytes.NewReader(raw)
	offset := 0
	for seq := uint64(0); ; seq++ {
		before := len(raw) - r.Len()
		c, err := Decode(r)
		if err != nil {
			t.Fatalf("frameBoundaryAfter: Decode: %v", err)
		}
		if c.Seq != seq {
			t.Fatalf("frameBoundaryAfter: unexpected seq %d at position %d", c.Seq, seq)
		}
		after := len(raw) - r.Len()
		_ = before
		offset = after
		if seq == lastSeq {
			return offset
		}
	}
}

func shellQuoteForTest(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// spawnLoopbackSSHDForSync mirrors internal/nodes' tunnel_test.go
// spawnLoopbackSSHD exactly (same config, same skip-on-sandboxed-
// environment behavior); duplicated here because that helper is
// unexported in package nodes.
func spawnLoopbackSSHDForSync(t *testing.T, clientPub ed25519.PublicKey) (port int, hostKeyFingerprint string) {
	t.Helper()
	sshdPath := findSSHDForSync(t)
	dir := t.TempDir()

	hostPub, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSSHPub, err := ssh.NewPublicKey(hostPub)
	if err != nil {
		t.Fatalf("host pub: %v", err)
	}
	hostKeyFingerprint = nodes.HostKeyFingerprint(hostSSHPub.Marshal())
	block, err := ssh.MarshalPrivateKey(hostPriv, "")
	if err != nil {
		t.Fatalf("marshal host key: %v", err)
	}
	hostKeyPath := filepath.Join(dir, "hostkey")
	if err := os.WriteFile(hostKeyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write host key: %v", err)
	}

	sshPub, err := ssh.NewPublicKey(clientPub)
	if err != nil {
		t.Fatalf("client pub: %v", err)
	}
	authorizedKeysPath := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authorizedKeysPath, ssh.MarshalAuthorizedKey(sshPub), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	port = freePortForSync(t)
	cfgPath := filepath.Join(dir, "sshd_config")
	cfg := strings.Join([]string{
		"Port " + strconv.Itoa(port), "ListenAddress 127.0.0.1", "HostKey " + hostKeyPath,
		"AuthorizedKeysFile " + authorizedKeysPath, "PubkeyAuthentication yes",
		"PasswordAuthentication no", "UsePAM no", "StrictModes no", "LogLevel ERROR",
		"Subsystem sftp none",
	}, "\n") + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write sshd_config: %v", err)
	}

	cmd := exec.Command(sshdPath, "-f", cfgPath, "-D", "-e")
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start sshd (sandboxed environment): %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	waitForPortForSync(t, port)
	return port, hostKeyFingerprint
}

func findSSHDForSync(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/usr/sbin/sshd", "/usr/bin/sshd"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no sshd binary found; skipping real-counterpart lane")
	return ""
}

func freePortForSync(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func waitForPortForSync(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("sshd did not become reachable")
}
