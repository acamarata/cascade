// Package v1 uses this file to parse the two observed v1 memory forms: named plain
// markdown collections and three-field YAML-frontmatter records.
// Purpose: parse complete v1 memory sources before any destination write.
// Inputs: files under a v1 .cascade/memory directory.
// Outputs: fully translated memory.ImportMutation values, never partial data.
// Constraints: real v1 forms only; unknown files, keys, kinds and versions
// refuse the whole directory before any destination write.
// SPORT: migration/v1/memory/ADD (P1-E26-W10-S53-T1).
package v1

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

const maxMemoryFileBytes = 16 << 20

var plainMemoryFiles = map[string]bool{
	"decisions": true,
	"lessons":   true,
	"patterns":  true,
}

func readMemorySource(root string) ([]memory.ImportMutation, error) {
	if strings.TrimSpace(root) == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "migration v1 memory: source root is required")
	}
	dir := filepath.Join(root, ".cascade", "memory")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cascade.Wrap(cascade.KindNotFound, err, "migration v1 memory: read .cascade/memory")
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "migration v1 memory: read .cascade/memory")
	}
	out := make([]memory.ImportMutation, 0, len(entries))
	for _, entry := range entries {
		mutation, err := readMemoryFile(dir, entry)
		if err != nil {
			return nil, err
		}
		out = append(out, mutation)
	}
	return out, nil
}

func readMemoryFile(dir string, entry fs.DirEntry) (memory.ImportMutation, error) {
	name := entry.Name()
	if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
		return memory.ImportMutation{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownInput,
			"migration v1 memory: %q is not a regular file", name)
	}
	stem, tombstone, ok := memoryFileStem(name)
	if !ok {
		return memory.ImportMutation{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownInput,
			"migration v1 memory: unsupported file %q", name)
	}
	path := filepath.Join(dir, name)
	info, err := entry.Info()
	if err != nil {
		return memory.ImportMutation{}, cascade.Wrap(cascade.KindUnavailable, err, "migration v1 memory: stat source")
	}
	if info.Size() > maxMemoryFileBytes {
		return memory.ImportMutation{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownInput,
			"migration v1 memory: %q exceeds the %d-byte limit", name, maxMemoryFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return memory.ImportMutation{}, cascade.Wrap(cascade.KindUnavailable, err, "migration v1 memory: read source")
	}
	return buildMemoryMutation(stem, name, data, tombstone, info)
}

func memoryFileStem(name string) (string, bool, bool) {
	if strings.HasSuffix(name, ".md.tombstone") {
		return strings.TrimSuffix(name, ".md.tombstone"), true, true
	}
	if strings.HasSuffix(name, ".md") {
		return strings.TrimSuffix(name, ".md"), false, true
	}
	return "", false, false
}

func buildMemoryMutation(stem, fileName string, data []byte, tombstone bool, info fs.FileInfo) (memory.ImportMutation, error) {
	if err := memory.ValidateName(stem); err != nil {
		return memory.ImportMutation{}, cascade.Wrap(cascade.KindInvalidInput, err, "migration v1 memory: invalid filename")
	}
	kind, description, err := parseV1Memory(stem, data, tombstone)
	if err != nil {
		return memory.ImportMutation{}, err
	}
	return memory.ImportMutation{
		Entry: memory.MemoryEntry{
			Name: stem, Kind: kind, Description: description,
			Body: string(data), ScopeRef: "migration:v1", Confidence: 1,
			Provenance: memory.Provenance{Origin: memory.OriginFile},
		},
		Tombstone: tombstone, SourceRef: "v1:.cascade/memory/" + fileName,
		SourceTime: info.ModTime(),
	}, nil
}

func parseV1Memory(stem string, data []byte, tombstone bool) (memory.MemoryKind, string, error) {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", "", cascade.Wrap(cascade.KindIntegrity, ErrMalformedInput,
			"migration v1 memory: source is not valid UTF-8 text")
	}
	if tombstone && len(data) == 0 {
		if !plainMemoryFiles[stem] {
			return "", "", cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownInput,
				"migration v1 memory: empty tombstone %q has no recoverable kind", stem)
		}
		return memory.KindProject, "", nil
	}
	if bytes.HasPrefix(data, []byte("---\n")) || bytes.HasPrefix(data, []byte("---\r\n")) {
		kind, description, _, err := parseMemoryFrontmatter(data)
		if err != nil {
			return "", "", err
		}
		return kind, description, nil
	}
	if !plainMemoryFiles[stem] {
		return "", "", cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownInput,
			"migration v1 memory: plain markdown file %q has no known v1 type", stem+".md")
	}
	heading, _, ok := strings.Cut(string(data), "\n")
	heading = strings.TrimSuffix(heading, "\r")
	if !ok || !strings.HasPrefix(heading, "# ") || strings.TrimSpace(heading[2:]) == "" {
		return "", "", cascade.Wrap(cascade.KindIntegrity, ErrMalformedInput,
			"migration v1 memory: plain collection is missing its level-one heading")
	}
	return memory.KindProject, strings.TrimSpace(heading[2:]), nil
}

func parseMemoryFrontmatter(data []byte) (memory.MemoryKind, string, string, error) {
	header, err := memoryHeaderLines(string(data))
	if err != nil {
		return "", "", "", err
	}
	fields, err := parseMemoryFields(header)
	if err != nil {
		return "", "", "", err
	}
	if raw, ok := fields["format"]; ok {
		version, convErr := strconv.Atoi(raw)
		if convErr != nil {
			return "", "", "", cascade.Wrap(cascade.KindIntegrity, ErrMalformedInput,
				"migration v1 memory: format is not an integer")
		}
		if version != 1 {
			return "", "", "", cascade.Wrapf(cascade.KindUnsupported, ErrVersionMismatch,
				"migration v1 memory: format %d is not supported", version)
		}
	}
	for _, required := range []string{"name", "description", "type"} {
		if _, ok := fields[required]; !ok {
			return "", "", "", cascade.Wrapf(cascade.KindIntegrity, ErrMalformedInput,
				"migration v1 memory: frontmatter is missing %q", required)
		}
	}
	kind, err := memory.ParseKind(fields["type"])
	if err != nil {
		return "", "", "", cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownInput,
			"migration v1 memory: unknown type %q", fields["type"])
	}
	return kind, fields["description"], fields["name"], nil
}

func memoryHeaderLines(text string) ([]string, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return nil, cascade.Wrap(cascade.KindIntegrity, ErrMalformedInput,
			"migration v1 memory: frontmatter opening fence is invalid")
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return lines[1:i], nil
		}
	}
	return nil, cascade.Wrap(cascade.KindIntegrity, ErrMalformedInput,
		"migration v1 memory: frontmatter closing fence is missing")
}

func parseMemoryFields(lines []string) (map[string]string, error) {
	fields := make(map[string]string, len(lines))
	for _, line := range lines {
		key, value, ok := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, cascade.Wrap(cascade.KindIntegrity, ErrMalformedInput,
				"migration v1 memory: invalid frontmatter field")
		}
		if key != "name" && key != "description" && key != "type" && key != "format" {
			return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownInput,
				"migration v1 memory: unknown frontmatter key %q", key)
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedInput,
				"migration v1 memory: duplicate frontmatter key %q", key)
		}
		fields[key] = value
	}
	return fields, nil
}
