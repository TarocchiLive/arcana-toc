package player

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gopxl/beep"
	"github.com/gopxl/beep/effects"
	"github.com/gopxl/beep/flac"
	"github.com/gopxl/beep/mp3"
	"github.com/gopxl/beep/speaker"
	"github.com/gopxl/beep/vorbis"
	"github.com/gopxl/beep/wav"

	"github.com/LingLambda/arcana-toc/internal/platform"
)

const (
	beepOutputRate     = beep.SampleRate(44100)
	beepBufferDuration = 200 * time.Millisecond
	beepDownloadLimit  = int64(512 << 20)
)

var (
	beepSpeakerOnce sync.Once
	beepSpeakerErr  error
)

// Beep is an in-process URL player. It downloads one authorized source to a
// private temporary file, decodes it with Beep, and writes PCM through Oto.
// The service owns the single speaker instance and one active handle.
type Beep struct {
	mu          sync.Mutex
	active      *beepHandle
	closed      bool
	httpClient  *http.Client
	preparing   context.CancelFunc
	prepareDone chan struct{}
}

// NewBeep returns an in-process player using the host's default audio device.
func NewBeep() *Beep {
	return &Beep{httpClient: &http.Client{Timeout: 2 * time.Minute}}
}

func (p *Beep) Capabilities() platform.Capabilities {
	return platform.Capabilities{DirectURL: true, Seek: true, Volume: true}
}

