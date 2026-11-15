package atlas

import (
	"errors"
	"testing"
)

func TestLegacyBodyReferenceFailsD4045(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		wantErr error
	}{
		{
			name:    "blank SQL returns ErrBodyInput",
			sql:     "",
			wantErr: ErrBodyInput,
		},
		{
			name:    "select body from notes",
			sql:     "SELECT body FROM notes",
			wantErr: ErrLegacyBody,
		},
		{
			name:    "select body from notes lowercase",
			sql:     "select body from notes",
			wantErr: ErrLegacyBody,
		},
		{
			name:    "select body from notes mixed case",
			sql:     "Select Body From Notes",
			wantErr: ErrLegacyBody,
		},
		{
			name:    "select with extra whitespace",
			sql:     "  SELECT   body   FROM   notes  ",
			wantErr: ErrLegacyBody,
		},
		{
			name:    "qualified notes.body",
			sql:     "SELECT notes.body FROM notes",
			wantErr: ErrLegacyBody,
		},
		{
			name:    "double quoted identifiers",
			sql:     `SELECT "body" FROM "notes"`,
			wantErr: ErrLegacyBody,
		},
		{
			name:    "insert into notes with body",
			sql:     "INSERT INTO notes (body) VALUES ('test')",
			wantErr: ErrLegacyBody,
		},
		{
			name:    "update notes set body",
			sql:     "UPDATE notes SET body = 'test'",
			wantErr: ErrLegacyBody,
		},
		{
			name:    "string literal containing body should be ignored",
			sql:     "SELECT 'body' FROM notes",
			wantErr: nil,
		},
		{
			name:    "comment containing body should be ignored",
			sql:     "-- SELECT body FROM notes\nSELECT title FROM notes",
			wantErr: nil,
		},
		{
			name:    "notes_fts.body is allowed",
			sql:     "SELECT notes_fts.body FROM notes_fts",
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckLegacyBody(tt.sql)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("CheckLegacyBody(%q) returned error %v, want nil", tt.sql, err)
				}
			} else {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("CheckLegacyBody(%q) returned error %v, want %v", tt.sql, err, tt.wantErr)
				}
			}
		})
	}
}
