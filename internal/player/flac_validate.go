package player

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"

	"github.com/mewkiz/flac/meta"
)

const (
	flacMaxID3Bytes          = int64(16 << 20)
	flacMaxMetadataBytes     = int64(64 << 20)
	flacMaxMetadataBlocks    = 1024
	flacMaxVorbisComments    = uint32(64 << 10)
	flacMetadataHeaderLength = int64(4)
)

// validateFLACFile checks metadata before gopxl/beep/flac passes the file to
// mewkiz/flac. The latter parses every metadata block for seekable inputs and
// trusts several counts and lengths when allocating pictures and comments.
func validateFLACFile(file *os.File) error {
	if file == nil {
		return errors.New("validate flac: nil file")
	}
	info, err := file.Stat()
	if err != nil {
		return errors.New("validate flac: stat source")
	}
	if !info.Mode().IsRegular() {
		return errors.New("validate flac: source is not a regular file")
	}

	fileSize := info.Size()
	signatureOffset, err := flacSignatureOffset(file, fileSize)
	if err != nil {
		return err
	}

	metadataStart := signatureOffset + 4
	pos := metadataStart
	for blockNumber := 0; ; blockNumber++ {
		if blockNumber >= flacMaxMetadataBlocks {
			return errors.New("validate flac: too many metadata blocks")
		}

		var header [4]byte
		if err := readFLACAt(file, header[:], pos, fileSize); err != nil {
			return errors.New("validate flac: truncated metadata header")
		}
		isLast := header[0]&0x80 != 0
		blockType := meta.Type(header[0] & 0x7f)
		blockLength := int64(header[1])<<16 | int64(header[2])<<8 | int64(header[3])
		bodyOffset := pos + flacMetadataHeaderLength
		bodyEnd := bodyOffset + blockLength
		if bodyEnd > fileSize {
			return errors.New("validate flac: metadata block exceeds source")
		}
		if bodyEnd-metadataStart > flacMaxMetadataBytes {
			return errors.New("validate flac: metadata exceeds size limit")
		}
		if blockNumber == 0 && blockType != meta.TypeStreamInfo {
			return errors.New("validate flac: first metadata block is not stream info")
		}
		if blockNumber != 0 && blockType == meta.TypeStreamInfo {
			return errors.New("validate flac: duplicate stream info block")
		}

		if err := validateFLACMetadataBlock(file, pos, bodyOffset, bodyEnd, blockType, blockNumber == 0); err != nil {
			return err
		}
		pos = bodyEnd
		if isLast {
			return nil
		}
	}
}

func flacSignatureOffset(file *os.File, fileSize int64) (int64, error) {
	var prefix [4]byte
	if err := readFLACAt(file, prefix[:], 0, fileSize); err != nil {
		return 0, errors.New("validate flac: truncated signature")
	}
	if bytes.Equal(prefix[:], []byte("fLaC")) {
		return 0, nil
	}
	if !bytes.Equal(prefix[:3], []byte("ID3")) {
		return 0, errors.New("validate flac: invalid signature")
	}

	var rest [6]byte
	if err := readFLACAt(file, rest[:], 4, fileSize); err != nil {
		return 0, errors.New("validate flac: truncated ID3 header")
	}
	for _, b := range rest[2:] {
		if b&0x80 != 0 {
			return 0, errors.New("validate flac: invalid ID3 size")
		}
	}
	id3Size := int64(rest[2])<<21 | int64(rest[3])<<14 | int64(rest[4])<<7 | int64(rest[5])
	if id3Size > flacMaxID3Bytes {
		return 0, errors.New("validate flac: ID3 metadata exceeds size limit")
	}
	offset := int64(10) + id3Size
	if err := readFLACAt(file, prefix[:], offset, fileSize); err != nil {
		return 0, errors.New("validate flac: ID3 metadata exceeds source")
	}
	if !bytes.Equal(prefix[:], []byte("fLaC")) {
		return 0, errors.New("validate flac: invalid signature after ID3 metadata")
	}
	return offset, nil
}

