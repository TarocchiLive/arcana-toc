package player

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// beep/wav allocates the complete body of every unknown chunk and every
// non-extensible fmt extension. Keep those metadata allocations small even
// though an audio source itself may be as large as beepDownloadLimit (512 MiB).
const maxWAVMetadataChunkSize uint32 = 1 << 20

var wavPCMSubFormat = [16]byte{
	0x01, 0x00, 0x00, 0x00,
	0x00, 0x00,
	0x10, 0x00,
	0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71,
}

type wavPCMFormat struct {
	channels   uint16
	sampleRate uint32
	blockAlign uint16
	bits       uint16
}

// validateWAVFile rejects structures that can make beep/wav allocate from an
// untrusted chunk length, divide by a zero block alignment, or read beyond the
// declared RIFF and actual file boundaries. ReadAt leaves the caller's offset
// unchanged.
func validateWAVFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return errors.New("wav: inspect file")
	}
	fileSize := info.Size()
	if fileSize < 12 {
		return errors.New("wav: truncated RIFF header")
	}

	var header [12]byte
	if err := readWAVAt(file, 0, header[:]); err != nil {
		return errors.New("wav: read RIFF header")
	}
	if string(header[:4]) != "RIFF" || string(header[8:]) != "WAVE" {
		return errors.New("wav: invalid RIFF/WAVE header")
	}

	riffSize := int64(binary.LittleEndian.Uint32(header[4:8]))
	if riffSize < 4 {
		return errors.New("wav: invalid RIFF size")
	}
	riffEnd := 8 + riffSize
	if riffEnd > fileSize {
		return errors.New("wav: RIFF size exceeds file length")
	}

	var pcm wavPCMFormat
	fmtSeen := false
	for offset := int64(12); ; {
		if offset+8 > riffEnd {
			return errors.New("wav: missing data chunk")
		}

		var chunkHeader [8]byte
		if err := readWAVAt(file, offset, chunkHeader[:]); err != nil {
			return errors.New("wav: read chunk header")
		}
		chunkID := string(chunkHeader[:4])
		chunkSize := binary.LittleEndian.Uint32(chunkHeader[4:])
		bodyStart := offset + 8
		bodyEnd := bodyStart + int64(chunkSize)
		nextChunk := bodyEnd + int64(chunkSize&1)
		if nextChunk > riffEnd {
			return fmt.Errorf("wav: %q chunk exceeds RIFF boundary", chunkID)
		}

		switch chunkID {
		case "fmt ":
			if chunkSize > maxWAVMetadataChunkSize {
				return fmt.Errorf("wav: fmt chunk exceeds %d-byte metadata limit", maxWAVMetadataChunkSize)
			}
			pcm, err = validateWAVFormat(file, bodyStart, chunkSize)
			if err != nil {
				return err
			}
			fmtSeen = true
		case "data":
			if !fmtSeen {
				return errors.New("wav: data chunk precedes format chunk")
			}
			if chunkSize > math.MaxInt32 {
				return errors.New("wav: data chunk exceeds decoder limit")
			}
			if chunkSize == 0 {
				return errors.New("wav: empty data chunk")
			}
			if chunkSize%uint32(pcm.blockAlign) != 0 {
				return errors.New("wav: data size is not block-aligned")
			}
			return nil
		default:
			if chunkSize > maxWAVMetadataChunkSize {
				return fmt.Errorf("wav: %q chunk exceeds %d-byte metadata limit", chunkID, maxWAVMetadataChunkSize)
			}
		}

		offset = nextChunk
	}
}

func validateWAVFormat(file *os.File, offset int64, size uint32) (wavPCMFormat, error) {
	if size < 16 {
		return wavPCMFormat{}, errors.New("wav: format chunk is shorter than 16 bytes")
	}
	// beep/wav does not consume a RIFF pad byte after fmt, so accepting an odd
	// fmt size would make it parse the pad as the next chunk identifier.
	if size&1 != 0 {
		return wavPCMFormat{}, errors.New("wav: odd-sized format chunk is unsupported by decoder")
	}

	var body [40]byte
	readSize := 16
	if size >= uint32(len(body)) {
		readSize = len(body)
	}
	if err := readWAVAt(file, offset, body[:readSize]); err != nil {
		return wavPCMFormat{}, errors.New("wav: read format chunk")
	}

	formatTag := binary.LittleEndian.Uint16(body[0:2])
	pcm := wavPCMFormat{
		channels:   binary.LittleEndian.Uint16(body[2:4]),
		sampleRate: binary.LittleEndian.Uint32(body[4:8]),
		blockAlign: binary.LittleEndian.Uint16(body[12:14]),
		bits:       binary.LittleEndian.Uint16(body[14:16]),
	}
	byteRate := binary.LittleEndian.Uint32(body[8:12])

	switch formatTag {
	case 1:
		// Plain PCM may use WAVEFORMAT (16 bytes) or WAVEFORMATEX. beep/wav
		// safely skips the bounded extension without interpreting it.
	case 0xfffe:
		if size != uint32(len(body)) {
			return wavPCMFormat{}, errors.New("wav: extensible format chunk must be 40 bytes")
		}
		if binary.LittleEndian.Uint16(body[16:18]) != 22 {
			return wavPCMFormat{}, errors.New("wav: invalid extensible format size")
		}
		validBits := binary.LittleEndian.Uint16(body[18:20])
		if validBits == 0 || validBits > pcm.bits {
			return wavPCMFormat{}, errors.New("wav: invalid extensible valid-bits value")
		}
		if [16]byte(body[24:40]) != wavPCMSubFormat {
			return wavPCMFormat{}, errors.New("wav: unsupported extensible subformat")
		}
	default:
		return wavPCMFormat{}, fmt.Errorf("wav: unsupported format type %d", formatTag)
	}

	if pcm.channels == 0 || pcm.channels > math.MaxInt16 {
		return wavPCMFormat{}, errors.New("wav: invalid channel count")
	}
	if pcm.sampleRate == 0 || pcm.sampleRate > math.MaxInt32 {
		return wavPCMFormat{}, errors.New("wav: invalid sample rate")
	}
	if pcm.bits != 8 && pcm.bits != 16 && pcm.bits != 24 {
		return wavPCMFormat{}, errors.New("wav: unsupported bits per sample")
	}

	expectedBlockAlign := uint32(pcm.channels) * uint32(pcm.bits/8)
	if expectedBlockAlign == 0 || expectedBlockAlign > math.MaxInt16 || uint32(pcm.blockAlign) != expectedBlockAlign {
		return wavPCMFormat{}, errors.New("wav: invalid block alignment")
	}
	expectedByteRate := uint64(pcm.sampleRate) * uint64(pcm.blockAlign)
	if expectedByteRate > math.MaxUint32 || uint64(byteRate) != expectedByteRate {
		return wavPCMFormat{}, errors.New("wav: invalid byte rate")
	}

	return pcm, nil
}

func readWAVAt(file *os.File, offset int64, dst []byte) error {
	n, err := file.ReadAt(dst, offset)
	if n != len(dst) {
		return io.ErrUnexpectedEOF
	}
	return err
}
