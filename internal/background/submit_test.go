package background

import (
	"errors"
	"testing"
)

func makeSubmitFiles(count, size int) []SubmitFile {
	files := make([]SubmitFile, count)
	for i := range files {
		files[i] = SubmitFile{
			Name: "host-file",
			Size: size,
		}
	}
	return files
}

func requireD3404ValidationError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected D3404 validation error, got nil")
	}
	if !errors.Is(err, ErrD3404Validation) {
		t.Fatalf("expected D3404 validation error, got %v", err)
	}
}

func TestSubmitValidSingleFile(t *testing.T) {
	err := Submit(makeSubmitFiles(1, 1024))
	if err != nil {
		t.Fatalf("expected valid submission, got %v", err)
	}
}

func TestSubmitValidEightFiles(t *testing.T) {
	err := Submit(makeSubmitFiles(8, 128*1024))
	if err != nil {
		t.Fatalf("expected valid submission, got %v", err)
	}
}

func TestSubmitRejectNineFiles(t *testing.T) {
	err := Submit(makeSubmitFiles(9, 1024))
	requireD3404ValidationError(t, err)
}

func TestSubmitRejectOversizedFile(t *testing.T) {
	err := Submit(makeSubmitFiles(1, 257*1024))
	requireD3404ValidationError(t, err)
}

func TestSubmitRejectOversizedTotal(t *testing.T) {
	err := Submit(makeSubmitFiles(5, 256*1024))
	requireD3404ValidationError(t, err)
}
