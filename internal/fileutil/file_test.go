package fileutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockExcludesSecondHandleAndReleasesOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.lock")
	first, err := Open(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	second, err := Open(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	if err := Lock(first); err != nil {
		t.Fatal(err)
	}
	if err := Lock(second); err == nil {
		t.Fatal("second handle acquired an occupied lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Lock(second); err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
}

func TestOpenRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	file, err := Open(link, os.O_RDONLY, 0600)
	if err == nil {
		_ = file.Close()
		t.Fatal("opened symlink")
	}
}
