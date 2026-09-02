// Package violation (rand_alias_violation.go) proves the same alias
// evasion for the global math/rand source, aliased as "r".
package violation

import r "math/rand"

func AliasedIntn() int {
	return r.Intn(10)
}
