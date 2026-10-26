// Package platform defines the adapter contract shared by the queue, player,
// and HTTP API. Login flows remain platform-specific.
package platform

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Platform identifies a supported music source.
type Platform string

const (
	Netease Platform = "netease"
	QQMusic Platform = "qqmusic"
)

// Valid reports whether p is one of the known platforms.
func (p Platform) Valid() bool {
	switch p {
	case Netease, QQMusic:
		return true
	default:
		return false
	}
}

// TrackRef is a stable song identity. Signed playback URLs never belong here.
type TrackRef struct {
	Platform Platform `json:"platform"`
	TrackID  string   `json:"track_id"`
}

func (t TrackRef) String() string { return string(t.Platform) + ":" + t.TrackID }

// SourceKind distinguishes URL-delivered sources from URI-driven dedicated
// clients (invariant 4: no forced GetURL() normalization).
type SourceKind string

const (
	SourceURL    SourceKind = "url"    // direct playback URL, resolved near playback
	SourceClient SourceKind = "client" // dedicated client plays a URI (Spotify)
)

// ResolvedSource is the near-playback result of source preparation
// (invariant 3). ExpiresAt is advisory only: measurements show nominal
// expiries can both under- and over-state real link lifetimes, so liveness
// must be verified by fetching, never by expiry math.
type ResolvedSource struct {
	Kind      SourceKind `json:"kind"`
	URL       string     `json:"url,omitempty"` // SourceURL only; never persisted
	URI       string     `json:"uri,omitempty"` // SourceClient only
	Quality   string     `json:"quality,omitempty"`
	ExpiresAt time.Time  `json:"expires_at,omitempty"`
}

// Capabilities states what an adapter honestly supports (invariant 5).
type Capabilities struct {
	DirectURL bool
	Seek      bool
	Volume    bool
	Qualities []string
}

// SearchHit is a catalog entry returned by Search. PaidHint is cosmetic and
// never authoritative: the final permission decision happens at resolve time.
type SearchHit struct {
	Ref      TrackRef      `json:"ref"`
	Title    string        `json:"title"`
	Artists  []string      `json:"artists"`
	Album    string        `json:"album,omitempty"`
	Duration time.Duration `json:"duration_ns,omitempty"`
	PaidHint bool          `json:"paid_hint"`
}

// Artist is a platform-qualified catalog artist. ID is the provider's stable
// identifier accepted by ArtistAdapter.ArtistSongs.
type Artist struct {
	Platform Platform `json:"platform"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
}

// Playlist is a platform-qualified playlist accepted by PlaylistAdapter.
type Playlist struct {
	Platform Platform `json:"platform"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
}

// SearchOptions uses one-based pages and a stable page size across providers.
type SearchOptions struct {
	Limit int
	Page  int
}

func (o SearchOptions) Normalize() SearchOptions {
	if o.Limit <= 0 || o.Limit > 20 {
		o.Limit = 20
	}
	if o.Page <= 0 {
		o.Page = 1
	}
	return o
}

// Validate checks normalized pagination before provider offsets are computed.
func (o SearchOptions) Validate() error {
	if o.Limit < 1 || o.Limit > 20 || o.Page < 1 {
		return errors.New("invalid search pagination")
	}
	// Reserve space for a full page as continuation checks include its items.
	if o.Page > int(^uint(0)>>1)/o.Limit {
		return errors.New("search page is too large")
	}
	return nil
}

type SearchPage[T any] struct {
	Items   []T
	HasMore bool
}

// Adapter is the queue-facing contract every platform backend implements.
type Adapter interface {
	Platform() Platform
	Capabilities() Capabilities
	Search(ctx context.Context, keyword string, options SearchOptions) (SearchPage[SearchHit], error)
	// ResolveSource prepares playback for ref at the requested quality.
	// Implementations must resolve near playback time and return typed
	// errors below instead of substituting alternate sources or qualities.
	ResolveSource(ctx context.Context, ref TrackRef, quality string) (ResolvedSource, error)
}

// ArtistAdapter is an optional catalog extension implemented by platforms
// that support artist discovery and artist song listings.
type ArtistAdapter interface {
	SearchArtists(ctx context.Context, keyword string, options SearchOptions) (SearchPage[Artist], error)
	ArtistSongs(ctx context.Context, id string) ([]SearchHit, error)
}

// PlaylistAdapter discovers playlists and reads their complete, ordered songs.
// An incomplete or inaccessible listing must return an error, not partial songs.
type PlaylistAdapter interface {
	SearchPlaylists(ctx context.Context, keyword string, options SearchOptions) (SearchPage[Playlist], error)
	PlaylistSongs(ctx context.Context, id string) ([]SearchHit, error)
}

// Sentinel errors: adapters wrap these so callers can branch on semantics
// (invariant 5: explicit failure, no silent substitution).
var (
	ErrPermissionDenied      = errors.New("platform: permission denied for track or quality")
	ErrSourceExpired         = errors.New("platform: source link expired")
	ErrUnsupportedCapability = errors.New("platform: unsupported capability")
	ErrTrackNotFound         = errors.New("platform: track not found")
	ErrNotLoggedIn           = errors.New("platform: account not logged in")
	ErrVerificationRequired  = errors.New("platform: account verification required")
)

// QualityError reports that a requested quality does not exist for a track.
type QualityError struct {
	Track   TrackRef
	Quality string
}

func (e *QualityError) Error() string {
	return fmt.Sprintf("platform: quality %q not available for %s", e.Quality, e.Track)
}
