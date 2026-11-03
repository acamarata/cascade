package codex

// Purpose: the replay App Server. TestMain turns this test binary into a
// stand-in `codex app-server` when it is launched with that argv: it
// replays a session file (testdata/session.jsonl by default) frame by
// frame, answering each client request with the block recorded after it.
// Inputs: $CODEX_HOME/replay.json (written by the test kit; the child sees
// only the driver's allowlisted environ, so options travel through the
// profile dir, never through extra variables).
// Constraints: no network; writes only under $CODEX_HOME.

import (
	"bufio"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// replayOpts steers the replay server.
type replayOpts struct {
	Session   string `json:"session"`
	CodexHome string `json:"codex_home,omitempty"` // "" = report $CODEX_HOME
	ExitCode  int    `json:"exit_code,omitempty"`  // exit status at stdin EOF
	Hang      bool   `json:"hang,omitempty"`       // ignore TERM, never answer
	Log       bool   `json:"log,omitempty"`        // append stdin lines to stdin.log
	EnvOut    bool   `json:"env_out,omitempty"`    // dump the environ to env.json
}

// sessionLine is one recorded frame.
type sessionLine struct {
	Dir   string          `json:"dir"`
	TMS   int             `json:"t_ms"`
	Frame json.RawMessage `json:"frame"`
	Raw   string          `json:"raw,omitempty"` // a verbatim (possibly broken) line
}

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == SubcommandAppServer {
		os.Exit(replayMain())
	}
	os.Exit(m.Run())
}

type replayer struct {
	home  string
	opts  replayOpts
	lines []sessionLine
	in    *bufio.Scanner
	out   *os.File
	log   *os.File
	start time.Time
}

func replayMain() int {
	home := os.Getenv(VendorAuthVar)
	r := &replayer{home: home, out: os.Stdout, start: time.Now()}
	if b, err := os.ReadFile(filepath.Join(home, "replay.json")); err != nil || json.Unmarshal(b, &r.opts) != nil {
		return 90
	}
	_ = os.WriteFile(filepath.Join(home, "started"), nil, 0o600)
	if r.opts.EnvOut {
		b, _ := json.Marshal(os.Environ())
		_ = os.WriteFile(filepath.Join(home, "env.json"), b, 0o600)
	}
	if r.opts.Hang {
		signal.Ignore(syscall.SIGTERM)
		_ = os.WriteFile(filepath.Join(home, "hanging"), nil, 0o600)
		for {
			time.Sleep(time.Hour)
		}
	}
	if r.opts.Log {
		r.log, _ = os.OpenFile(filepath.Join(home, "stdin.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
	if err := r.load(); err != nil {
		return 91
	}
	r.in = bufio.NewScanner(os.Stdin)
	r.in.Buffer(make([]byte, 1<<20), 16<<20)
	r.serve()
	return r.opts.ExitCode
}

func (r *replayer) load() error {
	b, err := os.ReadFile(r.opts.Session)
	if err != nil {
		return err
	}
	for _, l := range splitLines(b) {
		var sl sessionLine
		if err := json.Unmarshal(l, &sl); err != nil {
			return err
		}
		r.lines = append(r.lines, sl)
	}
	return nil
}

// readClient returns the next client frame, or nil at EOF.
func (r *replayer) readClient() map[string]json.RawMessage {
	for r.in.Scan() {
		if r.log != nil {
			_, _ = r.log.Write(append(append([]byte(nil), r.in.Bytes()...), '\n'))
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(r.in.Bytes(), &m) == nil {
			return m
		}
	}
	return nil
}

// serve answers each client request with its recorded block.
func (r *replayer) serve() {
	for {
		m := r.readClient()
		if m == nil {
			return
		}
		if r.interrupt(m) {
			continue
		}
		var method string
		_ = json.Unmarshal(m["method"], &method)
		if _, isReq := m["id"]; !isReq {
			continue
		}
		if !r.emitBlock(method) {
			return // nothing recorded: the server dies here
		}
	}
}

// interrupt answers turn/interrupt and reports the turn interrupted.
func (r *replayer) interrupt(m map[string]json.RawMessage) bool {
	var method string
	_ = json.Unmarshal(m["method"], &method)
	if method != "turn/interrupt" {
		return false
	}
	r.write(json.RawMessage(`{"id":` + string(m["id"]) + `,"result":{}}`))
	r.write(json.RawMessage(`{"method":"turn/completed","params":{"turn":{"id":"0","items":[],"status":"interrupted","error":null}}}`))
	return true
}

// emitBlock sends the s2c lines recorded after the c2s request method,
// pacing them by t_ms; a server request waits for its answer, and a
// refusal (decline, cancel, denied, abort) ends the block. It reports
// false when nothing was recorded for method.
func (r *replayer) emitBlock(method string) bool {
	i := r.find(method)
	if i < 0 {
		return false
	}
	base := r.lines[i].TMS
	blockStart := time.Now()
	for _, l := range r.lines[i+1:] {
		if l.Dir == "c2s" {
			return true
		}
		if wait := time.Duration(l.TMS-base)*time.Millisecond - time.Since(blockStart); wait > 0 {
			time.Sleep(wait)
		}
		if l.Raw != "" {
			_, _ = r.out.WriteString(l.Raw + "\n")
			continue
		}
		r.write(r.rewrite(l.Frame))
		if !r.awaitAnswer(l.Frame) {
			return true
		}
	}
	return true
}

// find returns the index of the recorded c2s request for method.
func (r *replayer) find(method string) int {
	for i, l := range r.lines {
		var f struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if l.Dir == "c2s" && json.Unmarshal(l.Frame, &f) == nil && f.Method == method && len(f.ID) > 0 {
			return i
		}
	}
	return -1
}

// awaitAnswer blocks on a server request until its answer arrives and
// reports whether the block continues.
func (r *replayer) awaitAnswer(frame json.RawMessage) bool {
	var f struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(frame, &f) != nil || f.Method == "" || len(f.ID) == 0 {
		return true
	}
	for {
		m := r.readClient()
		if m == nil {
			return false
		}
		if r.interrupt(m) {
			return false
		}
		if string(m["id"]) != string(f.ID) {
			continue
		}
		var res struct {
			Decision string `json:"decision"`
		}
		_ = json.Unmarshal(m["result"], &res)
		switch res.Decision {
		case "decline", "cancel", "denied", "abort":
			return false
		}
		return true
	}
}

// rewrite maps the recorded codexHome onto this profile's dir.
func (r *replayer) rewrite(frame json.RawMessage) json.RawMessage {
	var f map[string]json.RawMessage
	if json.Unmarshal(frame, &f) != nil || f["result"] == nil {
		return frame
	}
	var res map[string]json.RawMessage
	if json.Unmarshal(f["result"], &res) != nil || res["codexHome"] == nil {
		return frame
	}
	home := r.home
	if r.opts.CodexHome != "" {
		home = r.opts.CodexHome
	}
	res["codexHome"], _ = json.Marshal(home)
	f["result"], _ = json.Marshal(res)
	out, _ := json.Marshal(f)
	return out
}

func (r *replayer) write(frame json.RawMessage) {
	_, _ = r.out.Write(append(append([]byte(nil), frame...), '\n'))
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	sc := bufio.NewScanner(bytesReader(b))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			out = append(out, append([]byte(nil), sc.Bytes()...))
		}
	}
	return out
}
