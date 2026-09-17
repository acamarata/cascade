package intake

// Purpose: the live micro-verify request -- the one-token completion each
//   driver shape sends to prove a credential works (P1-E16-W4-S35-T11
//   split, for the 300-line cap).
// Inputs: a driver kind, its base URL, the credential and a model.
// Outputs: the HTTPRequest that endpoint expects.
// Constraints: one vendor carries the credential in the QUERY STRING, so
//   any error quoting this request's URL quotes the key. Every caller of
//   the URL built here passes its failures through redactCredential.
// SPORT: internal/providers/intake micro-verify request (ADD) --
//   P1-E16-W4-S35-T11.

import (
	"encoding/json"
	"net/http"
)

// microVerifyBody builds the 1-token completion request body for model
// under kind.
func microVerifyBody(kind DriverKind, model string) []byte {
	// Anthropic and every openai-compat-shaped driver (openai-compat,
	// ollama, localllm) share one request shape; only gemini differs.
	// One case list per shape, exhaustive over all 5 DriverKind members,
	// avoids repeating the JSON literal per member.
	payload := map[string]any{
		"model": model, "max_tokens": 1,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	switch kind {
	case DriverGemini:
		payload = map[string]any{
			"contents":         []map[string]any{{"parts": []map[string]string{{"text": "hi"}}}},
			"generationConfig": map[string]any{"maxOutputTokens": 1},
		}
	case DriverAnthropic, DriverOpenAICompat, DriverOllama, DriverLocalLLM:
		// payload already set to the shared shape above.
	}
	b, _ := json.Marshal(payload)
	return b
}

// microVerifyRequest builds the full HTTPRequest for the live micro-verify
// call, per driver kind's chat-completion endpoint.
func microVerifyRequest(kind DriverKind, base, key, model string) HTTPRequest {
	body := microVerifyBody(kind, model)
	switch kind {
	case DriverAnthropic:
		return HTTPRequest{Method: http.MethodPost, URL: base + "/v1/messages", Body: body,
			Headers: map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01", "content-type": "application/json"}}
	case DriverGemini:
		return HTTPRequest{Method: http.MethodPost, URL: base + "/v1beta/models/" + model + ":generateContent?key=" + key, Body: body,
			Headers: map[string]string{"content-type": "application/json"}}
	case DriverOpenAICompat, DriverOllama, DriverLocalLLM:
	}
	return HTTPRequest{Method: http.MethodPost, URL: base + "/v1/chat/completions", Body: body,
		Headers: map[string]string{"Authorization": "Bearer " + key, "content-type": "application/json"}}
}
