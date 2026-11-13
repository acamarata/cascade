package docket

// SchemaColumnTypes returns a fresh map of the standard text columns to the
// TEXT schema type. The returned map contains exactly the keys "title",
// "objective", "acceptance", and "verify", each mapped to "TEXT". Mutating
// the returned map does not affect subsequent calls.
func SchemaColumnTypes() map[string]string {
	return map[string]string{
		"title":      "TEXT",
		"objective":  "TEXT",
		"acceptance": "TEXT",
		"verify":     "TEXT",
	}
}
