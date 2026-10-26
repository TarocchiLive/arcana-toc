package player

import (
	"context"
	"errors"
	"github.com/LingLambda/arcana-toc/internal/fileutil"
	"io"
	"os"
)

type sourceReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *sourceReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func openAudioFile(path string) (*os.File, error) {
	f, err := fileutil.Open(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, errors.New("play beep: open source")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > beepDownloadLimit {
		_ = f.Close()
		return nil, errors.New("play beep: source must be a bounded regular file")
	}
	return f, nil
}
