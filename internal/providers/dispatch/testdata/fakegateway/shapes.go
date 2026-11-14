package main

import (
	"fmt"
	"net/http"
)

// Fault shapes the script can name. Each one is a transient failure under
// docs/conductor/transient-retry.md.
const (
	shapeOverloaded  = "overloaded"   // 529 with an overloaded_error body
	shapeAPIError    = "api_error"    // 500 with an api_error body
	shapeStreamError = "stream_error" // 200 SSE whose first event is an overloaded_error
)

// writeFault writes one transient failure and returns its status. An
// unknown shape is a 400 so a mistyped script fails loudly.
func writeFault(w http.ResponseWriter, shape string) int {
	switch shape {
	case shapeOverloaded:
		writeError(w, 529, "overloaded_error", "Overloaded")
		return 529
	case shapeAPIError:
		writeError(w, http.StatusInternalServerError, "api_error", "Internal server error")
		return http.StatusInternalServerError
	case shapeStreamError:
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
		return http.StatusOK
	}
	writeError(w, http.StatusBadRequest, "invalid_request_error", "fakegateway: unknown fault shape "+shape)
	return http.StatusBadRequest
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Request-Id", "req_000")
	w.WriteHeader(status)
	_, _ = fmt.Fprint(w, body)
}

func writeError(w http.ResponseWriter, status int, kind, msg string) {
	writeJSON(w, status, fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q},"request_id":"req_000"}`, kind, msg))
}

// replyText is the fake's one reply.
const replyText = "fake gateway reply ok"

func messageBody(model string) string {
	return fmt.Sprintf(`{"id":"msg_000","type":"message","role":"assistant","model":%q,`+
		`"content":[{"type":"text","text":%q}],"stop_reason":"end_turn","stop_sequence":null,`+
		`"usage":{"input_tokens":5,"output_tokens":4}}`, model, replyText)
}

// writeStream writes the reply as the Messages API's SSE event sequence.
func writeStream(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Request-Id", "req_000")
	w.WriteHeader(http.StatusOK)
	events := []struct{ name, data string }{
		{"message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_000","type":"message","role":"assistant","model":%q,`+
			`"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":1}}}`, model)},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, replyText)},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
	f, _ := w.(http.Flusher)
	for _, e := range events {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, e.data)
		if f != nil {
			f.Flush()
		}
	}
}
