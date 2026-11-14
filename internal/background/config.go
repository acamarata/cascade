// Package background provides configuration parsing for agy.
package background

import (
	"bytes"
	"encoding/json"
	"errors"
)

// ErrInvalidConfig is returned when the agy configuration is invalid.
var ErrInvalidConfig = errors.New("invalid agy config")

// ErrDeniedAction is returned when an action is blocked by the configuration.
var ErrDeniedAction = errors.New("M0747: action denied")

// AgyConfig holds the parsed agy configuration.
type AgyConfig struct {
	// DeniedActions lists action strings that are not permitted.
	DeniedActions []string `json:"denied_actions"`
}

// ParseConfig parses raw JSON bytes into an AgyConfig.
// It requires a single JSON object with no trailing tokens.
// Empty or whitespace-only input, malformed JSON, non-string array entries,
// empty or whitespace-only string entries, and duplicate exact strings all
// return nil and ErrInvalidConfig. Missing denied_actions or a JSON null
// value yield a nil slice; an empty JSON array yields a non-nil empty slice.
func ParseConfig(data []byte) (*AgyConfig, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, ErrInvalidConfig
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))

	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, ErrInvalidConfig
	}

	// Ensure no trailing JSON token
	if dec.More() {
		return nil, ErrInvalidConfig
	}

	cfg := &AgyConfig{}

	rawDenied, ok := raw["denied_actions"]
	if !ok {
		// missing field -> nil slice
		cfg.DeniedActions = nil
	} else {
		// Check if it's JSON null
		if string(rawDenied) == "null" {
			cfg.DeniedActions = nil
		} else {
			var arr []json.RawMessage
			if err := json.Unmarshal(rawDenied, &arr); err != nil {
				return nil, ErrInvalidConfig
			}
			result := make([]string, 0, len(arr))
			seen := make(map[string]struct{}, len(arr))
			for _, item := range arr {
				var s string
				if err := json.Unmarshal(item, &s); err != nil {
					return nil, ErrInvalidConfig
				}
				// Reject empty or whitespace-only strings
				if len(bytes.TrimSpace([]byte(s))) == 0 {
					return nil, ErrInvalidConfig
				}
				// Reject duplicates
				if _, exists := seen[s]; exists {
					return nil, ErrInvalidConfig
				}
				seen[s] = struct{}{}
				result = append(result, s)
			}
			cfg.DeniedActions = result
		}
	}

	return cfg, nil
}

// Check returns ErrDeniedAction if the given action is in the denied_actions list.
// It returns nil if the action is permitted.
func (c *AgyConfig) Check(action string) error {
	if c == nil {
		return nil
	}
	for _, d := range c.DeniedActions {
		if d == action {
			return ErrDeniedAction
		}
	}
	return nil
}
