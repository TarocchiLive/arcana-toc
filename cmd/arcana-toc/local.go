package main

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
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
	fd, err := unix.Open(filepath.Join(dir, "service.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "service lock")
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("data directory is already owned by another service")
	}
	return f, nil
}
