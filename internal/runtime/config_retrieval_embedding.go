package runtime

// Purpose: the [retrieval.embedding] config surface: the closed key set,
//   the strict checks Load runs over it, and RetrievalEmbedding, the one
//   accessor the daemon's embedder composition reads at start.
// Inputs: the decoded generic config tree (Config.Extra).
// Outputs: EmbeddingSettings, or a *ConfigError from Load.
// Constraints: the six settings are read by name, one key at a time; there
//   is no whole-section decode. local_socket is a guarded key (P1-SEC-36):
//   the key-name check knows its name and never reads its value, and its
//   only reader is RetrievalEmbeddingLocalSocket over the admitted
//   EffectiveConfig. A lane requires a model and a width, and the timeout
//   must lie in (0, 60s].
// SPORT: runtime/config.retrieval.embedding (ADD, P1-CPA-11).

import (
	"fmt"
	"time"
)

// DefaultEmbeddingLocalModel and DefaultEmbeddingLocalDimensions are the
// embedding space the local BGE-M3 sidecar is expected to serve when
// local_model and local_dimensions are absent.
const (
	DefaultEmbeddingLocalModel      = "bge-m3"
	DefaultEmbeddingLocalDimensions = 1024
)

// DefaultEmbeddingTimeout bounds one embed call when timeout is absent;
// MaxEmbeddingTimeout is the largest value the key accepts.
const (
	DefaultEmbeddingTimeout = 30 * time.Second
	MaxEmbeddingTimeout     = 60 * time.Second
)

// EmbeddingSettings is [retrieval.embedding] minus local_socket, whose
// admitted value only RetrievalEmbeddingLocalSocket returns.
type EmbeddingSettings struct {
	// LocalModel and LocalDimensions are the sidecar's embedding space.
	LocalModel      string
	LocalDimensions int
	// Lane names the conductor lane embeddings run on; "" means none.
	Lane string
	// Model and Dimensions are the lane's embedding space.
	Model      string
	Dimensions int
	// Timeout bounds one embed call.
	Timeout time.Duration
}

// RetrievalEmbedding returns the [retrieval.embedding] settings with the
// shipped defaults filled in. Load has already refused a malformed table,
// so a value that does not parse here reads as its default.
func (c *Config) RetrievalEmbedding() EmbeddingSettings {
	if c == nil {
		return defaultEmbeddingSettings()
	}
	s, err := readEmbeddingSettings(retrievalEmbeddingValue(c.Extra))
	if err != nil {
		return defaultEmbeddingSettings()
	}
	return s
}

// defaultEmbeddingSettings is the settings value of an absent table.
func defaultEmbeddingSettings() EmbeddingSettings {
	return EmbeddingSettings{
		LocalModel: DefaultEmbeddingLocalModel, LocalDimensions: DefaultEmbeddingLocalDimensions,
		Timeout: DefaultEmbeddingTimeout,
	}
}

// checkRetrievalEmbedding is Load's strict check of [retrieval.embedding];
// raw is retrievalEmbeddingValue's result.
func checkRetrievalEmbedding(raw interface{}) error {
	_, err := readEmbeddingSettings(raw)
	return err
}

// readEmbeddingSettings reads the settings key by key, then applies the
// lane rule. A nil raw is an absent table.
func readEmbeddingSettings(raw interface{}) (EmbeddingSettings, error) {
	out := defaultEmbeddingSettings()
	if raw == nil {
		return out, nil
	}
	table, ok := raw.(map[string]interface{})
	if !ok {
		return out, &ConfigError{Field: "retrieval.embedding", Reason: "must be a table"}
	}
	for key := range table {
		switch key {
		case "local_socket", "local_model", "local_dimensions", "lane", "model", "dimensions", "timeout":
		default:
			return out, &ConfigError{Field: "retrieval.embedding." + key, Reason: "unrecognised key in [retrieval.embedding]"}
		}
	}
	for _, key := range []string{"local_model", "local_dimensions", "lane", "model", "dimensions", "timeout"} {
		value, present := table[key]
		if !present {
			continue
		}
		if err := out.set(key, value); err != nil {
			return defaultEmbeddingSettings(), err
		}
	}
	return out, checkEmbeddingLane(out)
}

// set stores one key's value. local_socket is known by name and skipped
// without reading its value; an unknown name is refused.
func (s *EmbeddingSettings) set(key string, value interface{}) error {
	var err error
	switch key {
	case "local_socket":
		return nil
	case "local_model":
		s.LocalModel, err = embeddingString(key, value)
	case "local_dimensions":
		s.LocalDimensions, err = embeddingInt(key, value)
	case "lane":
		s.Lane, err = embeddingString(key, value)
	case "model":
		s.Model, err = embeddingString(key, value)
	case "dimensions":
		s.Dimensions, err = embeddingInt(key, value)
	case "timeout":
		s.Timeout, err = embeddingTimeout(value)
	default:
		return &ConfigError{Field: "retrieval.embedding." + key, Reason: "unrecognised key in [retrieval.embedding]"}
	}
	return err
}

// checkEmbeddingLane refuses a lane with no model or width.
func checkEmbeddingLane(s EmbeddingSettings) error {
	if s.Lane == "" {
		return nil
	}
	if s.Model == "" {
		return &ConfigError{Field: "retrieval.embedding.model", Reason: "a lane requires a model"}
	}
	if s.Dimensions <= 0 {
		return &ConfigError{Field: "retrieval.embedding.dimensions", Reason: "a lane requires a positive dimensions"}
	}
	return nil
}

// embeddingString reads one string value.
func embeddingString(key string, value interface{}) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", &ConfigError{Field: "retrieval.embedding." + key, Reason: "must be a string"}
	}
	return s, nil
}

// embeddingInt reads one positive integer value.
func embeddingInt(key string, value interface{}) (int, error) {
	n, ok := value.(int64)
	if !ok || n <= 0 || n > 1<<20 {
		return 0, &ConfigError{Field: "retrieval.embedding." + key, Reason: "must be a positive integer"}
	}
	return int(n), nil
}

// embeddingTimeout reads timeout, a duration string in (0, 60s].
func embeddingTimeout(value interface{}) (time.Duration, error) {
	s, ok := value.(string)
	if !ok {
		return 0, &ConfigError{Field: "retrieval.embedding.timeout", Reason: "must be a duration string such as \"30s\""}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 || d > MaxEmbeddingTimeout {
		return 0, &ConfigError{Field: "retrieval.embedding.timeout",
			Reason: fmt.Sprintf("must be a duration above 0 and at most %s", MaxEmbeddingTimeout)}
	}
	return d, nil
}
