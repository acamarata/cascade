package graph

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// Envelope holds a nonce and base64-encoded changed files.
type Envelope struct {
	Nonce string            `json:"nonce"`
	Files map[string]string `json:"files"`
}

// Frame encodes the nonce and files into a JSON envelope with base64 file contents.
func Frame(nonce string, files map[string][]byte) ([]byte, error) {
	if nonce == "" {
		return nil, errors.New("empty nonce")
	}
	encoded := make(map[string]string, len(files))
	for name, data := range files {
		encoded[name] = base64.StdEncoding.EncodeToString(data)
	}
	env := Envelope{
		Nonce: nonce,
		Files: encoded,
	}
	return json.Marshal(env)
}

// ParseFrame decodes a framed envelope back into its nonce and original file bytes.
func ParseFrame(frame []byte) (string, map[string][]byte, error) {
	var env Envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		return "", nil, err
	}
	decoded := make(map[string][]byte, len(env.Files))
	for name, b64 := range env.Files {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return "", nil, err
		}
		decoded[name] = data
	}
	return env.Nonce, decoded, nil
}

// NodeIdentity derives the node name from a lane in the LM@X or CMD@X pattern.
func NodeIdentity(lane string) (string, error) {
	idx := strings.Index(lane, "@")
	if idx < 0 {
		return "", errors.New("lane missing @ separator")
	}
	prefix := lane[:idx]
	if prefix != "LM" && prefix != "CMD" {
		return "", errors.New("unsupported lane prefix")
	}
	node := lane[idx+1:]
	if node == "" {
		return "", errors.New("empty node identity")
	}
	return node, nil
}
