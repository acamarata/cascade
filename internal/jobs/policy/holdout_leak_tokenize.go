package policy

// tokenize splits a proposal into maximal runs of ASCII letters, digits,
// underscores and hyphens. Any other character acts as a delimiter.
func tokenize(proposal string) []string {
	var tokens []string
	var current []byte
	for i := 0; i < len(proposal); i++ {
		c := proposal[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			current = append(current, c)
		} else {
			if len(current) > 0 {
				tokens = append(tokens, string(current))
				current = current[:0]
			}
		}
	}
	if len(current) > 0 {
		tokens = append(tokens, string(current))
	}
	return tokens
}
