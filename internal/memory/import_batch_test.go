package memory

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

type importRollbackFS struct {
	files       map[string][]byte
	writeCalls  int
	failOnWrite int
}

func (f *importRollbackFS) ReadFile(path string) ([]byte, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (f *importRollbackFS) WriteAtomic(path string, data []byte) error {
	f.writeCalls++
	if f.writeCalls == f.failOnWrite {
		f.failOnWrite = 0
		return errInjected
	}
	f.files[path] = append([]byte(nil), data...)
	return nil
}

func (f *importRollbackFS) Remove(path string) error {
	if _, ok := f.files[path]; !ok {
		return fs.ErrNotExist
	}
	delete(f.files, path)
	return nil
}

func (f *importRollbackFS) Exists(path string) (bool, error) {
	_, ok := f.files[path]
	return ok, nil
}

func (f *importRollbackFS) ReadDirNames(dir string) ([]string, error) {
	var names []string
	for path := range f.files {
		if filepath.Dir(path) == dir {
			names = append(names, filepath.Base(path))
		}
	}
	sort.Strings(names)
	return names, nil
}

func TestImportBatch_WriteFailureRestoresWholeBatch(t *testing.T) {
	sys := &importRollbackFS{files: map[string][]byte{}, failOnWrite: 2}
	store := newFileStoreWithFS(t.TempDir(), newTestClock(), sys)
	mutations := []ImportMutation{
		importMutation("first", "v1:first"),
		importMutation("second", "v1:second"),
	}
	_, err := store.ImportBatch(context.Background(), mutations, false)
	if err == nil {
		t.Fatal("second write failure was not returned")
	}
	if len(sys.files) != 0 {
		t.Fatalf("rollback left destination files: %v", sys.files)
	}
}

func TestImportBatch_DryRunAndConflictDoNotWrite(t *testing.T) {
	sys := &importRollbackFS{files: map[string][]byte{}}
	store := newFileStoreWithFS(t.TempDir(), newTestClock(), sys)
	mutation := importMutation("first", "v1:first")
	result, err := store.ImportBatch(context.Background(), []ImportMutation{mutation}, true)
	if err != nil || len(result) != 1 || len(sys.files) != 0 {
		t.Fatalf("dry run changed destination: result=%v files=%v err=%v", result, sys.files, err)
	}
	entry := canonicalImportedEntry(mutation)
	entry.Body = "different"
	sys.files[store.entryPath(entry.Kind, entry.Name)] = encodeEntry(entry)
	_, err = store.ImportBatch(context.Background(), []ImportMutation{mutation}, false)
	if err == nil || len(sys.files) != 1 {
		t.Fatalf("conflict was not a pre-write refusal: files=%v err=%v", sys.files, err)
	}
}

func importMutation(name, source string) ImportMutation {
	return ImportMutation{Entry: MemoryEntry{Name: name, Kind: KindProject,
		Body: "body", ScopeRef: "migration:v1", Confidence: 1,
		Provenance: Provenance{Origin: OriginFile}},
		SourceRef: source, SourceTime: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}
}
