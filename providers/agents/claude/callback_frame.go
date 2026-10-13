// Purpose: the shape rules, redaction and bounds for one authenticated
//   callback frame (R-21.152; T0 decisions F1/F2 of the S-61.T2 final
//   rework): the closed payload decode, the per-field validation, the
//   OutputFilter pass over tool_name and paths, and the latched run failure
//   the channel raises when any of them refuses.
// Inputs: an authenticated payload; the injected provider.OutputFilter.
// Outputs: a validated, filtered provider.CallbackPayload, or a typed error that
//   fails the whole run (never a silently dropped frame).
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; split
//   from callback.go for the 300-line cap. No free text reaches a row:
//   tool_name must match [A-Za-z0-9_.:-]{1,128}; result_hash is empty or 64
//   lowercase hex; each path is non-empty, clean, relative, free of ".."
//   and control characters, and at most 4096 bytes; dedup_key is at most
//   128 bytes; data_class is empty or a closed member; duration_ms is not
//   negative. The channel accepts at most maxCallbackRows rows (which also
//   bounds the dedup set: a key is kept only with its row) and
//   maxCallbackBytes authenticated frame bytes (the stdout bound).
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	maxCallbackRows  = 10000
	maxCallbackBytes = maxFilteredBufferBytes
	maxPathBytes     = 4096
	maxDedupKeyBytes = 128
)

// ErrCallbackFrameRejected fails a run that sent an authenticated callback
// frame breaking the closed payload shape.
var ErrCallbackFrameRejected = cascade.New(cascade.KindInvalidInput,
	"claude: an authenticated callback frame failed validation; run failed closed")

var (
	toolNameShape   = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	resultHashShape = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// decodedAll reports whether dec has nothing left: dec.More is false before a
// stray closing bracket, so a payload such as {..}} would pass it.
func decodedAll(dec *json.Decoder) bool {
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}

// decodePayload parses the closed payload and applies every shape rule.
func decodePayload(payload []byte) (provider.CallbackPayload, provider.DriverEventKind, error) {
	var p provider.CallbackPayload
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || !decodedAll(dec) {
		return p, 0, ErrCallbackFrameRejected
	}
	kind, known := frameKinds[p.Kind]
	switch {
	case !known, p.Seq == 0, p.DurationMS < 0, !toolNameShape.MatchString(p.ToolName),
		p.ResultHash != "" && !resultHashShape.MatchString(p.ResultHash),
		len(p.DedupKey) > maxDedupKeyBytes, !p.DataClass.Valid(),
		!validPaths(p.Paths):
		return p, 0, ErrCallbackFrameRejected
	}
	return p, kind, nil
}

// validPaths reports whether every path is a clean, relative, bounded
// worktree path without a ".." element or a control character.
func validPaths(paths []string) bool {
	for _, p := range paths {
		if p == "" || len(p) > maxPathBytes || filepath.IsAbs(p) || filepath.Clean(p) != p {
			return false
		}
		if strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return false
		}
		for _, part := range strings.Split(filepath.ToSlash(p), "/") {
			if part == ".." {
				return false
			}
		}
	}
	return true
}

// redact passes tool_name and every path through the OutputFilter; the
// row keeps the filtered values. A filter error fails the run with
// ErrEgressSubstitutionFailed.
func redact(filter provider.OutputFilter, p provider.CallbackPayload) (provider.CallbackPayload, error) {
	name, err := filter([]byte(p.ToolName))
	if err != nil {
		return p, provider.ErrEgressSubstitutionFailed
	}
	p.ToolName = string(name)
	paths := make([]string, len(p.Paths))
	for i, s := range p.Paths {
		out, err := filter([]byte(s))
		if err != nil {
			return p, provider.ErrEgressSubstitutionFailed
		}
		paths[i] = string(out)
	}
	p.Paths = paths
	return p, nil
}

// charge counts one authenticated frame against maxCallbackBytes.
func (c *callbackChannel) charge(n int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.used += n
	if c.used > maxCallbackBytes {
		return ErrFilteredBufferOverflow
	}
	return nil
}

// fail latches the run's first channel failure, fires the ladder hook when
// one is installed, and returns ack.
func (c *callbackChannel) fail(err error, ack string) string {
	c.mu.Lock()
	if c.failed == nil {
		c.failed = err
	}
	hook := c.onFail
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	return ack
}

// failure returns the latched channel failure, or nil.
func (c *callbackChannel) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed
}

// setFailHook installs the hook that stops the job; a failure latched
// before the job's process group was known fires it at once.
func (c *callbackChannel) setFailHook(fn func()) {
	c.mu.Lock()
	c.onFail = fn
	latched := c.failed != nil
	c.mu.Unlock()
	if latched {
		fn()
	}
}
