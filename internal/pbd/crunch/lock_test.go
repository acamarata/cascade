package crunch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLockPreventsModification(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	sibling := t.TempDir()

	if err := LockTrees(root); err != nil {
		t.Fatalf("LockTrees: %v", err)
	}
	defer UnlockTrees(root)

	if err := CheckWritable(root); !errors.Is(err, ErrTreeLocked) {
		t.Errorf("CheckWritable(root) = %v, want ErrTreeLocked", err)
	}
	if err := CheckWritable(sub); !errors.Is(err, ErrTreeLocked) {
		t.Errorf("CheckWritable(sub) = %v, want ErrTreeLocked", err)
	}
	if err := CheckWritable(sibling); err != nil {
		t.Errorf("CheckWritable(sibling) = %v, want nil", err)
	}
}

func TestUnlockAllowsModification(t *testing.T) {
	root := t.TempDir()
	if err := LockTrees(root); err != nil {
		t.Fatalf("LockTrees: %v", err)
	}
	if err := UnlockTrees(root); err != nil {
		t.Fatalf("UnlockTrees: %v", err)
	}
	if err := CheckWritable(root); err != nil {
		t.Errorf("CheckWritable(root) = %v, want nil", err)
	}
}

func TestLockMultiplePaths(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	if err := LockTrees(dir1, dir2); err != nil {
		t.Fatalf("LockTrees: %v", err)
	}
	defer UnlockTrees(dir1, dir2)

	if err := CheckWritable(dir1); !errors.Is(err, ErrTreeLocked) {
		t.Errorf("CheckWritable(dir1) = %v, want ErrTreeLocked", err)
	}
	if err := CheckWritable(dir2); !errors.Is(err, ErrTreeLocked) {
		t.Errorf("CheckWritable(dir2) = %v, want ErrTreeLocked", err)
	}

	// duplicate
	err := LockTrees(dir1, dir1)
	if !errors.Is(err, ErrInvalidTreePath) {
		t.Errorf("LockTrees duplicate = %v, want ErrInvalidTreePath", err)
	}

	// empty
	err = LockTrees(dir1, "")
	if !errors.Is(err, ErrInvalidTreePath) {
		t.Errorf("LockTrees empty = %v, want ErrInvalidTreePath", err)
	}

	// no partial effect: dir1 must still be locked
	if err := CheckWritable(dir1); !errors.Is(err, ErrTreeLocked) {
		t.Errorf("CheckWritable(dir1) after failed LockTrees = %v, want ErrTreeLocked", err)
	}
}

func TestLockNonExistentPathFails(t *testing.T) {
	dir1 := t.TempDir()
	missing := filepath.Join(dir1, "missing")

	err := LockTrees(dir1, missing)
	if !errors.Is(err, ErrPathNotFound) {
		t.Fatalf("LockTrees = %v, want ErrPathNotFound", err)
	}

	// None of the supplied paths should be locked
	if err := CheckWritable(dir1); err != nil {
		t.Errorf("CheckWritable(dir1) = %v, want nil (no partial lock)", err)
	}
}
