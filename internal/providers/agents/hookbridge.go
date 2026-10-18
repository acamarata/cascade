// Purpose: the pure half of the hook relay: map one harness hook payload to a
//
//	provider.CallbackPayload the driver's callback channel accepts, and
//	check one frame the way that channel does; sendFrame and deliver put a
//	built frame on the driver's unix socket under the counter lock.
//
// Inputs: the hook event name, the harness's hook JSON, the resolved project
//
//	root the callback file was found in; a session token and one frame line.
//
// Outputs: a provider.CallbackPayload without Seq (the relay owns Seq), or a
//
//	typed error; DecodeRelayFrame returns the authenticated payload.
//
// Constraints: only fields present in the captured fixtures
//
//	(testdata/claude-hooks) are read. A frame the driver would reject fails
//	the whole run, so every field is made valid here or dropped: an odd tool
//	name becomes "unknown", a path outside the tree is left out. Callback
//	telemetry is never evidence (m-7): the token file sits in the agent's own
//	tree, so the agent can forge frames, and no consumer may treat a frame as
//	proof of work.
//
// SPORT: internal.providers.agents/ADD (P1-AGT-14, contract:agent-hook-relay).

package agents

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Hook events the relay forwards.
const (
	HookPreToolUse  = "PreToolUse"
	HookPostToolUse = "PostToolUse"
	HookStop        = "Stop"
)

const (
	unknownToolName  = "unknown"
	maxDedupKeyBytes = 128
	maxFramePathLen  = 4096
)

// hookFrameKinds maps a hook event to the callback frame kind: a tool call is
// a status frame, the end of a turn is a done frame.
var hookFrameKinds = map[string]string{
	HookPreToolUse:  "status",
	HookPostToolUse: "status",
	HookStop:        "done",
}

// frameKinds is the closed set of frame kinds the driver accepts.
var frameKinds = map[string]bool{"stdout": true, "status": true, "error": true, "done": true}

var (
	toolNameShape  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	toolUseIDShape = regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)
	hashShape      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// hookInput is the slice of the harness's hook JSON the relay reads. Unknown
