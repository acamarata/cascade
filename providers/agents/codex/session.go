package codex

// Purpose: the session run loop: handshake (initialize, initialized,
// thread/start, turn/start), then frames until turn/completed, then the
// child's exit and the job's final state.
// Inputs: the child's stdout lines.
// Outputs: the job record's events, output, journal and final state.
// Constraints: every line passes the job's OutputFilter before anything is
// stored; a filter error fails the job with ErrEgressSubstitutionFailed
// and withholds its output.
// SPORT: providers.agents.codex/ADD (P1-AGT-05).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/procgroup"
	"github.com/acamarata/cascade/pkg/provider"
)

// maxLine bounds one frame.
const maxLine = 16 << 20

// lineReader reads newline-delimited frames.
type lineReader struct{ r *bufio.Reader }

func newLineReader(r io.Reader) *lineReader { return &lineReader{r: bufio.NewReaderSize(r, 64<<10)} }

func (l *lineReader) next() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := l.r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxLine {
			return nil, malformed("frame exceeds the line bound")
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil && len(buf) > 0 && err == io.EOF {
			return buf, nil
		}
		return buf, err
	}
}

func idKey(id int64) string { return strconv.FormatInt(id, 10) }

// run drives one job to its end and records the outcome.
func (d *Driver) run(j *job) {
	err := d.handshake(j)
	if err == nil {
		err = d.loop(j, "")
	}
	d.finish(j, err)
}

// handshake runs initialize, initialized, thread/start and turn/start.
func (d *Driver) handshake(j *job) error {
	res, err := d.call(j, "initialize", map[string]any{"clientInfo": map[string]any{"name": ClientName, "version": ClientVersion}})
	if err != nil {
		return err
	}
	if err := j.checkInitialize(res); err != nil {
		return err
	}
	line, err := encodeNotification("initialized")
	if err != nil {
		return err
	}
	if err := j.write(line); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, ErrNonZeroExit, "codex: writing initialized")
	}
	if res, err = d.call(j, "thread/start", threadStartParams(j.spec)); err != nil {
		return err
	}
	var ts struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(res, &ts) != nil || ts.Thread.ID == "" {
		return malformed("thread/start returned no thread id")
	}
	j.mu.Lock()
	j.journal.ThreadID = ts.Thread.ID
	j.mu.Unlock()
	if res, err = d.call(j, "turn/start", turnStartParams(j.spec, ts.Thread.ID)); err != nil {
		return err
	}
	var tr struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(res, &tr)
	j.setTurn(tr.Turn.ID)
	return nil
}

func (j *job) setTurn(id string) {
	j.mu.Lock()
	stopping := j.stopping
	if id != "" {
		j.journal.TurnID = id
	}
	j.mu.Unlock()
	if stopping { // a cancel raced the handshake: interrupt now
		j.interrupt()
	}
}

// checkInitialize records the user agent and pins the codex version and
// the profile auth dir.
func (j *job) checkInitialize(res json.RawMessage) error {
	var ir struct {
		UserAgent string `json:"userAgent"`
		CodexHome string `json:"codexHome"`
	}
	if err := json.Unmarshal(res, &ir); err != nil {
		return malformed("initialize result is not an object")
	}
	ver := versionFromUserAgent(ir.UserAgent)
	j.mu.Lock()
	j.journal.UserAgent, j.journal.CodexVersion = ir.UserAgent, ver
	j.journal.SchemaRevision = string(ProtocolRevision) + "/" + ver
	j.mu.Unlock()
	if ver != PinnedCodexVersion {
		return provider.ErrHarnessIncompatible
	}
	if ir.CodexHome == "" || filepath.Clean(ir.CodexHome) != j.codexDir {
		return ErrCodexHomeMismatch
	}
	return nil
}

// versionFromUserAgent takes the token after the first '/' up to a space.
func versionFromUserAgent(ua string) string {
	_, rest, ok := strings.Cut(ua, "/")
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, " ")
	return v
}

// call sends one request and reads frames until its response.
func (d *Driver) call(j *job, method string, params any) (json.RawMessage, error) {
	j.mu.Lock()
	j.nextID++
	id := j.nextID
	j.outstanding[idKey(id)] = true
	j.mu.Unlock()
	line, err := encodeRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	if err := j.write(line); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, ErrNonZeroExit, "codex: writing %s", method)
	}
	var res json.RawMessage
	err = d.loopUntil(j, idKey(id), &res)
	return res, err
}

// loop reads frames until turn/completed (want "") or the response to
// want, which it stores in res.
func (d *Driver) loop(j *job, want string) error { return d.loopUntil(j, want, nil) }

func (d *Driver) loopUntil(j *job, want string, res *json.RawMessage) error {
	for {
		line, err := j.reader.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errEarlyEOF
			}
			return err
		}
		line = bytes.TrimRight(line, "\r\n")
		filtered, ferr := j.filter(line)
		if ferr != nil {
			return provider.ErrEgressSubstitutionFailed
		}
		f, err := decodeFrame(line)
		if err != nil {
			return err
		}
		done, err := d.dispatch(j, f, filtered, want, res)
		if err != nil || done {
			return err
		}
	}
}

// errEarlyEOF marks stdout closing before the session ended.
var errEarlyEOF = cascade.Wrap(cascade.KindUnavailable, ErrNonZeroExit, "codex: app server closed stdout before turn/completed")

// finish waits for the child and settles the job's final state.
func (d *Driver) finish(j *job, runErr error) {
	if runErr != nil && !j.isStopping() {
		_ = procgroup.Signal(j.pgid, procgroup.SigKill)
	}
	j.closeStdin() // after the kill: a write blocked on a full pipe holds the lock until the child dies
	waitErr := j.cmd.Wait()
	d.dropPending(j)
	j.mu.Lock()
	defer j.mu.Unlock()
	defer close(j.done)
	switch {
	case j.stopping:
		j.state = provider.AgentRunCancelled
	case runErr != nil && (runErr != errEarlyEOF || waitErr == nil): // identity, not errors.Is (Kind only)
		j.state, j.err = provider.AgentRunFailed, runErr
	case waitErr != nil:
		j.state = provider.AgentRunFailed
		j.err = cascade.Wrapf(cascade.KindUnavailable, ErrNonZeroExit, "codex: app server exit: %v", waitErr)
	default:
		j.settleTurn()
	}
	if j.err != nil {
		j.output.Reset()
	}
}

// settleTurn maps turn/completed's status to the final state.
func (j *job) settleTurn() {
	switch j.turnStatus {
	case "completed":
		j.state = provider.AgentRunAccepted
	case "interrupted":
		j.state, j.err = provider.AgentRunCancelled, ErrRunCancelled
	default:
		j.state, j.err = provider.AgentRunFailed, ErrTurnFailed
	}
}

func (j *job) isStopping() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stopping
}
