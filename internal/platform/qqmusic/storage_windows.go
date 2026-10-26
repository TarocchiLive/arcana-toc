package qqmusic

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/LingLambda/arcana-toc/internal/fileutil"
	"golang.org/x/sys/windows"
)

func openCredentialDirectory(path string) (*os.File, error) {
	// The directory handle denies delete sharing, pinning the path until close.
	return fileutil.Open(path, os.O_RDONLY, 0)
}

func openCredentialAt(directory *os.File, name string, flags int) (*os.File, error) {
	return fileutil.Open(filepath.Join(directory.Name(), name), flags, 0600)
}

func removeCredentialAt(directory *os.File, name string) error {
	return os.Remove(filepath.Join(directory.Name(), name))
}

func renameCredentialAt(directory *os.File, old, name string) error {
	source, err := windows.UTF16PtrFromString(filepath.Join(directory.Name(), old))
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(filepath.Join(directory.Name(), name))
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// MoveFileEx above flushes the replacement; Windows cannot fsync directories.
func syncCredentialDirectory(_ *os.File) {}

func checkCredentialFile(info os.FileInfo) error {
	// Windows permissions are enforced by the DACL installed by fileutil.Open.
	if !info.Mode().IsRegular() {
		return ErrCredentialInvalid
	}
	return nil
}

func checkCredentialTarget(directory *os.File, name string) error {
	file, err := openCredentialAt(directory, name, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return checkCredentialFile(info)
}
