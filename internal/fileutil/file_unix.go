//go:build darwin || linux

package fileutil

import (
	"golang.org/x/sys/unix"
	"os"
)

// Open opens a regular path without following a final symlink or blocking on a FIFO.
func Open(path string, flags int, mode os.FileMode) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// Lock takes a nonblocking exclusive lock until the file is closed.
func Lock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
