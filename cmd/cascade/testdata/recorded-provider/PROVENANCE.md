# recorded-provider fixture provenance

`ollama-chat.json` is the body the local recorded-provider HTTP server in
`cmd/cascade/conductor_fanout_*_test.go` returns for every `POST /api/chat`.
The tests register that server as an Ollama-driver provider in the real
providers registry, so the real `providers/ollama` driver and the real
`providers/transport` HTTP transport decode it.

The file is one live response from a local Ollama server, stored byte for
byte as the server returned it.

| Field | Value |
|---|---|
| Provider | Ollama `/api/chat`, `stream: false` (local server on 127.0.0.1:11434) |
| Server version | ollama 0.35.1 |
| Model | `qwen2.5:0.5b` (id `a8b0c5157701`, Apache-2.0) |
| Model digest | `a8b0c51577010a279d933d14c2a8ab4b268079d44c5c8830c0a93900f1827c67` |
| Captured | 2026-10-04T17:49:18Z |
| Capture command | `curl -s http://127.0.0.1:11434/api/chat -d '{"model":"qwen2.5:0.5b","messages":[{"role":"user","content":"hi"}],"stream":false}'` |
| File sha256 | `00d939bb096a3e40b6b7bb02cebd05236681ecf74185fe2919211f99cf4f17bf` |
| Credential / personal data | none (local server, no key; the prompt was "hi") |

To refresh it, run the capture command against a local Ollama with the same
model, write the output to `ollama-chat.json` unchanged, and update every row
above.
