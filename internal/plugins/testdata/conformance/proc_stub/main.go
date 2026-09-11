// Command proc_stub is the subprocess side of the process-RPC spike
// adapter (P1-E14-W3-S30-T6). It reads one JSON request envelope per
// line from stdin and writes one JSON result envelope per line to
// stdout, implementing the same seven ABI v1 host functions with the
// same deterministic fixture responses the builtin spike adapter uses,
// so the process and builtin adapters are directly comparable under the
// conformance suite. This file ships no logic beyond that stdin/stdout
// protocol: it is testdata for the spike, never a production binary.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type envelope struct {
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload"`
}

type result struct {
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

func main() {
	reader := bufio.NewScanner(os.Stdin)
	reader.Buffer(make([]byte, 0, 64*1024), 1<<20)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()

	for reader.Scan() {
		line := reader.Bytes()
		var env envelope
		if err := json.Unmarshal(line, &env); err != nil {
			writeResult(writer, result{Error: fmt.Sprintf("decode envelope: %v", err)})
			continue
		}
		writeResult(writer, dispatch(env))
	}
}

func dispatch(env envelope) result {
	switch env.Method {
	case "HostHTTP":
		return handleHTTP(env.Payload)
	case "HostStorage":
		return handleStorage(env.Payload)
	case "HostLog":
		return handleLog(env.Payload)
	case "HostStream":
		return handleStream(env.Payload)
	case "HostSecretRef":
		return handleSecretRef(env.Payload)
	case "HostEventEmit":
		return handleEventEmit(env.Payload)
	case "HostToolRegister":
		return handleToolRegister(env.Payload)
	default:
		return result{Error: fmt.Sprintf("unknown method %q", env.Method)}
	}
}

// Each handler mirrors the deterministic fixture logic of the builtin
// spike adapter in internal/plugins/conformance_test.go, using the same
// JSON field names so the two adapters are directly comparable.

func handleHTTP(payload json.RawMessage) result {
	var req struct {
		Method string `json:"method"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return result{Error: err.Error()}
	}
	return okResult(map[string]any{"status": 200, "body": "echo:" + req.URL})
}

func handleStorage(payload json.RawMessage) result {
	var req struct {
		Op    string `json:"op"`
		Key   string `json:"key"`
		Value string `json:"value,omitempty"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return result{Error: err.Error()}
	}
	v := "stored:" + req.Key
	if req.Op == "set" {
		v = req.Value
	}
	return okResult(map[string]any{"value": v})
}

func handleLog(payload json.RawMessage) result {
	return okResult(map[string]any{"accepted": true})
}

func handleStream(payload json.RawMessage) result {
	var req struct {
		ChannelID string `json:"channelId"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return result{Error: err.Error()}
	}
	return okResult(map[string]any{"bytesWritten": len(req.Data)})
}

func handleSecretRef(payload json.RawMessage) result {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return result{Error: err.Error()}
	}
	return okResult(map[string]any{"refId": "ref:" + req.Name})
}

func handleEventEmit(payload json.RawMessage) result {
	var req struct {
		Topic   string `json:"topic"`
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return result{Error: err.Error()}
	}
	return okResult(map[string]any{"eventId": "evt:" + req.Topic})
}

func handleToolRegister(payload json.RawMessage) result {
	return okResult(map[string]any{"registered": true})
}

func okResult(v any) result {
	data, err := json.Marshal(v)
	if err != nil {
		return result{Error: err.Error()}
	}
	return result{Payload: data}
}

func writeResult(w *bufio.Writer, r result) {
	data, err := json.Marshal(r)
	if err != nil {
		fmt.Fprintf(w, "{\"error\":%q}\n", err.Error())
		w.Flush()
		return
	}
	w.Write(data)
	w.WriteByte('\n')
	w.Flush()
}
