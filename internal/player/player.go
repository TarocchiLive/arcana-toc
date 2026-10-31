// Package player provides the in-process audio playback contract used by the
// service. The default implementation decodes URL sources with Go libraries
// and sends PCM directly to the host audio device.
package player

import (
	"context"
	"time"

	"github.com/LingLambda/arcana-toc/internal/platform"
)

// EndReason explains why playback of one source terminated.
type EndReason string

const (
	EndEOF     EndReason = "eof"     // natural completion
	EndStopped EndReason = "stopped" // stopped by request
	EndError   EndReason = "error"   // backend failure
)

// Result is delivered exactly once when playback terminates.
type Result struct {
	Reason EndReason
	Err    error
}

// Status is the current observable state of active playback.
type Status struct {
	Paused   bool          `json:"paused"`
	Position time.Duration `json:"position"`
	Duration time.Duration `json:"duration"`
	Volume   int           `json:"volume"`
}

// Handle controls one active playback. Implementations must be safe for
// concurrent use; calling after Done is a no-op or an error, never a hang.
type Handle interface {
	Pause() error
	Resume() error
	Stop() error
	// Seek jumps to an absolute position from the start of the source.
	Seek(offset time.Duration) error
	// SetVolume sets loudness in percent (0-100).
	SetVolume(percent int) error
	// Status reports the acknowledged state currently held by the backend.
	Status() (Status, error)
	// Done resolves exactly once with the termination outcome.
	Done() <-chan Result
}

// Player starts playback of prepared sources. Sources of kind SourceClient
// (Spotify) belong to dedicated-client backends, not URL players.
type Player interface {
	Capabilities() platform.Capabilities
	// Play bounds source preparation with prepareCtx; after delivery only
	// playbackCtx controls the handle's lifetime. Both contexts must be non-nil.
	Play(prepareCtx, playbackCtx context.Context, source platform.ResolvedSource) (Handle, error)
	// Close releases backend resources and is safe to call during shutdown.
	Close() error
}
