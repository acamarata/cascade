// Purpose: the hook relay, the one production producer of callback frames
//
//	(R-14.327): read a harness hook payload, find the per-spawn callback
//	file the driver announced in the project directory's own .claude/, and
//	send one token-framed, strictly sequenced frame to the driver's unix
//	socket.
//
// Inputs: the project directory the harness names (not the hook's working
//
//	directory or the payload's cwd), the hook event name, the hook JSON on
//	stdin, the uid the callback file must belong to.
//
// Outputs: nil when a frame was accepted or there is no callback file
//
//	(a hook outside a spawn does nothing); KindPermissionDenied when the
//	callback file or socket is not the driver's (nothing is sent) or the
//	driver refused the frame; other typed errors for bad input.
//
// Constraints: the token is read only from the callback file, never from the
//
//	environment or argv, and is never printed or put in an error. The file is
//	looked up only in <resolved project dir>/.claude, never by walking up.
//	That .claude must be a real directory owned by uid (a symlink to another
//	tree's .claude is refused); the file is opened relative to that verified
//	directory without following a symlink, and must be a regular, 0600 file
//	owned by uid with exactly one link (a hard link to another tree's file
//	is refused), checked on the opened descriptor. Seq comes from a counter
//	file in the callback socket's directory (the driver's fresh 0700
//	directory, refused unless it is a real directory owned by uid with no
//	group or other bits; the driver removes it when the job ends, so the
//	leased tree gains no stray artifact), locked across the whole send so
//	frames reach the driver in Seq order whichever process sends them; the
//	counter is keyed by token, so a new spawn restarts at 1. The counter is a
//	regular 0600 file owned by uid with exactly one link (a hard link to
//	another file is refused, never written); its update is kill-safe (a
//	fixed-width record is written over the old one, then the file is
//	truncated) and a stored maximum refuses instead of wrapping to 0. The only
//	real-time call is the short sleep between lock attempts (pollWait); the
//	relay's deadline comes from ctx. The relay posts nowhere
//	but that socket (never the daemon RPC socket). Callback telemetry is never
//	evidence (m-7).
//
// SPORT: internal.providers.agents/ADD (P1-AGT-14, contract:agent-hook-relay).

package agents

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	relayMaxStdin    = 32 << 20
	callbackBodyMax  = 4 << 10
	relayAckMaxBytes = 256
)

// Relay sends one frame for the hook event read from stdin. projectDir is the
// spawn's project directory as the harness named it (the command passes
// CLAUDE_PROJECT_DIR), not the hook's working directory. See the file
// comment for the contract.
func Relay(ctx context.Context, projectDir string, event string, stdin io.Reader, uid int) error {
	if !filepath.IsAbs(projectDir) {
		return cascade.New(cascade.KindInvalidInput, "agents: hook relay: the project directory must be absolute")
	}
	root, found := callbackRoot(projectDir)
	if !found {
		return nil
	}
	cb, err := readCallbackFile(root, uid)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, relayMaxStdin+1))
	if err != nil || len(raw) > relayMaxStdin {
		return cascade.New(cascade.KindInvalidInput, "agents: hook relay: hook payload unreadable or over the size cap")
	}
	p, err := payloadFromHook(event, raw, root)
	if err != nil {
		return err
	}
	return sendFrame(ctx, cb, p, uid)
}

// callbackRoot resolves the project directory and reports whether its own
// .claude/ holds the callback file. There is no walk upward and the hook's
// working directory is never consulted, so a project directory that lies
// inside another tree never finds that tree's file. A path that cannot be
// examined counts as absent.
func callbackRoot(projectDir string) (string, bool) {
	root, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		return "", false
	}
	if _, err := os.Lstat(filepath.Join(root, ".claude", provider.CallbackFileName)); err != nil {
		return "", false
	}
	return root, true
}

// callbackTarget is the decoded callback file.
type callbackTarget struct {
	socket string
	token  provider.SessionToken
}

// deniedf builds the one refusal kind for a callback file or socket that is
// not the driver's; its text never includes file contents.
func deniedf(format string, args ...any) error {
	return cascade.Newf(cascade.KindPermissionDenied, "agents: hook relay: "+format, args...)
}

// errDirUntrusted and errFileUnopenable are openUnderDir's two failures; the
// callers turn them into refusals that name the directory or the file.
var (
	errDirUntrusted   = errors.New("the directory is not trusted")
	errFileUnopenable = errors.New("the file cannot be opened")
)

