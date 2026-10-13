// Purpose: the per-spawn inbound callback channel (R-21.158 framing,
//   T-2.yaml FRAMING): a unix socket the driver listens on, announced
//   with the 128-bit session token in the provider.CallbackFileName file
//   (body provider.CallbackFile, mode 0600) under the leased tree's
//   .claude/ directory. Every frame is provider.EncodeCallbackFrame's
//   "<32 hex token> <provider.CallbackPayload JSON>\n";
//   provider.AuthenticateFrame checks the token BEFORE the payload is
//   parsed, then Seq/DedupKey come from the frame itself and go through
//   provider.EventNormalizer (a Seq regression is ErrOutOfOrderEvent).
// Inputs: frames from hook or tool subprocesses of the job.
// Outputs: closed-projection JournalRows for accepted frames; one ack
//   line per frame ("ok", "dup", or "refused <reason>").
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; split
//   out for the 300-line cap. The stdout pipe carries no wire token (it is
//   authenticated by pipe ownership). A refused frame closes its
//   connection. A frame without the token is refused and not attributed to
//   the job; every authenticated frame that breaks a shape rule, the
//   filter, the row or byte bound, or the Seq order fails the run and
//   stops the job (callback_frame.go).
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

package claude

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const maxFrameBytes = 64 << 10

// callbackChannel is one job's listener and the rows it accepted.
type callbackChannel struct {
	ln        net.Listener
	sockDir   string
	tokenPath string
	token     provider.SessionToken
	class     provider.DataClass
	protocol  provider.ProtocolVersion
	scan      *canaryScanner
	output    provider.OutputFilter

	mu     sync.Mutex
	norm   *provider.EventNormalizer
	rows   []JournalRow
	used   int
	failed error
	onFail func()
	conns  map[net.Conn]bool
	closed bool
	wg     sync.WaitGroup
}

var frameKinds = map[string]provider.DriverEventKind{
	"stdout": provider.DriverEventStdout, "status": provider.DriverEventStatus,
	"error": provider.DriverEventError, "done": provider.DriverEventDone,
}

// openCallbackChannel listens on a fresh 0700 socket directory and writes
// the provider.CallbackFile body to claudeDir/provider.CallbackFileName
// with O_EXCL and O_NOFOLLOW, mode 0600.
func openCallbackChannel(claudeDir string, c *callbackChannel) error {
	dir, err := os.MkdirTemp(shortTempBase(), "ccb")
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "claude: callback socket dir")
	}
	c.sockDir, c.tokenPath = dir, filepath.Join(claudeDir, provider.CallbackFileName)
	sock := filepath.Join(dir, "s")
	if c.ln, err = net.Listen("unix", sock); err != nil {
		_ = os.RemoveAll(dir)
		return cascade.Wrapf(cascade.KindInternal, err, "claude: callback listener")
	}
	body, _ := json.Marshal(provider.CallbackFile{Socket: sock, Token: hex.EncodeToString(c.token[:])})
	if err := writeExclusive(c.tokenPath, body); err != nil {
		c.close()
		return err
	}
	c.norm, c.conns = provider.NewEventNormalizer(), map[net.Conn]bool{}
	c.wg.Add(1)
	go c.accept()
	return nil
}

// shortTempBase keeps the socket path under the unix-socket length limit.
func shortTempBase() string {
	if base := os.TempDir(); len(base) < 64 {
		return base
	}
	return "/tmp"
}

// writeExclusive creates path (0600, O_EXCL, O_NOFOLLOW). A stale regular
// file left by a crashed run is removed first; anything else there refuses.
func writeExclusive(path string, body []byte) error {
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return cascade.Wrapf(cascade.KindCapabilityDenied, ErrWorktreeEscape, "claude: %s is not a regular file", path)
		}
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|openNoFollow, 0o600)
	if err != nil {
		return cascade.Wrapf(cascade.KindCapabilityDenied, ErrWorktreeEscape, "claude: writing the callback token file: %v", err)
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return cascade.Wrapf(cascade.KindInternal, err, "claude: writing the callback token file")
	}
	return f.Close()
}