func (p *Beep) Play(ctx context.Context, source platform.ResolvedSource) (Handle, error) {
	if ctx == nil {
		return nil, errors.New("play beep: nil context")
	}
	if source.Kind != platform.SourceURL || source.URL == "" {
		return nil, fmt.Errorf("play beep: %w", platform.ErrUnsupportedCapability)
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("play beep: player is closed")
	}
	if p.active != nil || p.preparing != nil {
		p.mu.Unlock()
		return nil, errors.New("play beep: playback already active")
	}
	lifetime := ctx
	ctx, cancel := context.WithCancel(ctx)
	p.preparing = cancel
	prepared := make(chan struct{})
	p.prepareDone = prepared
	p.mu.Unlock()
	defer func() {
		cancel()
		p.mu.Lock()
		p.preparing = nil
		close(prepared)
		p.mu.Unlock()
	}()

	file, path, contentType, err := p.prepare(ctx, source.URL)
	if err != nil {
		return nil, err
	}
	streamer, format, err := decodeSource(file, contentType, source.URL)
	if err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if format.SampleRate <= 0 || streamer.Len() <= 0 {
		_ = streamer.Close()
		_ = file.Close()
		_ = os.Remove(path)
		return nil, errors.New("play beep: source has no decodable audio")
	}
	if err := initBeepSpeaker(); err != nil {
		_ = streamer.Close()
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	h := newBeepHandle(p, file, path, streamer, format)
	p.mu.Lock()
	if p.closed || p.active != nil || ctx.Err() != nil {
		p.mu.Unlock()
		h.finish(Result{Reason: EndStopped})
		return nil, errors.New("play beep: playback became unavailable")
	}
	p.active = h
	p.mu.Unlock()
	// Close waits for preparation to finish before stopping this handle.
	speaker.Play(h.volume)
	go func() {
		select {
		case <-lifetime.Done():
			_ = h.Stop()
		case <-h.released:
		}
	}()
	return h, nil
}

func (p *Beep) prepare(ctx context.Context, rawURL string) (*os.File, string, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", "", errors.New("play beep: invalid source")
	}
	file, err := os.CreateTemp("", "arcana-toc-audio-*")
	if err != nil {
		return nil, "", "", errors.New("play beep: create temporary source")
	}
	path := file.Name()
	cleanup := func(err error) (*os.File, string, string, error) {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, "", "", err
	}

	if parsed.Scheme == "" || parsed.Scheme == "file" {
		localPath := rawURL
		if parsed.Scheme == "file" {
			localPath = parsed.Path
		}
		input, openErr := openAudioFile(localPath)
		if openErr != nil {
			return cleanup(openErr)
		}
		written, copyErr := io.Copy(file, io.LimitReader(&sourceReader{ctx: ctx, reader: input}, beepDownloadLimit+1))
		_ = input.Close()
		if copyErr != nil || written > beepDownloadLimit {
			if ctx.Err() != nil {
				return cleanup(ctx.Err())
			}
			return cleanup(errors.New("play beep: read source"))
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return cleanup(errors.New("play beep: rewind source"))
		}
		return file, path, "", nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return cleanup(errors.New("play beep: unsupported source scheme"))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return cleanup(errors.New("play beep: create source request"))
	}
	client := p.httpClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return cleanup(ctx.Err())
		}
		return cleanup(errors.New("play beep: source request failed"))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return cleanup(errors.New("play beep: source request rejected"))
	}
	if resp.ContentLength > beepDownloadLimit {
		return cleanup(errors.New("play beep: source is too large"))
	}
	written, err := io.Copy(file, io.LimitReader(resp.Body, beepDownloadLimit+1))
	if err != nil {
		if ctx.Err() != nil {
			return cleanup(ctx.Err())
		}
		return cleanup(errors.New("play beep: download source"))
	}
	if written > beepDownloadLimit {
		return cleanup(errors.New("play beep: source is too large"))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return cleanup(errors.New("play beep: rewind source"))
	}
	return file, path, resp.Header.Get("Content-Type"), nil
}
func initBeepSpeaker() error {
	beepSpeakerOnce.Do(func() {
		if err := speaker.Init(beepOutputRate, beepOutputRate.N(beepBufferDuration)); err != nil {
			beepSpeakerErr = fmt.Errorf("play beep: initialize audio device: %w", err)
		}
	})
	return beepSpeakerErr
}
func decodeSource(file *os.File, contentType, rawURL string) (beep.StreamSeekCloser, beep.Format, error) {
	kind, err := detectFormat(file, contentType, rawURL)
	if err != nil {
		return nil, beep.Format{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, beep.Format{}, errors.New("play beep: rewind decoder input")
	}
	var (
		stream beep.StreamSeekCloser
		format beep.Format
	)
	switch kind {
	case "mp3":
		if err := validateMP3File(file); err != nil {
			return nil, beep.Format{}, err
		}
		stream, format, err = mp3.Decode(file)
	case "flac":
		if err := validateFLACFile(file); err != nil {
			return nil, beep.Format{}, err
		}
		stream, format, err = flac.Decode(file)
	case "ogg":
		stream, format, err = vorbis.Decode(file)
	case "wav":
		if err := validateWAVFile(file); err != nil {
			return nil, beep.Format{}, err
		}
		stream, format, err = wav.Decode(file)
	default:
		err = errors.New("play beep: unsupported audio format")
	}
	if err != nil {
		if stream != nil {
			_ = stream.Close()
		}
		return nil, beep.Format{}, errors.New("play beep: decode source")
	}
	return stream, format, nil
}

func detectFormat(file *os.File, contentType, rawURL string) (string, error) {
	var header [512]byte
	n, err := file.Read(header[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("play beep: inspect source")
	}
	data := header[:n]
	switch {
	case len(data) >= 4 && string(data[:4]) == "fLaC":
		return "flac", nil
	case len(data) >= 4 && string(data[:4]) == "OggS":
		return "ogg", nil
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return "wav", nil
	case len(data) >= 3 && string(data[:3]) == "ID3":
		return "mp3", nil
	case len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0:
		return "mp3", nil
	}
	mime := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch mime {
	case "audio/mpeg", "audio/mp3", "audio/x-mpeg":
		return "mp3", nil
	case "audio/flac", "audio/x-flac":
		return "flac", nil
	case "audio/ogg", "application/ogg":
		return "ogg", nil
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav", nil
	}
	if parsed, err := url.Parse(rawURL); err == nil {
		rawURL = parsed.Path
	}
	if ext := strings.ToLower(filepath.Ext(rawURL)); ext != "" {
		switch ext {
		case ".mp3":
			return "mp3", nil
		case ".flac":
			return "flac", nil
		case ".ogg", ".oga":
			return "ogg", nil
		case ".wav":
			return "wav", nil
		}
	}
	return "", errors.New("play beep: unknown audio format")
}

type beepHandle struct {
	player   *Beep
	file     *os.File
	path     string
	raw      beep.StreamSeekCloser
	format   beep.Format
	ctrl     *beep.Ctrl
	volume   *effects.Volume
	stream   *resampledStream
	done     chan Result
	released chan struct{}

	mu          sync.Mutex
	paused      bool
	volumeValue int
	finished    bool
	terminal    Status
	doneOnce    sync.Once
	closeOnce   sync.Once
}

func newBeepHandle(player *Beep, file *os.File, path string, raw beep.StreamSeekCloser, format beep.Format) *beepHandle {
	h := &beepHandle{
		player:      player,
		file:        file,
		path:        path,
		raw:         raw,
		format:      format,
		ctrl:        &beep.Ctrl{},
		done:        make(chan Result, 1),
		released:    make(chan struct{}),
		volumeValue: 100,
	}
	h.stream = newResampledStream(raw, format.SampleRate, beepOutputRate)
	h.ctrl.Streamer = beep.Seq(h.stream, beep.Callback(func() {
		result := Result{Reason: EndEOF}
		if err := h.raw.Err(); err != nil {
			result = Result{Reason: EndError, Err: errors.New("audio decoder failed")}
		}
		h.finish(result)
	}))
	h.volume = &effects.Volume{Streamer: h.ctrl, Base: 2}
	h.setVolumeValue(100)
	return h
}

func (h *beepHandle) Pause() error {
	if err := h.ensureLive(); err != nil {
		return err
	}
	speaker.Lock()
	h.mu.Lock()
	if h.finished {
		h.mu.Unlock()
		speaker.Unlock()
		return nil
	}
	h.ctrl.Paused = true
	h.paused = true
	h.mu.Unlock()
	speaker.Unlock()
	return nil
}

func (h *beepHandle) Resume() error {
	if err := h.ensureLive(); err != nil {
		return err
	}
	speaker.Lock()
	h.mu.Lock()
	if h.finished {
		h.mu.Unlock()
		speaker.Unlock()
		return nil
	}
	h.ctrl.Paused = false
	h.paused = false
	h.mu.Unlock()
	speaker.Unlock()
	return nil
}

func (h *beepHandle) Stop() error {
	speaker.Lock()
	h.mu.Lock()
	if h.finished {
		h.mu.Unlock()
		speaker.Unlock()
		return nil
	}
	h.ctrl.Streamer = nil
	h.mu.Unlock()
	h.finish(Result{Reason: EndStopped})
	speaker.Unlock()
	return nil
}

func (h *beepHandle) Seek(offset time.Duration) error {
	if offset < 0 {
		return errors.New("seek must be nonnegative")
	}
	if err := h.ensureLive(); err != nil {
		return err
	}
	speaker.Lock()
	defer speaker.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished {
		return nil
	}
	if offset > h.duration() {
		offset = h.duration()
	}
	if err := h.raw.Seek(h.format.SampleRate.N(offset)); err != nil {
		return errors.New("seek audio source")
	}
	h.stream.reset()
	return nil
}

func (h *beepHandle) SetVolume(percent int) error {
	if percent < 0 || percent > 100 {
		return errors.New("volume must be between 0 and 100")
	}
	if err := h.ensureLive(); err != nil {
		return err
	}
	speaker.Lock()
	h.mu.Lock()
	if !h.finished {
		h.setVolumeValue(percent)
	}
	h.mu.Unlock()
	speaker.Unlock()
	return nil
}

func (h *beepHandle) Status() (Status, error) {
	speaker.Lock()
	defer speaker.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished {
		return h.terminal, nil
	}
	return Status{
		Paused:   h.paused,
		Position: h.format.SampleRate.D(h.raw.Position()),
		Duration: h.duration(),
		Volume:   h.volumeValue,
	}, nil
}

func (h *beepHandle) Done() <-chan Result { return h.done }

func (h *beepHandle) ensureLive() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished {
		return errors.New("playback is finished")
	}
	return nil
}

func (h *beepHandle) duration() time.Duration {
	return h.format.SampleRate.D(h.raw.Len())
}

func (h *beepHandle) setVolumeValue(percent int) {
	h.volumeValue = percent
	h.volume.Volume = float64(percent)*5/100 - 5
	h.volume.Silent = percent == 0
}

func (h *beepHandle) finish(result Result) {
	h.doneOnce.Do(func() {
		h.mu.Lock()
		h.terminal = Status{Paused: h.paused, Position: h.format.SampleRate.D(h.raw.Position()), Duration: h.duration(), Volume: h.volumeValue}
		h.finished = true
		h.mu.Unlock()
		h.cleanup()
		close(h.released)
		if h.player != nil {
			h.player.clear(h)
		}
		h.done <- result
		close(h.done)
	})
}

func (h *beepHandle) cleanup() {
	h.closeOnce.Do(func() {
		_ = h.raw.Close()
		_ = h.file.Close()
		_ = os.Remove(h.path)
	})
}

func (p *Beep) clear(h *beepHandle) {
	p.mu.Lock()
	if p.active == h {
		p.active = nil
	}
	p.mu.Unlock()
}

func (p *Beep) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	h := p.active
	cancel := p.preparing
	prepared := p.prepareDone
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		<-prepared
	}
	if h != nil {
		return h.Stop()
	}
	return nil
}

var _ Player = (*Beep)(nil)
var _ Handle = (*beepHandle)(nil)

type resampledStream struct {
	raw        beep.StreamSeekCloser
	oldRate    beep.SampleRate
	outputRate beep.SampleRate
	stream     beep.Streamer
}

func newResampledStream(raw beep.StreamSeekCloser, oldRate, outputRate beep.SampleRate) *resampledStream {
	r := &resampledStream{raw: raw, oldRate: oldRate, outputRate: outputRate}
	r.reset()
	return r
}

func (r *resampledStream) reset() {
	if r.oldRate == r.outputRate {
		r.stream = r.raw
		return
	}
	r.stream = beep.Resample(4, r.oldRate, r.outputRate, r.raw)
}

func (r *resampledStream) Stream(samples [][2]float64) (int, bool) {
	return r.stream.Stream(samples)
}

func (r *resampledStream) Err() error { return r.raw.Err() }
