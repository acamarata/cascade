// Purpose: the R-21.152 custody types: the CLOSED JournalRow projection
//   and the record it is built from, the bounded in-memory filteredWriter
//   every child byte passes through, and the per-spawn credential canary
//   with its exact/base64/hex scanner.
// Inputs: raw child stdout/stderr chunks; authenticated callback frames;
//   the per-spawn spec.Filter.
// Outputs: JournalRows; filtered in-memory bytes; canary verdicts.
// Constraints: JournalRow's fields are exactly {tool_name, duration_ms,
//   exit_code, paths, result_hash, event_kind, seq, data_class,
//   protocol_version}: no prompt, model output, transcript reference or
//   free-text error. Nothing here writes a transcript to disk. A filter
//   error withholds that chunk and everything after it.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

package claude

import (
	"bytes"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TranscriptRecord is the allowlisted content of one row: from a
// validated, filtered callback frame, or the result object's duration and
// hash (never a tool: tool events come only from the callback channel).
type TranscriptRecord struct {
	Tool       string
	Duration   time.Duration
	ExitCode   int
	Paths      []string
	ResultHash string
}

// JournalRow is the CLOSED projection Collect persists (R-21.152).
type JournalRow struct {
	ToolName        string                   `json:"tool_name"`
	DurationMS      int64                    `json:"duration_ms"`
	ExitCode        int                      `json:"exit_code"`
	Paths           []string                 `json:"paths"`
	ResultHash      string                   `json:"result_hash"`
	Kind            provider.DriverEventKind `json:"event_kind"`
	Seq             uint64                   `json:"seq"`
	DataClass       provider.DataClass       `json:"data_class"`
	ProtocolVersion provider.ProtocolVersion `json:"protocol_version"`
}

// RawTranscript always refuses: no raw stream is kept or handed out.
func (d *Driver) RawTranscript(provider.AgentJobID) (string, error) {
	return "", provider.ErrTranscriptPersistenceForbidden
}

// toJournalRow projects one record plus its normalized event, joining the
// class (R-21.143: raised, never lowered).
func toJournalRow(rec TranscriptRecord, ev provider.AgentEvent, class provider.DataClass) JournalRow {
	paths := append([]string{}, rec.Paths...)
	return JournalRow{
		ToolName: rec.Tool, DurationMS: rec.Duration.Milliseconds(), ExitCode: rec.ExitCode,
		Paths: paths, ResultHash: rec.ResultHash, Kind: ev.Kind, Seq: ev.Seq,
		DataClass: provider.JoinSensitivity(class, ev.DataClass), ProtocolVersion: ev.Protocol,
	}
}

// maxFilteredBufferBytes bounds each stream's in-memory buffer.
const maxFilteredBufferBytes = 16 << 20

// ErrFilteredBufferOverflow fails a run whose output passed the bound.
var ErrFilteredBufferOverflow = cascade.New(cascade.KindQuotaExhausted, "claude: filtered output buffer overflow")

// errFilterFailed marks a chunk the OutputFilter refused.
var errFilterFailed = cascade.New(cascade.KindUnavailable, "claude: output filter failed")

// filteredWriter passes every chunk through the spawn's OutputFilter
// before buffering it in memory, bounded at maxFilteredBufferBytes.
type filteredWriter struct {
	f          provider.OutputFilter
	mu         sync.Mutex
	buf        bytes.Buffer
	overflowed bool
	withheld   bool
}

// Write filters p. A filter error withholds p and every later chunk and
// returns errFilterFailed; passing the bound latches the overflow.
func (w *filteredWriter) Write(p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.withheld || w.overflowed {
		return nil
	}
	out, err := w.f(p)
	if err != nil {
		w.withheld = true
		return errFilterFailed
	}
	if w.buf.Len()+len(out) > maxFilteredBufferBytes {
		w.overflowed = true
		return ErrFilteredBufferOverflow
	}
	w.buf.Write(out)
	return nil
}

// Bytes returns a copy of everything filtered so far.
func (w *filteredWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

// Overflowed reports whether the bound was ever passed.
func (w *filteredWriter) Overflowed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.overflowed
}

// ErrCredentialCanary fails a run whose output, frames, artifacts or
// journal carried the seeded canary in any form (R-21.152). The message
// never contains the canary itself.
var ErrCredentialCanary = cascade.New(cascade.KindIntegrity, "claude: seeded credential canary observed; run failed closed")

// cryptoRead is crypto/rand's reader (aliased so the rand.* lint that
// targets math/rand does not match it).
var cryptoRead = cryptorand.Read

// canaryFileName prefixes the decoy credential planted in the runtime
// home; a per-spawn suffix lets concurrent jobs share one profile.
const canaryFileName = ".cascade-credential-canary"

// canary is one spawn's seeded value and the file that holds it.
type canary struct {
	value string
	path  string
	forms [][]byte
}

// plantCanary mints a random canary and writes it, mode 0600, into home.
func plantCanary(home string) (*canary, error) {
	var raw [16]byte
	if _, err := cryptoRead(raw[:]); err != nil {
		return nil, cascade.Wrapf(cascade.KindInternal, err, "claude: minting the canary")
	}
	value := "cascade-canary-" + hex.EncodeToString(raw[:])
	c := &canary{value: value, path: filepath.Join(home, canaryFileName+"-"+value[15:23])}
	c.forms = canaryForms(c.value)
	if err := writeExclusive(c.path, []byte("token="+c.value+"\n")); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *canary) remove() { _ = os.Remove(c.path) }

// canaryForms returns the canary in exact (as is, upper and lower case), hex (both cases) and base64
// (standard and URL alphabets, each of the three byte alignments) forms.
func canaryForms(v string) [][]byte {
	forms := [][]byte{[]byte(v), bytes.ToUpper([]byte(v)), bytes.ToLower([]byte(v)),
		[]byte(hex.EncodeToString([]byte(v))), bytes.ToUpper([]byte(hex.EncodeToString([]byte(v))))}
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		for k := 0; k < 3; k++ {
			padded := append(make([]byte, k), v...)
			full := enc.EncodeToString(padded)
			start, end := 0, (len(padded)/3)*4
			if k > 0 {
				start = 4
			}
			forms = append(forms, []byte(full[start:end]))
		}
	}
	return forms
}

