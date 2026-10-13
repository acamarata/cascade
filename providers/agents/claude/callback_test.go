//go:build !windows

package claude

// Purpose: the per-spawn callback channel over its real unix socket: the
//   0600 token file, AuthenticateFrame before parsing, Seq/DedupKey from
//   the frame, a Seq regression failing the run with ErrOutOfOrderEvent,
//   the data-class join, and the canary scan of frame bytes (the shape
//   rules and bounds are in callback_limits_test.go).
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; tests
//   callback.go. The client uses raw syscall sockets: a local unix socket
//   is not network egress, and unit tests may not import "net" (the
//   no-network lane gate).
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

type channelInfo = provider.CallbackFile

// openJobChannel spawns a job that waits for a release file, and returns
// the driver, the job and the channel the token file announces.
func openJobChannel(t *testing.T, spec func(*fx) provider.AgentJobSpec) (*fx, *Driver, provider.SpawnResult, channelInfo) {
	t.Helper()
	return openJobChannelWith(t, spec, func(*fx) {})
}

// openJobChannelWith is openJobChannel with prep applied to the fixture
// before the wait-for-release prefix is added to its job body.
func openJobChannelWith(t *testing.T, spec func(*fx) provider.AgentJobSpec, prep func(*fx)) (*fx, *Driver, provider.SpawnResult, channelInfo) {
	t.Helper()
	f := newFx(t)
	prep(f)
	release := filepath.Join(f.dir, "release")
	f.body = "while [ ! -f " + release + " ]; do sleep 0.05; done\n" + f.body
	d := f.driver(f.config())
	res, err := d.Spawn(context.Background(), spec(f))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	tokenPath := filepath.Join(f.root, ".claude", provider.CallbackFileName)
	fi, err := os.Lstat(tokenPath)
	if err != nil || fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("token file %v mode %v, want a regular 0600 file", err, fi)
	}
	b, _ := os.ReadFile(tokenPath)
	var ci channelInfo
	if err := json.Unmarshal(b, &ci); err != nil || len(ci.Token) != 32 {
		t.Fatalf("token file %s: %v", b, err)
	}
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	return f, d, res, ci
}

// sendFrames writes lines on one connection and returns the acks read
// back, stopping at the first refusal (the channel closes the connection).
func sendFrames(t *testing.T, sock string, lines ...string) []string {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if err := syscall.Connect(fd, &syscall.SockaddrUnix{Name: sock}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	var acks []string
	buf := make([]byte, 256)
	for _, l := range lines {
		if !writeAll(fd, []byte(l+"\n")) {
			return acks
		}
		n, err := syscall.Read(fd, buf)
		for err == syscall.EINTR {
			n, err = syscall.Read(fd, buf)
		}
		if err != nil || n == 0 {
			return acks
		}
		acks = append(acks, strings.TrimSpace(string(buf[:n])))
		if strings.HasPrefix(acks[len(acks)-1], "refused") {
			return acks
		}
	}
	return acks
}

// writeAll writes b in full: a blocking stream write can return early (a
// runtime preemption signal interrupts it), so the rest is written again.
func writeAll(fd int, b []byte) bool {
	for len(b) > 0 {
		n, err := syscall.Write(fd, b)
		if err == syscall.EINTR {
			continue
		}
		if err != nil || n <= 0 {
			return false
		}
		b = b[n:]
	}
	return true
}

func canConnect(sock string) bool {
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return false
	}
	defer func() { _ = syscall.Close(fd) }()
	return syscall.Connect(fd, &syscall.SockaddrUnix{Name: sock}) == nil
}

