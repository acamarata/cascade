// Purpose: the capture-and-replay harness. It reads the frames a real MCP
// client sent, drives them through a Server, and compares the answers with
// the ones recorded beside them, so every new protocol surface is proven
// against bytes from a real peer rather than against a description.
//
// Inputs: capture directories <client>/<client_version>[-<purpose>]/ holding
// in.jsonl, out.jsonl and capture.json, under a root whose parent holds the
// README with one table row per capture (testdata/README.md states the
// contract).
//
// Outputs: the answers a Server gives to captured frames, and typed errors
// naming the capture and the frame that diverged.
//
// It is a test harness, not product code: it compiles only into this
// package's tests, and the external test package reaches it through the
// package under test.
//
// Constraints: no network and no clock. Replay encodes with plain
// json.Marshal, not ResponseMarshaler: it compares this server's answers and
// writes nothing to a wire, so the egress firewall has no crossing to guard.
//
// SPORT: internal/mcp:replay (ADD).

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/buildinfo"
	"github.com/acamarata/cascade/pkg/cascade"
)

// needsDaemon marks a capture whose tools forward to a daemon.
const needsDaemon = "daemon"

// CaptureMeta is the provenance a capture.json records. Needs "daemon"
// means the capture's tools forward to a daemon: ReplayAll skips it and its
// owner test replays it against one.
type CaptureMeta struct {
	Client          string `json:"client"`
	ClientVersion   string `json:"client_version"`
	ProtocolVersion string `json:"protocol_version"`
	CapturedAt      string `json:"captured_at"`
	Command         string `json:"command"`
	Profile         string `json:"profile,omitempty"`
	Needs           string `json:"needs,omitempty"`
}

// ReadCapture reads the capture in dir: the client's frames, the recorded
// answers and the provenance. A missing or malformed capture.json is
// KindInvalidInput, as is a missing frame file.
func ReadCapture(dir string) (in, out [][]byte, meta CaptureMeta, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, "capture.json"))
	if err != nil {
		return nil, nil, CaptureMeta{}, cascade.Wrap(cascade.KindInvalidInput, err,
			"mcp: capture "+dir+" has no readable capture.json")
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, nil, CaptureMeta{}, cascade.Wrap(cascade.KindInvalidInput, err,
			"mcp: capture "+dir+" has a malformed capture.json")
	}
	if in, err = readJSONL(filepath.Join(dir, "in.jsonl")); err != nil {
		return nil, nil, CaptureMeta{}, err
	}
	if out, err = readJSONL(filepath.Join(dir, "out.jsonl")); err != nil {
		return nil, nil, CaptureMeta{}, err
	}
	return in, out, meta, nil
}

// readJSONL splits a capture file into its non-blank lines.
func readJSONL(path string) ([][]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "mcp: reading capture frames")
	}
	var lines [][]byte
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if line = bytes.TrimSpace(line); len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// Replay drives the frames through s in order and returns one output line
// per request, none per notification. A line that does not parse answers
// with an id-less error, exactly as the stdio transport does.
func Replay(ctx context.Context, s *Server, in [][]byte) ([][]byte, error) {
	if s == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "mcp: replay needs a server")
	}
	var out [][]byte
	for _, line := range in {
		if err := ctx.Err(); err != nil {
			return nil, cascade.Wrap(cascade.KindCanceled, err, "mcp: replay stopped")
		}
		var resp *Response
		if frame, perr := ParseFrame(line); perr != nil {
			resp = &Response{JSONRPC: "2.0", Error: perr}
		} else {
			resp = s.Dispatch(ctx, frame)
		}
		if resp == nil {
			continue
		}
		encoded, err := json.Marshal(resp)
		if err != nil {
			return nil, cascade.Wrap(cascade.KindInternal, err, "mcp: encoding a replayed response")
		}
		out = append(out, encoded)
	}
	return out, nil
}

// CompareFrames reports the first difference between the recorded frames
// and the replayed ones, byte for byte after JSON canonicalisation. The one
// exception is result.serverInfo.version: it is the build stamp, so got must
// equal buildinfo.Version and the recorded value is not compared.
func CompareFrames(want, got [][]byte) error {
	if len(want) != len(got) {
		return cascade.New(cascade.KindIntegrity,
			fmt.Sprintf("mcp: replay produced %d frames, the capture holds %d", len(got), len(want)))
	}
	for i := range want {
		wantJSON, err := canonicalFrame(want[i], false)
		if err != nil {
			return cascade.Wrap(cascade.KindIntegrity, err, fmt.Sprintf("mcp: recorded frame %d", i))
		}
		gotJSON, err := canonicalFrame(got[i], true)
		if err != nil {
			return cascade.Wrap(cascade.KindIntegrity, err, fmt.Sprintf("mcp: replayed frame %d", i))
		}
		if !bytes.Equal(wantJSON, gotJSON) {
			return cascade.New(cascade.KindIntegrity, fmt.Sprintf(
				"mcp: frame %d diverged\n captured: %s\n now:      %s", i, wantJSON, gotJSON))
		}
	}
	return nil
}

