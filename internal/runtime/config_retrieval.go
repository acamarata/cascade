package runtime

// Purpose: the [retrieval] config surface — the typed retrievalSection,
//   its top-level key check, and the strict parser for
//   [retrieval.reranker] (08-INIT-CONFIG-SPEC §3, dotted table form
//   [retrieval.reranker] enabled = false). sources[] and
//   [retrieval.fusion] are parsed by config_retrieval_fusion.go. Split
//   out of config.go rather than added to it per R-14.117/Art.10.3
//   (config.go is at the 300-line cap).
// Inputs: the decoded generic config tree.
// Outputs: the typed retrievalSection, or a *ConfigError.
// Constraints: parsing is strict and fails closed everywhere under
//   [retrieval]: an unrecognised key or a wrong type is a hard typed
//   error, never a warning that leaves the pipeline quietly on its
//   defaults. The one key deliberately passed over is
//   retrieval.fusion.enabled, which F/S-12.T6 owns end-to-end (R-16.49).
//   The raw [retrieval] table also survives verbatim in Config.Extra, so
//   the generic round-trip and the `cascade config` effective view keep
//   working without knowing this file exists.
// SPORT: runtime/config.retrieval (ADD, P1-E06-W2-S12-T3; extended by
//   P1-E06-W2-S12-T4 with sources[] and [retrieval.fusion]).

// retrievalSection is the typed [retrieval] table: the whole 08 §3 key
// set (R-14.18) except retrieval.fusion.enabled, which is F/S-12.T6's.
type retrievalSection struct {
	// Sources are the registered corpus source locators, in file order.
	Sources []string
	// Fusion is [retrieval.fusion] (config_retrieval_fusion.go).
	Fusion fusionSection
	// Reranker is [retrieval.reranker].
	Reranker rerankerSection
}

// retrievalKeys is the set of keys [retrieval] may hold. It is a closed
// set on purpose: a misspelled section key is a tuning the operator
// believes is live, so it is refused rather than preserved silently in
// Extra alongside the keys that do take effect.
var retrievalKeys = map[string]bool{"sources": true, "fusion": true, "reranker": true}

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

// parseRetrievalSection reads the whole [retrieval] table out of tree.
//
// It is deliberately strict and fails closed. A user who wrote
// `[retrieval.reranker] enabled = "yes"`, or misspelled the key, asked
// for reranking; loading such a file successfully with the stage silently
// off would tell them reranking is running when nothing is. So a
// malformed value, a misspelled key inside [retrieval.reranker], and a
// [retrieval] or [retrieval.reranker] that is not a table are all hard
// typed errors that refuse the whole config.
//
// Absence is not malformation: no file, no [retrieval], and a [retrieval]
// holding only some of its keys all resolve to the shipped defaults with
// no error and no warning.
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
	for key := range table {
		if !retrievalKeys[key] {
			return out, &ConfigError{
				Field:  "retrieval." + key,
				Reason: "unrecognised key in [retrieval]",
			}
		}
	}
	sources, err := parseSources(table)
	if err != nil {
		return out, err
	}
	out.Sources = sources
	fusion, err := parseFusion(table)
	if err != nil {
		return out, err
	}
	out.Fusion = fusion
	return parseReranker(table, out)
}

// parseReranker reads [retrieval.reranker] into out.
func parseReranker(table map[string]interface{}, out retrievalSection) (retrievalSection, error) {
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
