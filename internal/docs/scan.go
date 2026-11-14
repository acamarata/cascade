// Package docs provides repository documentation scanning.
package docs

import (
	"os"
	"path/filepath"
	"strings"
)

// Entry represents a single documentation file found during a scan.
type Entry struct {
	Repo  string
	Path  string
	Depth int
}

// Scan traverses each repository path in repos up to a maximum depth of 4
// and returns collected documentation entries grouped by repository path.
// An empty input slice returns an empty non-nil map and nil error.
func Scan(repos []string) (map[string][]Entry, error) {
	result := make(map[string][]Entry)
	for _, repo := range repos {
		err := filepath.Walk(repo, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, relErr := filepath.Rel(repo, path)
			if relErr != nil {
				return relErr
			}
			depth := 0
			if rel != "." {
				depth = len(strings.Split(filepath.ToSlash(rel), "/"))
			}
			if info.IsDir() {
				if depth >= 4 {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(info.Name(), ".md") && depth <= 4 {
				result[repo] = append(result[repo], Entry{
					Repo:  repo,
					Path:  path,
					Depth: depth,
				})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