func frame(token string, seq int, extra string) string {
	return token + ` {"seq":` + itoa(seq) + `,"dedup_key":"k` + itoa(seq) + `","kind":"status","tool_name":"hook"` + extra + `}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func finish(t *testing.T, f *fx, d *Driver, id provider.AgentJobID) (provider.CollectResult, error) {
	t.Helper()
	_ = os.WriteFile(filepath.Join(f.dir, "release"), nil, 0o600)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return d.Collect(ctx, id)
}

func TestClaudeRejectsForeignSocketFrame(t *testing.T) {
	f, d, res, ci := openJobChannel(t, func(f *fx) provider.AgentJobSpec { return f.spec() })
	foreign := strings.Repeat("0f", 16)
	checks := []struct {
		lines []string
		want  string
	}{
		{[]string{frame(ci.Token, 1, "")}, "ok"},
		{[]string{frame(foreign, 2, "")}, "refused auth"},
		{[]string{foreign + " {not json at all"}, "refused auth"}, // rejected before parsing
		{[]string{`{"seq":2,"kind":"status"}`}, "refused auth"},   // no token at all
		{[]string{frame(ci.Token, 3, "")}, "ok"},
		{[]string{frame(ci.Token, 3, "")}, "dup"},
	}
	for i, c := range checks {
		acks := sendFrames(t, ci.Socket, c.lines...)
		if len(acks) != 1 || acks[0] != c.want {
			t.Fatalf("check %d: acks %q, want %q", i, acks, c.want)
		}
	}
	if acks := sendFrames(t, ci.Socket, ci.Token+" "+strings.Repeat("x", maxFrameBytes+10)); len(acks) != 0 {
		t.Fatalf("an overlong frame got %q", acks)
	}
	cr, err := finish(t, f, d, res.JobID)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var rows []JournalRow
	_ = json.Unmarshal([]byte(cr.Output), &rows)
	var seqs []uint64
	for _, r := range rows {
		if r.ToolName == "hook" {
			seqs = append(seqs, r.Seq)
		}
	}
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 3 {
		t.Fatalf("accepted frame seqs %v, want [1 3]", seqs)
	}
	if exists(filepath.Join(f.root, ".claude", provider.CallbackFileName)) || canConnect(ci.Socket) {
		t.Fatal("the channel outlived the job")
	}
	f, d, res, ci = openJobChannel(t, func(f *fx) provider.AgentJobSpec { return f.spec() })
	if acks := sendFrames(t, ci.Socket, frame(ci.Token, 3, ""), frame(ci.Token, 2, "")); len(acks) != 2 || acks[1] != "refused order" {
		t.Fatalf("Seq regression acks %q", acks)
	}
	if cr, err = finish(t, f, d, res.JobID); err != provider.ErrOutOfOrderEvent || cr.Output != "" {
		t.Fatalf("Collect = %v (output %q): a Seq regression must fail the run with ErrOutOfOrderEvent", err, cr.Output)
	}
}

func TestClaudeDataClassStamped(t *testing.T) {
	restricted := func(f *fx) provider.AgentJobSpec {
		s := f.spec()
		s.DataClass = provider.DataClassRestricted
		return s
	}
	f, d, res, ci := openJobChannel(t, restricted)
	if acks := sendFrames(t, ci.Socket, frame(ci.Token, 1, `,"data_class":"public"`)); acks[0] != "ok" {
		t.Fatalf("acks %q", acks)
	}
	cr, err := finish(t, f, d, res.JobID)
	if err != nil || cr.DataClass != provider.DataClassRestricted {
		t.Fatalf("class %s err %v: a public frame must not lower a restricted job", cr.DataClass, err)
	}
	var rows []JournalRow
	if err := json.Unmarshal([]byte(cr.Output), &rows); err != nil || len(rows) < 2 {
		t.Fatalf("Output %q: %v (%d rows), want the done row and the frame's row", cr.Output, err, len(rows))
	}
	for _, r := range rows {
		if r.DataClass != provider.DataClassRestricted {
			t.Fatalf("row %+v lowered the class", r)
		}
	}
	f, d, res, ci = openJobChannel(t, restricted)
	sendFrames(t, ci.Socket, frame(ci.Token, 1, `,"data_class":"local-only"`))
	if cr, err = finish(t, f, d, res.JobID); err != nil || cr.DataClass != provider.DataClassLocalOnly {
		t.Fatalf("class %s err %v: a local-only frame must raise the job", cr.DataClass, err)
	}
}

func TestCanaryInCallbackFrameFailsClosed(t *testing.T) {
	f, d, res, ci := openJobChannel(t, func(f *fx) provider.AgentJobSpec { return f.spec() })
	matches, _ := filepath.Glob(filepath.Join(f.home, canaryFileName+"-*"))
	if len(matches) != 1 {
		t.Fatalf("planted canaries %v, want one", matches)
	}
	b, _ := os.ReadFile(matches[0])
	value := strings.TrimSpace(strings.TrimPrefix(string(b), "token="))
	sendFrames(t, ci.Socket, frame(ci.Token, 1, `,"result_hash":"`+value+`"`))
	_, err := finish(t, f, d, res.JobID)
	if !errors.Is(err, ErrCredentialCanary) || bytes.Contains([]byte(err.Error()), []byte(value)) {
		t.Fatalf("Collect = %v, want the canary failure without the canary", err)
	}
}

// frameWith encodes p under the channel's token with the one producer
// seam, provider.EncodeCallbackFrame, without its trailing newline.
func frameWith(t *testing.T, ci channelInfo, p provider.CallbackPayload) string {
	t.Helper()
	var tok provider.SessionToken
	if _, err := hex.Decode(tok[:], []byte(ci.Token)); err != nil {
		t.Fatal(err)
	}
	b, err := provider.EncodeCallbackFrame(tok, p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(b), "\n")
}
