package egress

import "sort"

// sortVaultValues orders values longest first, then by name. Longest
// first matters: if one stored value contains another, replacing the
// shorter one first would leave the longer one's remaining bytes on the
// wire. Name ascending makes the order total, so the same vault always
// substitutes in the same sequence.
func sortVaultValues(values []vaultValue) {
	sort.Slice(values, func(i, j int) bool {
		if len(values[i].value) != len(values[j].value) {
			return len(values[i].value) > len(values[j].value)
		}
		return values[i].name < values[j].name
	})
}
