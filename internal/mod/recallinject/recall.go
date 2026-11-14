// Package recallinject provides utilities to classify file paths and tag them using an injected classification function.
package recallinject

import "errors"

// ErrNoClassifier is returned when NewRecall is called with a nil classifier.
var ErrNoClassifier = errors.New("no classifier provided")

// ClassifyFunc is a function that classifies a file path into a category string.
type ClassifyFunc func(path string) string

// Recall uses an injected classifier to tag file paths.
type Recall struct {
	classify ClassifyFunc
}

// NewRecall creates a new Recall instance with the given classifier.
// It returns ErrNoClassifier if the provided classifier is nil.
func NewRecall(classify ClassifyFunc) (*Recall, error) {
	if classify == nil {
		return nil, ErrNoClassifier
	}
	return &Recall{classify: classify}, nil
}

// Tag classifies the given paths and returns a map from path to class.
func (r *Recall) Tag(paths []string) map[string]string {
	result := make(map[string]string, len(paths))
	for _, p := range paths {
		result[p] = r.classify(p)
	}
	return result
}
