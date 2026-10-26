package player

import (
	"errors"
	"os"
)

func validateMP3File(f *os.File) error {
	var h [10]byte
	n, _ := f.ReadAt(h[:], 0)
	if n < 3 || string(h[:3]) != "ID3" {
		return nil
	}
	if n != len(h) {
		return errors.New("truncated ID3 header")
	}
	var size int64
	for _, b := range h[6:10] {
		if b&0x80 != 0 {
			return errors.New("invalid ID3 length")
		}
		size = (size << 7) | int64(b)
	}
	info, err := f.Stat()
	if err != nil {
		return errors.New("inspect MP3 source")
	}
	// Album artwork is not needed by the player; cap the decoder's tag allocation.
	if size > 8<<20 || size > info.Size()-10 {
		return errors.New("ID3 metadata exceeds source bounds or 8 MiB limit")
	}
	return nil
}