// canonicalFrame re-encodes one frame with sorted keys and its number
// literals intact, masking serverInfo.version. With checkBuild set, that
// version must also be the running build's.
func canonicalFrame(raw []byte, checkBuild bool) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var frame any
	if err := dec.Decode(&frame); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the JSON frame")
	}
	if err := maskServerVersion(frame, checkBuild); err != nil {
		return nil, err
	}
	return json.Marshal(frame)
}

// maskServerVersion replaces result.serverInfo.version with a marker.
// Nothing else in a frame is ever masked.
func maskServerVersion(frame any, checkBuild bool) error {
	top, _ := frame.(map[string]any)
	result, _ := top["result"].(map[string]any)
	info, _ := result["serverInfo"].(map[string]any)
	version, ok := info["version"]
	if !ok {
		return nil
	}
	if checkBuild && version != buildinfo.Version {
		return fmt.Errorf("serverInfo.version = %v, want the build version %q", version, buildinfo.Version)
	}
	info["version"] = "<build>"
	return nil
}

// ReplayAll checks every directory under root holding an in.jsonl. Each must
// carry capture.json and a row in the README beside root. A Needs "daemon"
// capture is skipped; every other one is replayed against the server build
// returns for it and compared. Every failing capture is reported, and a tree
// with no capture at all fails: it proves nothing.
func ReplayAll(ctx context.Context, root string, build func(meta CaptureMeta) (*Server, error)) error {
	if build == nil {
		return cascade.New(cascade.KindInvalidInput, "mcp: replay needs a server builder")
	}
	dirs, err := captureDirs(root)
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		return cascade.New(cascade.KindInvalidInput, "mcp: no captures under "+root)
	}
	readme, err := os.ReadFile(filepath.Join(filepath.Dir(root), "README.md"))
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "mcp: the captures have no README")
	}
	var failures []error
	for _, rel := range dirs {
		if err := replayCapture(ctx, filepath.Join(root, rel), rel, string(readme), build); err != nil {
			failures = append(failures, fmt.Errorf("capture %s: %w", filepath.ToSlash(rel), err))
		}
	}
	return errors.Join(failures...)
}

// captureDirs lists the directories under root holding an in.jsonl, relative
// to root, in lexical order.
func captureDirs(root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "in.jsonl" {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		dirs = append(dirs, rel)
		return nil
	})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "mcp: walking the captures")
	}
	return dirs, nil
}

// replayCapture applies ReplayAll's rules to one capture directory.
func replayCapture(ctx context.Context, dir, rel, readme string, build func(CaptureMeta) (*Server, error)) error {
	in, out, meta, err := ReadCapture(dir)
	if err != nil {
		return err
	}
	if !hasReadmeRow(readme, filepath.ToSlash(rel)) {
		return cascade.New(cascade.KindInvalidInput, "mcp: no README row for this capture")
	}
	if meta.Needs == needsDaemon {
		return nil
	}
	if err := checkProvenance(meta); err != nil {
		return err
	}
	server, err := build(meta)
	if err != nil {
		return err
	}
	got, err := Replay(ctx, server, in)
	if err != nil {
		return err
	}
	return CompareFrames(out, got)
}

// hasReadmeRow reports whether a table row names the capture in backticks.
// The backticks keep 2.1.273 from matching a 2.1.273-inbox row.
func hasReadmeRow(readme, rel string) bool {
	cell := "`" + rel + "`"
	for _, line := range strings.Split(readme, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "|") && strings.Contains(line, cell) {
			return true
		}
	}
	return false
}

// checkProvenance refuses a replayable capture that does not say where it
// came from: frames with no named real client are not evidence.
func checkProvenance(meta CaptureMeta) error {
	fields := []struct{ name, value string }{
		{"client", meta.Client},
		{"client_version", meta.ClientVersion},
		{"protocol_version", meta.ProtocolVersion},
		{"captured_at", meta.CapturedAt},
		{"command", meta.Command},
	}
	for _, field := range fields {
		if field.value == "" {
			return cascade.New(cascade.KindInvalidInput, "mcp: capture.json has no "+field.name)
		}
	}
	return nil
}
