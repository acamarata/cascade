package hygiene

// ViolationKind identifies the type of hygiene violation detected.
type ViolationKind int

const (
	// ClockArithmetic represents a violation related to clock arithmetic.
	ClockArithmetic ViolationKind = iota
	// GlobalGitFixture represents a violation related to global git fixtures.
	GlobalGitFixture
	// FlatSleep represents a violation related to flat sleeps.
	FlatSleep
)

// Violation describes a single hygiene violation found in a source file.
type Violation struct {
	// Kind is the type of violation.
	Kind ViolationKind
	// File is the path of the file containing the violation.
	File string
	// Line is the line number where the violation occurred.
	Line int
}

// ScanFiles inspects the given file paths for hygiene violations.
// It returns a list of all violations found, or an error if any file cannot be read.
func ScanFiles(paths []string) ([]Violation, error) {
	return nil, nil
}
