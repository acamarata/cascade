// Command hostcallplugin exercises host calls over a real process pipe.
package main

import (
	"bufio"
	"encoding/json"
	"os"
)

type frame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 65536), 8*1024*1024)
	out := json.NewEncoder(os.Stdout)
	var pending json.RawMessage
	replies := 0
	for scanner.Scan() {
		var f frame
		if json.Unmarshal(scanner.Bytes(), &f) != nil {
			return
		}
		if f.Method == "" {
			replies++
			if pending != nil {
				respond(out, pending, f)
				pending = nil
			}
			continue
		}
		switch f.Method {
		case "cascade.hello":
			respond(out, f.ID, map[string]string{"protocol_version": "1.0.0", "manifest_hash": "hostcall"})
		case "proxy", "notify":
			var request frame
			if json.Unmarshal(f.Params, &request) != nil {
				return
			}
			request.ID = f.ID
			if f.Method == "notify" {
				request.ID = nil
			} else {
				pending = f.ID
			}
			emit(out, request)
			if f.Method == "notify" {
				respond(out, f.ID, true)
			}
		case "probe":
			respond(out, f.ID, replies)
		case "crash":
			os.Exit(3)
		}
	}
}

func emit(out *json.Encoder, f frame) {
	_ = out.Encode(struct {
		JSONRPC string `json:"jsonrpc"`
		frame
	}{"2.0", f})
}

func respond(out *json.Encoder, id json.RawMessage, result any) {
	_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
