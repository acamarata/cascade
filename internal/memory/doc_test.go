package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// exampleClock is a fixed clock so the examples print stable output. Real
// callers pass the process clock; nothing in this package reads the wall
// clock itself.
type exampleClock struct{ at time.Time }

func (c exampleClock) Now() time.Time { return c.at }

// ExampleFileStore demonstrates the whole record lifecycle: write, read
// back, list, and delete.
func ExampleFileStore() {
	base, err := os.MkdirTemp("", "memory-example-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(base) }()

	ctx := context.Background()
	clk := exampleClock{at: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	store := NewFileStore(base, clk)

	entry := MemoryEntry{
		Name:        "units-and-clock",
		Kind:        KindUser,
		Description: "Stated unit and clock preferences",
		Body:        "Prefers metric units and a 24-hour clock.\n",
		ScopeRef:    "global",
		Confidence:  0.9,
		Provenance:  Provenance{Origin: OriginSession, SessionID: "s-1"},
	}
	if err := store.Write(ctx, entry); err != nil {
		panic(err)
	}
	// A second identical write changes nothing on disk.
	if err := store.Write(ctx, entry); err != nil {
		panic(err)
	}
	exampleReport(ctx, store)

	// Output:
	// body: Prefers metric units and a 24-hour clock.
	//
	// written at: 2026-01-02T03:04:05Z
	// body unchanged since that write: true
	// records: [units-and-clock]
	// present after delete: false
}

// exampleReport reads the record back, lists the kind, deletes the record
// and reports each step. Split out of ExampleFileStore so both stay inside
// the 50-line function limit.
func exampleReport(ctx context.Context, store *FileStore) {
	got, err := store.Read(ctx, KindUser, "units-and-clock")
	if err != nil {
		panic(err)
	}
	fmt.Println("body:", got.Body)
	fmt.Println("written at:", got.Provenance.UpdatedAt.Format(time.RFC3339))
	fmt.Println("body unchanged since that write:", got.Provenance.ContentHash == got.BodyHash())

	names, err := store.List(ctx, KindUser)
	if err != nil {
		panic(err)
	}
	fmt.Println("records:", names)

	if err := store.Delete(ctx, KindUser, "units-and-clock"); err != nil {
		panic(err)
	}
	present, err := store.Exists(ctx, KindUser, "units-and-clock")
	if err != nil {
		panic(err)
	}
	fmt.Println("present after delete:", present)
}

// ExampleFileStore_Read shows the two refusals a caller must handle: a
// record that is not there, and a record that cannot be parsed.
func ExampleFileStore_Read() {
	base, err := os.MkdirTemp("", "memory-example-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(base) }()

	ctx := context.Background()
	store := NewFileStore(base, exampleClock{at: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)})

	_, err = store.Read(ctx, KindProject, "never-written")
	fmt.Println("missing:", err)

	dir := filepath.Join(base, string(KindProject))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "damaged.md"), []byte("not a record"), 0o600); err != nil {
		panic(err)
	}
	_, err = store.Read(ctx, KindProject, "damaged")
	fmt.Println("damaged:", err)

	// Output:
	// missing: not-found: no memory record project/never-written: not-found: memory record not found
	// damaged: integrity: file does not begin with a "---" frontmatter fence: integrity: malformed memory record
}

// ExampleHashBody shows the content hash a record carries, which is what
// tells a caller whether a body changed since the store last wrote it.
func ExampleHashBody() {
	fmt.Println(HashBody(""))
	// Output: af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262
}