func validateFLACMetadataBlock(file *os.File, headerOffset, bodyOffset, bodyEnd int64, blockType meta.Type, first bool) error {
	switch blockType {
	case meta.TypeStreamInfo:
		if !first || bodyEnd-bodyOffset != 34 {
			return errors.New("validate flac: invalid stream info length")
		}
		block, err := meta.Parse(io.NewSectionReader(file, headerOffset, bodyEnd-headerOffset))
		if err != nil {
			return errors.New("validate flac: invalid stream info")
		}
		streamInfo, ok := block.Body.(*meta.StreamInfo)
		if !ok {
			return errors.New("validate flac: invalid stream info")
		}
		if streamInfo.BlockSizeMin > streamInfo.BlockSizeMax {
			return errors.New("validate flac: invalid stream block size range")
		}
		if streamInfo.FrameSizeMin != 0 && streamInfo.FrameSizeMax != 0 && streamInfo.FrameSizeMin > streamInfo.FrameSizeMax {
			return errors.New("validate flac: invalid stream frame size range")
		}
		switch streamInfo.BitsPerSample {
		case 8, 16, 24:
		default:
			return errors.New("validate flac: unsupported bits per sample")
		}
		if streamInfo.NSamples > uint64(^uint(0)>>1) {
			return errors.New("validate flac: sample count exceeds platform limit")
		}
	case meta.TypePadding:
		if err := validateFLACZeroRange(file, bodyOffset, bodyEnd); err != nil {
			return errors.New("validate flac: invalid padding block")
		}
	case meta.TypeApplication:
		if bodyEnd-bodyOffset < 4 {
			return errors.New("validate flac: invalid application block")
		}
	case meta.TypeSeekTable:
		length := bodyEnd - bodyOffset
		if length < 18 || length%18 != 0 {
			return errors.New("validate flac: invalid seek table length")
		}
	case meta.TypeVorbisComment:
		return validateFLACVorbisComment(file, bodyOffset, bodyEnd)
	case meta.TypeCueSheet:
		return validateFLACCueSheet(file, bodyOffset, bodyEnd)
	case meta.TypePicture:
		return validateFLACPicture(file, bodyOffset, bodyEnd)
	default:
		if blockType == 127 {
			return errors.New("validate flac: invalid metadata block type")
		}
		// Types 7 through 126 are reserved and are skipped by mewkiz/flac.
	}
	return nil
}

func validateFLACVorbisComment(file *os.File, start, end int64) error {
	cursor := start
	vendorLength, err := readFLACUint32(file, &cursor, end, binary.LittleEndian)
	if err != nil || int64(vendorLength) > end-cursor {
		return errors.New("validate flac: invalid vorbis vendor length")
	}
	cursor += int64(vendorLength)
	commentCount, err := readFLACUint32(file, &cursor, end, binary.LittleEndian)
	if err != nil {
		return errors.New("validate flac: truncated vorbis comment count")
	}
	if commentCount > flacMaxVorbisComments || int64(commentCount) > (end-cursor)/4 {
		return errors.New("validate flac: invalid vorbis comment count")
	}
	for range commentCount {
		length, err := readFLACUint32(file, &cursor, end, binary.LittleEndian)
		if err != nil || int64(length) > end-cursor {
			return errors.New("validate flac: invalid vorbis comment length")
		}
		if !flacRangeContainsByte(file, cursor, cursor+int64(length), '=') {
			return errors.New("validate flac: invalid vorbis comment")
		}
		cursor += int64(length)
	}
	if cursor != end {
		return errors.New("validate flac: trailing vorbis comment data")
	}
	return nil
}

