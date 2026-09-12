// Package secrets uses this file to frame quoted multiline vault.env records.
// Purpose: combine physical input lines into bounded logical assignments.
// Inputs: raw vault.env bytes.
// Outputs: logical assignments with their first physical line number.
// Constraints: bounded records; no error contains source content.
// SPORT: secrets/vault-import/CHANGED (P1-E26-W10-S53-T1).
package secrets

import (
	"bufio"
	"bytes"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

type envRecord struct {
	text string
	line int
}

func scanEnvRecords(data []byte) ([]envRecord, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxEnvLineLen)
	var records []envRecord
	var pending string
	startLine, lineNo := 0, 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if pending != "" {
			pending += "\n" + line
			if len(pending) > maxEnvLineLen {
				return nil, errEnvLine(startLine, "exceeds the maximum assignment length")
			}
			if quotedAssignmentClosed(pending) {
				records = append(records, envRecord{text: pending, line: startLine})
				pending = ""
			}
			continue
		}
		if quotedAssignmentOpen(line) && !quotedAssignmentClosed(line) {
			pending, startLine = line, lineNo
			continue
		}
		records = append(records, envRecord{text: line, line: lineNo})
	}
	if err := scanner.Err(); err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err,
			"secrets: vault.env line %d could not be read", lineNo+1)
	}
	if pending != "" {
		return nil, errEnvLine(startLine, "has an unterminated multi-line quoted value")
	}
	return records, nil
}

func quotedAssignmentOpen(line string) bool {
	raw, ok := assignmentValue(line)
	return ok && len(raw) > 0 && (raw[0] == '\'' || raw[0] == '"')
}

func quotedAssignmentClosed(line string) bool {
	raw, ok := assignmentValue(line)
	if !ok || len(raw) < 2 || (raw[0] != '\'' && raw[0] != '"') {
		return false
	}
	end := len(raw) - 1
	if raw[end] != raw[0] {
		return false
	}
	if raw[0] == '\'' {
		return true
	}
	backslashes := 0
	for i := end - 1; i >= 0 && raw[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 0
}

func assignmentValue(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	trimmed = strings.TrimPrefix(trimmed, "export ")
	_, raw, ok := strings.Cut(trimmed, "=")
	return strings.TrimSpace(raw), ok
}