// fields are ignored: the harness adds fields between releases.
type hookInput struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolUseID     string          `json:"tool_use_id"`
	DurationMS    int64           `json:"duration_ms"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolResponse  json.RawMessage `json:"tool_response"`
}

// payloadFromHook maps raw (the hook JSON for event) to a payload. root is
// the tree the callback file lives in; file paths are made relative to it.
func payloadFromHook(event string, raw []byte, root string) (provider.CallbackPayload, error) {
	kind, known := hookFrameKinds[event]
	if !known {
		return provider.CallbackPayload{}, cascade.Newf(cascade.KindInvalidInput, "agents: hook relay: unsupported event %q", event)
	}
	var in hookInput
	if err := json.Unmarshal(raw, &in); err != nil || in.HookEventName != event {
		return provider.CallbackPayload{}, cascade.Newf(cascade.KindInvalidInput, "agents: hook relay: payload is not a %s hook payload", event)
	}
	p := provider.CallbackPayload{Kind: kind, ToolName: toolNameFor(event, in.ToolName), Paths: []string{}}
	if toolUseIDShape.MatchString(in.ToolUseID) {
		p.DedupKey = event + ":" + in.ToolUseID
	}
	if event != HookStop {
		p.Paths = hookPaths(root, in.ToolInput)
	}
	if event == HookPostToolUse {
		p.DurationMS = max(in.DurationMS, 0)
		p.ResultHash = responseHash(in.ToolResponse)
	}
	return p, nil
}

// toolNameFor returns the frame's tool name: the event name for a turn end,
// the harness's tool name when it has the shape the driver accepts.
func toolNameFor(event, tool string) string {
	switch {
	case event == HookStop:
		return HookStop
	case toolNameShape.MatchString(tool):
		return tool
	}
	return unknownToolName
}

// responseHash is the SHA-256 of the tool_response bytes as sent, or empty
// when the payload carries none.
func responseHash(resp json.RawMessage) string {
	if len(resp) == 0 || bytes.Equal(resp, []byte("null")) {
		return ""
	}
	sum := sha256.Sum256(resp)
	return hex.EncodeToString(sum[:])
}

// hookPaths returns the tree-relative file path of a tool call, if the tool
// input names one inside root. Nothing else about the input is forwarded.
func hookPaths(root string, toolInput json.RawMessage) []string {
	var in struct {
		FilePath string `json:"file_path"`
	}
	paths := []string{}
	if json.Unmarshal(toolInput, &in) != nil || !filepath.IsAbs(in.FilePath) {
		return paths
	}
	rel, err := filepath.Rel(canonicalPath(root), canonicalPath(in.FilePath))
	if err == nil && validFramePath(rel) {
		paths = append(paths, rel)
	}
	return paths
}

// canonicalPath resolves symlinks in p (for a file not created yet, in its
// directory), so two spellings of one tree, such as /tmp and /private/tmp,
// compare equal; p is returned unchanged when neither resolves.
func canonicalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if d, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(d, filepath.Base(p))
	}
	return p
}

// validFramePath mirrors the driver's path rule: non-empty, clean, relative,
// no ".." element, no control character, bounded.
func validFramePath(p string) bool {
	if p == "" || p == "." || len(p) > maxFramePathLen || filepath.IsAbs(p) || filepath.Clean(p) != p {
		return false
	}
	if !utf8.ValidString(p) || strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// frameShapeOK reports whether p passes the closed-shape rules the driver's
// callback channel applies (everything except Seq).
func frameShapeOK(p provider.CallbackPayload) bool {
	if !frameKinds[p.Kind] {
		return false
	}
	if p.DurationMS < 0 || !toolNameShape.MatchString(p.ToolName) || len(p.DedupKey) > maxDedupKeyBytes || !p.DataClass.Valid() {
		return false
	}
	if p.ResultHash != "" && !hashShape.MatchString(p.ResultHash) {
		return false
	}
	for _, path := range p.Paths {
		if !validFramePath(path) {
			return false
		}
	}
	return true
}

// DecodeRelayFrame checks one relay frame line ("<32 hex token> <payload
// JSON>", trailing newline optional) the way the driver's callback channel
// does: the token is authenticated before the payload is parsed, the payload
// decodes with unknown fields refused, and its shape and Seq are valid.
func DecodeRelayFrame(tok provider.SessionToken, line []byte) (provider.CallbackPayload, error) {
	line = bytes.TrimRight(line, "\n")
	var frame provider.ControlFrame
	if hexTok, rest, ok := bytes.Cut(line, []byte(" ")); ok && len(hexTok) == 2*len(tok) {
		var got provider.SessionToken
		if _, err := hex.Decode(got[:], hexTok); err == nil {
			frame = provider.ControlFrame{Token: got, Payload: rest}
		}
	}
	payload, err := provider.AuthenticateFrame(tok, frame)
	if err != nil {
		return provider.CallbackPayload{}, err
	}
	var p provider.CallbackPayload
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || !atEOF(dec) || p.Seq == 0 || !frameShapeOK(p) {
		return provider.CallbackPayload{}, cascade.New(cascade.KindInvalidInput, "agents: hook relay: frame payload failed validation")
	}
	return p, nil
}

// atEOF reports whether dec has nothing left. dec.More is false before a
// stray closing bracket, so it cannot be the end-of-input test.
func atEOF(dec *json.Decoder) bool {
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}

// sendFrame takes the counter lock, assigns the next Seq, checks the encoded
// frame with DecodeRelayFrame, and delivers it, holding the lock until the
// driver has answered so concurrent relays reach it in Seq order.
func sendFrame(ctx context.Context, cb callbackTarget, p provider.CallbackPayload, uid int) error {
	if fi, err := os.Lstat(cb.socket); err != nil || !socketTrusted(fi, uid) {
		return deniedf("the callback socket is not the caller's socket")
	}
	counter, err := openCounter(filepath.Join(filepath.Dir(cb.socket), counterFileName), uid)
	if err != nil {
		return err
	}
	defer func() { _ = counter.Close() }()
	unlock, err := lockCounter(ctx, counter)
	if err != nil {
		return err
	}
	defer unlock()
	if p.Seq, err = nextSeq(counter, hex.EncodeToString(cb.token[:])); err != nil {
		return err
	}
	if !frameShapeOK(p) {
		return cascade.New(cascade.KindInternal, "agents: hook relay: built a frame the driver would refuse")
	}
	frame, err := provider.EncodeCallbackFrame(cb.token, p)
	if err != nil {
		return err
	}
	if _, err := DecodeRelayFrame(cb.token, frame); err != nil {
		return cascade.New(cascade.KindInternal, "agents: hook relay: built a frame the check refuses")
	}
	return deliver(ctx, cb.socket, frame)
}

// deliver dials the unix socket, writes one frame and reads the one-line ack.
func deliver(ctx context.Context, socket string, frame []byte) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "agents: hook relay: the driver's callback socket is not reachable")
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err := conn.Write(frame); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "agents: hook relay: writing the frame")
	}
	ack, err := bufio.NewReaderSize(io.LimitReader(conn, relayAckMaxBytes), relayAckMaxBytes).ReadString('\n')
	switch ack = strings.TrimSpace(ack); {
	case ack == "ok" || ack == "dup":
		return nil
	case strings.HasPrefix(ack, "refused"):
		return deniedf("the driver refused the frame (%q)", strings.TrimSpace(strings.TrimPrefix(ack, "refused")))
	case err != nil:
		return cascade.Wrap(cascade.KindUnavailable, err, "agents: hook relay: no answer from the driver")
	}
	return cascade.New(cascade.KindUnavailable, "agents: hook relay: no answer from the driver")
}