// readCallbackFile opens root/.claude/<callback file> relative to the
// verified .claude directory, without following a symlink at either level,
// checks the opened descriptor is a regular single-link 0600 file owned by
// uid, and decodes it.
func readCallbackFile(root string, uid int) (callbackTarget, error) {
	f, err := openUnderDir(filepath.Join(root, ".claude"), provider.CallbackFileName, os.O_RDONLY, 0, uid, false)
	switch {
	case errors.Is(err, errDirUntrusted):
		return callbackTarget{}, deniedf("the .claude directory is not a real directory owned by the caller")
	case err != nil:
		return callbackTarget{}, deniedf("the callback file cannot be opened")
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !counterTrusted(fi, uid) {
		return callbackTarget{}, deniedf("the callback file is not a single-link 0600 file owned by the caller")
	}
	body, err := io.ReadAll(io.LimitReader(f, callbackBodyMax+1))
	if err != nil || len(body) > callbackBodyMax {
		return callbackTarget{}, deniedf("the callback file is unreadable or too large")
	}
	return decodeCallbackBody(body)
}

// decodeCallbackBody parses provider.CallbackFile strictly.
func decodeCallbackBody(body []byte) (callbackTarget, error) {
	var cf provider.CallbackFile
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cf); err != nil || dec.More() || !filepath.IsAbs(cf.Socket) {
		return callbackTarget{}, deniedf("the callback file is malformed")
	}
	var t callbackTarget
	if len(cf.Token) != 2*len(t.token) || cf.Token != strings.ToLower(cf.Token) {
		return callbackTarget{}, deniedf("the callback file is malformed")
	}
	if _, err := hex.Decode(t.token[:], []byte(cf.Token)); err != nil || !t.token.Valid() {
		return callbackTarget{}, deniedf("the callback file is malformed")
	}
	t.socket = cf.Socket
	return t, nil
}

const (
	counterFileName  = "cascade-callback.seq"
	lockPollInterval = 2 * time.Millisecond
	counterReadMax   = 128
)

// pollWait is the one real-time seam in the relay: the sleep between two
// lock attempts. It never decides when the relay gives up; ctx does.
var pollWait = time.After

// seqStore is the slice of *os.File the counter update needs; a test
// substitutes a store that stops mid-update to model a kill.
type seqStore interface {
	io.ReaderAt
	io.WriterAt
	Truncate(size int64) error
}

// lockCounter takes the exclusive lock on f, polling so ctx can cancel it.
func lockCounter(ctx context.Context, f *os.File) (func(), error) {
	for {
		ok, err := tryLockFile(f)
		if err != nil {
			return nil, cascade.Wrap(cascade.KindInternal, err, "agents: hook relay: locking the sequence file")
		}
		if ok {
			return func() { unlockFile(f) }, nil
		}
		select {
		case <-ctx.Done():
			return nil, cascade.Wrap(cascade.KindTimeout, ctx.Err(), "agents: hook relay: waiting for the sequence lock")
		case <-pollWait(lockPollInterval):
		}
	}
}

// openCounter opens (creating, 0600) the counter file at path relative to
// its verified directory, without following a symlink, and refuses anything
// but a regular single-link 0600 file owned by uid.
func openCounter(path string, uid int) (*os.File, error) {
	f, err := openUnderDir(filepath.Dir(path), filepath.Base(path), os.O_RDWR|os.O_CREATE, 0o600, uid, true)
	switch {
	case errors.Is(err, errDirUntrusted):
		return nil, deniedf("the callback socket's directory is not a private directory owned by the caller")
	case err != nil:
		return nil, deniedf("the sequence file cannot be opened")
	}
	if fi, err := f.Stat(); err != nil || !counterTrusted(fi, uid) {
		_ = f.Close()
		return nil, deniedf("the sequence file is not a single-link 0600 file owned by the caller")
	}
	return f, nil
}

// counterRecord is "<token hex> <n>\n" with n zero-padded to 20 digits, so
// every record for one token has the same length.
func counterRecord(tokenHex string, n uint64) []byte {
	return []byte(fmt.Sprintf("%s %020d\n", tokenHex, n))
}

// nextSeq reads the first line "<token hex> <n>" from f, returns n+1 (1 for a
// counter that belongs to another token or is unreadable) and stores it
// before the frame is sent: a lost ack leaves a gap, never a repeat.
func nextSeq(f seqStore, tokenHex string) (uint64, error) {
	body, err := io.ReadAll(io.NewSectionReader(f, 0, counterReadMax))
	if err != nil {
		return 0, cascade.Wrap(cascade.KindInternal, err, "agents: hook relay: reading the sequence file")
	}
	seq := uint64(1)
	line, _, _ := strings.Cut(string(body), "\n")
	if tok, n, ok := strings.Cut(strings.TrimSpace(line), " "); ok && tok == tokenHex {
		if last, perr := strconv.ParseUint(n, 10, 64); perr == nil {
			if last == math.MaxUint64 {
				return 0, cascade.New(cascade.KindInternal, "agents: hook relay: the sequence counter is exhausted")
			}
			seq = last + 1
		}
	}
	rec := counterRecord(tokenHex, seq)
	if _, err := f.WriteAt(rec, 0); err != nil {
		return 0, cascade.Wrap(cascade.KindInternal, err, "agents: hook relay: writing the sequence file")
	}
	if err := f.Truncate(int64(len(rec))); err != nil {
		return 0, cascade.Wrap(cascade.KindInternal, err, "agents: hook relay: writing the sequence file")
	}
	return seq, nil
}