func (c *callbackChannel) accept() {
	defer c.wg.Done()
	for {
		conn, err := c.ln.Accept()
		if err != nil {
			return
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			_ = conn.Close()
			continue
		}
		c.conns[conn] = true
		c.wg.Add(1)
		c.mu.Unlock()
		go c.serve(conn)
	}
}

// serve reads frames until EOF, an overlong line, or a refusal.
func (c *callbackChannel) serve(conn net.Conn) {
	defer c.wg.Done()
	defer c.drop(conn)
	r := bufio.NewReaderSize(conn, 4096)
	for {
		line, err := readFrame(r)
		if err != nil {
			return
		}
		ack := c.handle(line)
		if _, werr := conn.Write([]byte(ack + "\n")); werr != nil || ack[0] == 'r' {
			return
		}
	}
}

// readFrame returns one newline-terminated line of at most maxFrameBytes.
func readFrame(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxFrameBytes {
			return nil, cascade.New(cascade.KindInvalidInput, "claude: callback frame too long")
		}
		if err != bufio.ErrBufferFull {
			return bytes.TrimRight(line, "\n"), err
		}
	}
}

// handle authenticates one frame BEFORE anything parses it, then charges,
// validates, filters and normalizes it, and returns its ack. After the
// channel has failed the run, every frame is refused.
func (c *callbackChannel) handle(line []byte) string {
	c.scan.Scan(line)
	frame := provider.ControlFrame{}
	if tok, rest, ok := bytes.Cut(line, []byte(" ")); ok && len(tok) == 32 {
		if _, err := hex.Decode(frame.Token[:], tok); err == nil {
			frame.Payload = rest
		}
	}
	payload, err := provider.AuthenticateFrame(c.token, frame)
	switch {
	case err != nil:
		return "refused auth"
	case c.failure() != nil:
		return "refused failed"
	}
	if err := c.charge(len(line)); err != nil {
		return c.fail(err, "refused overflow")
	}
	p, kind, err := decodePayload(payload)
	if err != nil {
		return c.fail(err, "refused payload")
	}
	if p, err = redact(c.output, p); err != nil {
		return c.fail(err, "refused filter")
	}
	ack, err := c.accept1(p, kind)
	if err != nil {
		return c.fail(err, ack)
	}
	return ack
}

// accept1 normalizes one validated frame into a row. The row bound is
// checked before Normalize, so the dedup set never outgrows it.
func (c *callbackChannel) accept1(p provider.CallbackPayload, kind provider.DriverEventKind) (string, error) {
	class := provider.JoinSensitivity(c.class, p.DataClass)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.rows) >= maxCallbackRows {
		return "refused overflow", ErrFilteredBufferOverflow
	}
	ev, keep, err := c.norm.Normalize(provider.DriverEvent{Kind: kind, Seq: p.Seq, DedupKey: p.DedupKey, DataClass: class})
	switch {
	case err != nil:
		return "refused order", err
	case !keep:
		return "dup", nil
	}
	ev.Protocol = c.protocol
	rec := TranscriptRecord{Tool: p.ToolName, Duration: time.Duration(p.DurationMS) * time.Millisecond,
		ExitCode: p.ExitCode, Paths: p.Paths, ResultHash: p.ResultHash}
	c.rows = append(c.rows, toJournalRow(rec, ev, class))
	return "ok", nil
}

func (c *callbackChannel) drop(conn net.Conn) {
	_ = conn.Close()
	c.mu.Lock()
	delete(c.conns, conn)
	c.mu.Unlock()
}

// accepted returns a copy of the rows accepted so far.
func (c *callbackChannel) accepted() []JournalRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]JournalRow(nil), c.rows...)
}

// close stops the listener and every connection, then removes the socket
// directory and the token file. A second call is harmless.
func (c *callbackChannel) close() {
	if c.ln != nil {
		_ = c.ln.Close()
	}
	c.mu.Lock()
	c.closed = true
	for conn := range c.conns {
		_ = conn.Close()
	}
	c.mu.Unlock()
	c.wg.Wait()
	_ = os.RemoveAll(c.sockDir)
	if c.tokenPath != "" {
		_ = os.Remove(c.tokenPath)
	}
}
