// Purpose: unit + fuzz tests for rclone_decode.go's two decoders (06
// §5.7): FuzzRcloneVersionParse and FuzzRcloneListDecode, seeded from
// testdata/fuzz/ with real `rclone version`/`rclone lsjson` output
// captured from the real 1.75.1 binary installed for this ticket's
// verification (provenance: Art.2.2 — captured, not self-authored).
//
// SPORT: internal.backup.targets.rclone/ADDED (P1-E19-W4-S41-T3).
package targets_test

import (
	"testing"

	"github.com/acamarata/cascade/internal/backup/targets"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestParseRcloneVersionOutput_RealBinaryShape(t *testing.T) {
	out := []byte("rclone v1.75.1\n- os/version: darwin 26.6.2 (64 bit)\n- os/kernel: 25.6.0 (arm64)\n")
	v, err := targets.ParseRcloneVersionOutput(out)
	if err != nil {
		t.Fatalf("ParseRcloneVersionOutput: %v", err)
	}
	if v.Tag != "1.75.1" {
		t.Fatalf("Tag = %q, want %q", v.Tag, "1.75.1")
	}
}

func TestParseRcloneVersionOutput_Empty(t *testing.T) {
	_, err := targets.ParseRcloneVersionOutput(nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ParseRcloneVersionOutput(empty) = %v, want KindInvalidInput", err)
	}
}

func TestParseRcloneVersionOutput_UnrecognizedFirstLine(t *testing.T) {
	_, err := targets.ParseRcloneVersionOutput([]byte("not rclone at all\n"))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ParseRcloneVersionOutput(garbage) = %v, want KindInvalidInput", err)
	}
}

func TestParseRcloneVersionOutput_MissingVTag(t *testing.T) {
	_, err := targets.ParseRcloneVersionOutput([]byte("rclone 1.75.1\n"))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ParseRcloneVersionOutput(no v prefix) = %v, want KindInvalidInput", err)
	}
}

func TestDecodeRcloneListOutput_RealBinaryShape(t *testing.T) {
	out := []byte(`[
{"Path":"objects","Name":"objects","Size":96,"IsDir":true},
{"Path":"objects/ab","Name":"ab","Size":96,"IsDir":true},
{"Path":"objects/ab/abcd","Name":"abcd","Size":6,"IsDir":false}
]`)
	keys, err := targets.DecodeRcloneListOutput(out)
	if err != nil {
		t.Fatalf("DecodeRcloneListOutput: %v", err)
	}
	if len(keys) != 1 || keys[0] != "objects/ab/abcd" {
		t.Fatalf("DecodeRcloneListOutput = %v, want [objects/ab/abcd] (dirs excluded)", keys)
	}
}

func TestDecodeRcloneListOutput_EmptyArray(t *testing.T) {
	keys, err := targets.DecodeRcloneListOutput([]byte("[]"))
	if err != nil {
		t.Fatalf("DecodeRcloneListOutput([]): %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("DecodeRcloneListOutput([]) = %v, want empty", keys)
	}
}

func TestDecodeRcloneListOutput_MalformedJSON(t *testing.T) {
	_, err := targets.DecodeRcloneListOutput([]byte("not json"))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("DecodeRcloneListOutput(garbage) = %v, want KindInvalidInput", err)
	}
}

func TestDecodeRcloneListOutput_EmptyPathRefused(t *testing.T) {
	_, err := targets.DecodeRcloneListOutput([]byte(`[{"Path":"","IsDir":false}]`))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("DecodeRcloneListOutput(empty path) = %v, want KindInvalidInput", err)
	}
}

// FuzzRcloneVersionParse proves ParseRcloneVersionOutput never panics on
// arbitrary bytes, only ever returning a value or a typed error (06
// §5.7). Seed corpus: testdata/fuzz/FuzzRcloneVersionParse/seed001.
func FuzzRcloneVersionParse(f *testing.F) {
	f.Add([]byte("rclone v1.75.1\n- os/version: darwin 26.6.2 (64 bit)\n"))
	f.Add([]byte(""))
	f.Add([]byte("garbage\x00\xff"))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = targets.ParseRcloneVersionOutput(data)
	})
}

// FuzzRcloneListDecode proves DecodeRcloneListOutput never panics on
// arbitrary bytes. Seed corpus: testdata/fuzz/FuzzRcloneListDecode/seed001.
func FuzzRcloneListDecode(f *testing.F) {
	f.Add([]byte(`[{"Path":"objects/ab/abcd","IsDir":false}]`))
	f.Add([]byte("[]"))
	f.Add([]byte("not json"))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = targets.DecodeRcloneListOutput(data)
	})
}

func TestParseRcloneVersionOutput_EmptyTagAfterV(t *testing.T) {
	_, err := targets.ParseRcloneVersionOutput([]byte("rclone v\n"))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ParseRcloneVersionOutput(empty tag) = %v, want KindInvalidInput", err)
	}
}
