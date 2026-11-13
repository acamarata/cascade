// Package hygiene provides scratch path utilities for fleet operations.
package hygiene

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// scratchBase is the root every scratch directory lives under, in slash form:
// <os temp dir>/cascade-scratch, so no host path is built in.
var scratchBase = path.Clean(filepath.ToSlash(filepath.Join(os.TempDir(), "cascade-scratch")))

var validTopic = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ScratchPath returns the scratch directory for a given topic and date.
// The date must be in YYYY-MM-DD format. The topic must contain only
// alphanumeric characters, hyphens, or underscores.
func ScratchPath(topic, date string) (string, error) {
	if topic == "" || !validTopic.MatchString(topic) {
		return "", errors.New("invalid topic")
	}
	parts := strings.Split(date, "-")
	if len(parts) != 3 || len(parts[0]) != 4 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		return "", errors.New("invalid date format, expected YYYY-MM-DD")
	}
	dateCompact := parts[0] + parts[1] + parts[2]
	dir := fmt.Sprintf("%s/%s-%s/", scratchBase, topic, dateCompact)
	return dir, nil
}

// ValidateScratch checks that the given path is within a valid scratch
// directory under <scratch base>/<topic>-<YYYYMMDD>/.
// It rejects paths outside this hierarchy, including escapes via "..".
func ValidateScratch(p string) error {
	cleaned := path.Clean(filepath.ToSlash(p))
	if !strings.HasPrefix(cleaned, scratchBase+"/") {
		return errors.New("path is outside scratch base")
	}
	rel := cleaned[len(scratchBase)+1:]
	parts := strings.SplitN(rel, "/", 2)
	if len(parts) < 2 || parts[1] == "" {
		return errors.New("path is not under a topic directory")
	}
	topicDir := parts[0]
	idx := strings.LastIndex(topicDir, "-")
	if idx <= 0 {
		return errors.New("missing date suffix in topic directory")
	}
	topic := topicDir[:idx]
	datePart := topicDir[idx+1:]
	if !validTopic.MatchString(topic) {
		return errors.New("invalid topic in path")
	}
	if len(datePart) != 8 {
		return errors.New("invalid date suffix length")
	}
	for _, c := range datePart {
		if c < '0' || c > '9' {
			return errors.New("non-digit in date suffix")
		}
	}
	return nil
}