func validateFLACPicture(file *os.File, start, end int64) error {
	cursor := start
	if _, err := readFLACUint32(file, &cursor, end, binary.BigEndian); err != nil {
		return errors.New("validate flac: truncated picture type")
	}
	mimeLength, err := readFLACUint32(file, &cursor, end, binary.BigEndian)
	if err != nil || int64(mimeLength) > end-cursor {
		return errors.New("validate flac: invalid picture MIME length")
	}
	cursor += int64(mimeLength)
	descriptionLength, err := readFLACUint32(file, &cursor, end, binary.BigEndian)
	if err != nil || int64(descriptionLength) > end-cursor {
		return errors.New("validate flac: invalid picture description length")
	}
	cursor += int64(descriptionLength)
	for range 4 {
		if _, err := readFLACUint32(file, &cursor, end, binary.BigEndian); err != nil {
			return errors.New("validate flac: truncated picture dimensions")
		}
	}
	dataLength, err := readFLACUint32(file, &cursor, end, binary.BigEndian)
	if err != nil || int64(dataLength) != end-cursor {
		return errors.New("validate flac: invalid picture data length")
	}
	return nil
}

func validateFLACCueSheet(file *os.File, start, end int64) error {
	const cueHeaderLength = int64(128 + 8 + 1 + 258 + 1)
	if end-start < cueHeaderLength {
		return errors.New("validate flac: truncated cue sheet")
	}
	var flag [1]byte
	if err := readFLACAt(file, flag[:], start+136, end); err != nil || flag[0]&0x7f != 0 {
		return errors.New("validate flac: invalid cue sheet flags")
	}
	if err := validateFLACZeroRange(file, start+137, start+395); err != nil {
		return errors.New("validate flac: invalid cue sheet reserved data")
	}
	if err := readFLACAt(file, flag[:], start+395, end); err != nil || flag[0] == 0 {
		return errors.New("validate flac: invalid cue sheet track count")
	}
	cursor := start + cueHeaderLength
	for range int(flag[0]) {
		if end-cursor < 36 {
			return errors.New("validate flac: truncated cue sheet track")
		}
		if err := readFLACAt(file, flag[:], cursor+21, end); err != nil || flag[0]&0x3f != 0 {
			return errors.New("validate flac: invalid cue sheet track flags")
		}
		if err := validateFLACZeroRange(file, cursor+22, cursor+35); err != nil {
			return errors.New("validate flac: invalid cue sheet track reserved data")
		}
		if err := readFLACAt(file, flag[:], cursor+35, end); err != nil {
			return errors.New("validate flac: truncated cue sheet index count")
		}
		cursor += 36
		indexBytes := int64(flag[0]) * 12
		if indexBytes > end-cursor {
			return errors.New("validate flac: cue sheet indices exceed block")
		}
		for index := range int64(flag[0]) {
			if err := validateFLACZeroRange(file, cursor+index*12+9, cursor+index*12+12); err != nil {
				return errors.New("validate flac: invalid cue sheet index reserved data")
			}
		}
		cursor += indexBytes
	}
	if cursor != end {
		return errors.New("validate flac: trailing cue sheet data")
	}
	return nil
}

func readFLACUint32(file *os.File, cursor *int64, end int64, order binary.ByteOrder) (uint32, error) {
	var buf [4]byte
	if err := readFLACAt(file, buf[:], *cursor, end); err != nil {
		return 0, err
	}
	*cursor += 4
	return order.Uint32(buf[:]), nil
}

func readFLACAt(file *os.File, buf []byte, offset, end int64) error {
	if offset < 0 || int64(len(buf)) > end-offset {
		return io.ErrUnexpectedEOF
	}
	_, err := file.ReadAt(buf, offset)
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

func validateFLACZeroRange(file *os.File, start, end int64) error {
	var buf [4096]byte
	for start < end {
		n := int64(len(buf))
		if end-start < n {
			n = end - start
		}
		if err := readFLACAt(file, buf[:n], start, end); err != nil {
			return err
		}
		for _, b := range buf[:n] {
			if b != 0 {
				return errors.New("non-zero reserved data")
			}
		}
		start += n
	}
	return nil
}

func flacRangeContainsByte(file *os.File, start, end int64, target byte) bool {
	var buf [4096]byte
	for start < end {
		n := int64(len(buf))
		if end-start < n {
			n = end - start
		}
		if err := readFLACAt(file, buf[:n], start, end); err != nil {
			return false
		}
		if bytes.IndexByte(buf[:n], target) >= 0 {
			return true
		}
		start += n
	}
	return false
}
