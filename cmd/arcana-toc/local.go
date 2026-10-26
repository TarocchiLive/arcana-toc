package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/LingLambda/arcana-toc/internal/fileutil"
)

func ownDataDir(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe data directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	f, err := fileutil.Open(filepath.Join(dir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := fileutil.Lock(f); err != nil {
		_ = f.Close()
		return nil, errors.New("data directory is already owned by another service")
	}
	return f, nil
}
