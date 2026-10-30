// Command replayplugin stands in for cascade-github in the PR-diff wiring
// tests. It answers cascade-github.prs.diff for octocat/Hello-World#1 with
// the api.github.com capture (response.diff) found next to its own
// executable, shaped as tools.PRDiff, and #2 with a plugin error frame. Any
// other request makes it exit, so an unexpected call cannot pass silently.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type prDiff struct {
	Diff  string `json:"diff"`
	Bytes int    `json:"bytes"`
}

func main() {
	s := bufio.NewScanner(os.Stdin)
	e := json.NewEncoder(os.Stdout)
	for s.Scan() {
		var in frame
		if json.Unmarshal(s.Bytes(), &in) != nil || in.ID == 0 {
			continue
		}
		var out frame
		switch in.Method {
		case "cascade.hello":
			out = frame{JSONRPC: "2.0", ID: in.ID, Result: map[string]string{"protocol_version": "1.0.0", "manifest_hash": ""}}
		case "cascade-github.prs.diff":
			out = diffReply(in)
		default:
			os.Exit(3)
		}
		if e.Encode(out) != nil {
			return
		}
	}
}

func diffReply(in frame) frame {
	var args struct {
		Owner  string `json:"owner"`
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	dec := json.NewDecoder(bytes.NewReader(in.Params))
	dec.DisallowUnknownFields()
	if dec.Decode(&args) != nil || args.Owner != "octocat" || args.Repo != "Hello-World" {
		os.Exit(2)
	}
	switch args.Number {
	case 1:
		body := capture()
		return frame{JSONRPC: "2.0", ID: in.ID, Result: prDiff{Diff: string(body), Bytes: len(body)}}
	case 2:
		return frame{JSONRPC: "2.0", ID: in.ID, Error: &rpcError{Code: -32000, Message: "not found: github: not found (Not Found)"}}
	default:
		os.Exit(2)
	}
	return frame{}
}

func capture() []byte {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(4)
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "response.diff"))
	if err != nil {
		os.Exit(4)
	}
	return body
}
