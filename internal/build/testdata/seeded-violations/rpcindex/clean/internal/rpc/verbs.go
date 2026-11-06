package rpc

const Bare = "fixture.bare"

var _ = DeclareAccess(Bare, AccessOpen)
var rules = []struct {
	method string
	rule   ElevationRule
}{
	{"fixture.qualified", ElevationRule{Always: true}},
}

func init() {
	for _, row := range rules {
		RegisterElevatedVerb(row.method, row.rule)
	}
}