// canaryScanner finds any form in a stream fed in chunks, carrying a tail
// so a form split across two chunks is still found.
type canaryScanner struct {
	mu    sync.Mutex
	forms [][]byte
	tail  []byte
	hit   bool
}

func newCanaryScanner(forms [][]byte) *canaryScanner { return &canaryScanner{forms: forms} }

// Scan feeds one chunk.
func (s *canaryScanner) Scan(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	window := append(s.tail, p...)
	longest := 0
	for _, f := range s.forms {
		longest = max(longest, len(f))
		if bytes.Contains(window, f) {
			s.hit = true
		}
	}
	keep := min(len(window), longest)
	s.tail = append([]byte(nil), window[len(window)-keep:]...)
}

// Hit reports whether any form was seen.
func (s *canaryScanner) Hit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hit
}

// containsCanary reports whether b holds any form.
func containsCanary(b []byte, forms [][]byte) bool {
	for _, f := range forms {
		if bytes.Contains(b, f) {
			return true
		}
	}
	return false
}

// newRecord mints the per-spawn token and canary, opens the callback
// channel and builds the job record. Any failure undoes what it made.
func (d *Driver) newRecord(p *spawnPlan) (*jobRecord, error) {
	tok, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	cn, err := plantCanary(p.job.Env.Home)
	if err != nil {
		return nil, err
	}
	cn.forms = append(cn.forms, keyValueForms(p.job.Env.Driver, p.prof.KeyVar)...)
	claudeDir := filepath.Join(p.lease.Root, ".claude")
	ch := &callbackChannel{token: tok, class: provider.JoinSensitivity(p.job.DataClass), protocol: p.protocol,
		scan: newCanaryScanner(cn.forms), output: p.job.Filter}
	if err := ensureRealDir(claudeDir); err != nil {
		cn.remove()
		return nil, err
	}
	if err := openCallbackChannel(claudeDir, ch); err != nil {
		cn.remove()
		return nil, err
	}
	injected := map[string]bool{".claude/" + provider.CallbackFileName: true}
	for _, rel := range p.injected {
		injected[rel] = true
	}
	return &jobRecord{
		id: p.id, lease: p.lease, vendorVars: []string{p.prof.VendorAuthVar, p.prof.KeyVar}, protocol: p.protocol,
		dataClass: provider.JoinSensitivity(p.job.DataClass), injected: injected,
		stdout: &filteredWriter{f: p.job.Filter}, stderr: &filteredWriter{f: p.job.Filter},
		scanOut: newCanaryScanner(cn.forms), scanErr: newCanaryScanner(cn.forms),
		channel: ch, canary: cn, state: provider.AgentRunRunning, done: make(chan struct{}),
	}, nil
}

// release closes the callback channel and removes the token file and the
// planted canary.
func (r *jobRecord) release() {
	r.channel.close()
	r.canary.remove()
}
