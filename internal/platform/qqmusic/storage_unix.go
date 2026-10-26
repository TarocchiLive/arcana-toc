//go:build darwin || linux

package qqmusic

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func openCredentialDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openCredentialAt(directory *os.File, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func removeCredentialAt(directory *os.File, name string) error {
	return unix.Unlinkat(int(directory.Fd()), name, 0)
}

func renameCredentialAt(directory *os.File, old, name string) error {
	return unix.Renameat(int(directory.Fd()), old, int(directory.Fd()), name)
}

func syncCredentialDirectory(directory *os.File) { _ = unix.Fsync(int(directory.Fd())) }

func checkCredentialFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return ErrCredentialInvalid
	}
	return nil
}

func checkCredentialTarget(directory *os.File, name string) error {
	var info unix.Stat_t
	err := unix.Fstatat(int(directory.Fd()), name, &info, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Mode&0o777 != 0o600 {
		return ErrCredentialInvalid
	}
	return nil
}
