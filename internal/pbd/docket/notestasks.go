// Package docket provides note management utilities.
package docket

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotesDir is returned when the notes directory does not exist.
var ErrNotesDir = errors.New("ERR_NOTES_DIR")

// TaskEntry represents a single task found in a markdown note.
type TaskEntry struct {
	// Path is the slash-separated path relative to the notes directory.
	Path string
	// Line is the 1-based line number where the task appears.
	Line int
	// Text is the full text of the task line.
	Text string
}

// scanTaskFile returns the task lines of one markdown file, labelled with rel.
func scanTaskFile(path, rel string) (entries []TaskEntry, err error) {
	f, openErr := os.Open(path)
	if openErr != nil {
		return nil, openErr
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "- [ ]") || strings.HasPrefix(trimmed, "- [x]") {
			entries = append(entries, TaskEntry{
				Path: rel,
				Line: lineNum,
				Text: line,
			})
		}
	}
	return entries, scanner.Err()
}

// NotesTasks returns every task across all .md files in notesDir.
// A task is a line starting with "- [ ]" or "- [x]".
// Results are sorted by path, then by line number.
// If notesDir does not exist, it returns ErrNotesDir.
func NotesTasks(notesDir string) ([]TaskEntry, error) {
	info, err := os.Stat(notesDir)
	if err != nil || !info.IsDir() {
		return nil, ErrNotesDir
	}

	var entries []TaskEntry

	err = filepath.Walk(notesDir, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fi.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(fi.Name()), ".md") {
			return nil
		}

		rel, relErr := filepath.Rel(notesDir, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		fileEntries, scanErr := scanTaskFile(path, rel)
		if scanErr != nil {
			return scanErr
		}
		entries = append(entries, fileEntries...)
		return nil
	})

	if err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Path != entries[j].Path {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].Line < entries[j].Line
	})

	return entries, nil
}
