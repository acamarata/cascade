// Package conform holds the Port conformance suite. Each Port ships its
// cases with the Port; swarms build against the fake and prove the real
// module with this suite.
package conform

// Suite runs the conformance cases a Port ships against an implementation.
type Suite struct{}
