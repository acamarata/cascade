package runtime

// Purpose: the [retrieval] config surface this ticket owns — the single
//   key retrieval.reranker.enabled (08-INIT-CONFIG-SPEC §3, dotted table
//   form [retrieval.reranker] enabled = false), its typed section, and
//   its strict parser. Split out of config.go rather than added to it per
//   R-14.117/Art.10.3 (config.go is at the 300-line cap).
// Inputs: the decoded generic config tree.
// Outputs: the typed retrievalSection, or a *ConfigError.
// Constraints: Art.1 — this ticket owns [retrieval.reranker] and NOTHING
//   else under [retrieval]. sources[], fusion.k and fusion.weights are
//   S-12.T4's; they are left in Config.Extra untouched, unvalidated and
//   undefaulted, exactly as the loader treats every unowned section.
//   Parsing is strict inside the table it does own: an unrecognised key
//   or a wrong type is a hard typed error, never a warning that leaves
//   the stage quietly off.
// SPORT: runtime/config.retrieval (ADD, P1-E06-W2-S12-T3).

// retrievalSection is the typed slice of [retrieval] this ticket owns.
type retrievalSection struct {
	Reranker rerankerSection
}

// rerankerSection is [retrieval.reranker]. Enabled is the gate on the
// optional post-fusion reranker stage (internal/retrieval/rrf). Its
// default is false and is the zero value on purpose: an absent config
// file, an absent section and an explicit `enabled = false` must all mean
// the same thing, so there is no *bool and no tri-state here.
type rerankerSection struct {
	Enabled bool `toml:"enabled"`
}

// RerankerEnabled reports whether the optional reranker stage is switched
// on. It is the one accessor retrieval reads, so no caller has to know
// where in the config tree the key lives.
func (c *Config) RerankerEnabled() bool {
	if c == nil {
		return false
	}
	return c.Retrieval.Reranker.Enabled
}

// parseRetrievalSection reads [retrieval.reranker] out of tree.
//
// It is deliberately strict and fails closed. A user who wrote
// `[retrieval.reranker] enabled = "yes"`, or misspelled the key, asked
// for reranking; loading such a file successfully with the stage silently
// off would tell them reranking is running when nothing is. So a
// malformed value, a misspelled key inside [retrieval.reranker], and a
// [retrieval] or [retrieval.reranker] that is not a table are all hard
// typed errors that refuse the whole config.
//
// Absence is not malformation: no file, no [retrieval], or a [retrieval]
// holding only S-12.T4's keys all resolve to enabled = false with no
// error and no warning.
func parseRetrievalSection(tree map[string]interface{}) (retrievalSection, error) {
	out := retrievalSection{}
	raw, ok := tree["retrieval"]
	if !ok {
		return out, nil
	}
	table, ok := raw.(map[string]interface{})
	if !ok {
		return out, &ConfigError{Field: "retrieval", Reason: "must be a table"}
	}
	rawReranker, ok := table["reranker"]
	if !ok {
		return out, nil
	}
	reranker, ok := rawReranker.(map[string]interface{})
	if !ok {
		return out, &ConfigError{Field: "retrieval.reranker", Reason: "must be a table"}
	}
	for k := range reranker {
		if k != "enabled" {
			return out, &ConfigError{
				Field:  "retrieval.reranker." + k,
				Reason: "unrecognised key in [retrieval.reranker]",
			}
		}
	}
	if v, ok := reranker["enabled"]; ok {
		enabled, ok := v.(bool)
		if !ok {
			return out, &ConfigError{Field: "retrieval.reranker.enabled", Reason: "must be a boolean"}
		}
		out.Reranker.Enabled = enabled
	}
	return out, nil
}
