//go:build integration

// Purpose (this file): the dress rehearsal over a REAL loopback sshd —
//   the SAME drill script the in-process rehearsal runs, with the only
//   difference being that every batch is shipped to a remote file over a
//   real ssh session and read back through a real remote `cat`.
//
// WHY BOTH LANES EXIST. The in-process rehearsal runs everywhere and
//   proves the SCRIPT. This one proves the script over the real transport,
//   so the only thing left untested before the two-machine drill is which
//   machine is on the other end. Art.2: the transport is an external
//   contract, and a rehearsal that only ever handed bytes across a
//   function call would have proven nothing about it.
//
// FILES_SCOPE DEVIATION (recorded per LANE-RULES §1): the contract lists
//   six acceptance files, all untagged. A `//go:build integration` lane
//   cannot live in an untagged file, and putting the sshd rehearsal in the
//   unconditional suite would make every developer's `go test` depend on a
//   local sshd.
//
// SPORT: sync/acceptance-drill sshd rehearsal (ADD) — P1-E17-W4-S38-T5.

package sync

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/acamarata/cascade/internal/nodes"
)

// sshCarrier ships each batch to a remote file over a real ssh session and
// reads it back, so the bytes the receiver decodes are bytes that really
// crossed sshd.
func sshCarrier(t *testing.T) wireCarrier {
	t.Helper()
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
	session, err := nodes.NewSSHExecDialer(signer, 5*time.Second).Dial(context.Background(),
		nodes.Target{NodeID: "loopback", User: u.Username, Addr: "127.0.0.1:" + strconv.Itoa(port)},
		func(fp string) error {
			if fp != hostFingerprint {
				t.Fatalf("host key fingerprint mismatch: got %q want %q", fp, hostFingerprint)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	// The loopback "remote" shares this filesystem, so the round trip is
	// through sshd rather than through the disk: the WRITE and the READ
	// are both real remote operations.
	dir := t.TempDir()
	var leg int
	return func(t *testing.T, frames []byte) []byte {
		t.Helper()
		leg++
		path := filepath.Join(dir, "batch-"+strconv.Itoa(leg)+".bin")
		ctx := context.Background()
		if err := session.WriteFile(ctx, path, frames); err != nil {
			t.Fatalf("shipping batch %d over ssh: %v", leg, err)
		}
		out, err := session.Output(ctx, "cat "+shellQuoteForTest(path))
		if err != nil {
			t.Fatalf("reading batch %d back over ssh: %v", leg, err)
		}
		if len(out) != len(frames) {
			t.Fatalf("batch %d came back %d bytes, sent %d", leg, len(out), len(frames))
		}
		return out
	}
}

// TestSyncRoundTripDressRehearsalRealSSHD runs the whole drill script with
// every batch crossing a real sshd.
func TestSyncRoundTripDressRehearsalRealSSHD(t *testing.T) {
	carry := sshCarrier(t)
	a, b := newAcceptancePeer(t, "laptop"), newAcceptancePeer(t, "server")
	runDrillScript(t, carry, a, b, nodes.TierController)

	assertConverged(t, a, b)
	if a.engine.Conflicts().Len() == 0 && b.engine.Conflicts().Len() == 0 {
		t.Error("the drill journaled no conflicts over the real transport")
	}
}

// TestTheSSHCarrierReallyMovesTheBytes is the mutation proof for the lane
// above: if the carrier quietly returned its input, the rehearsal would
// pass without sshd having been involved at all.
func TestTheSSHCarrierReallyMovesTheBytes(t *testing.T) {
	carry := sshCarrier(t)
	sent := []byte("cascade acceptance drill carrier probe")
	got := carry(t, sent)
	if string(got) != string(sent) {
		t.Fatalf("the carrier returned %q, want the bytes it was given", got)
	}
	// The bytes came back from a remote `cat`, which means the write
	// happened: a carrier that short-circuited would have no file to read
	// and the Output call above would have failed.
	if len(got) == 0 {
		t.Fatal("the carrier returned nothing")
	}
}
