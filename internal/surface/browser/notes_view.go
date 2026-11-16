package browser

// ChainEntry represents a single entry in an instruction chain.
type ChainEntry struct {
	Name string
	Path string
}

// Row represents a single row in the notes view.
type Row struct {
	Name string
	Path string
}

// Opener is an injected interface for opening note paths.
type Opener interface {
	Open(path string) error
}

// NotesView returns a new row slice copying every Name and Path verbatim
// in input order, including duplicates and empty paths. A nil or empty
// chain returns nil. The opener parameter is retained for API compatibility
// but unused by NotesView.
func NotesView(chain []ChainEntry, opener Opener) []Row {
	if len(chain) == 0 {
		return nil
	}
	rows := make([]Row, len(chain))
	for i, entry := range chain {
		rows[i] = Row{
			Name: entry.Name,
			Path: entry.Path,
		}
	}
	return rows
}
