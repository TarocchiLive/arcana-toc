package server

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/LingLambda/arcana-toc/internal/platform"
	"github.com/LingLambda/arcana-toc/internal/player"
	"github.com/LingLambda/arcana-toc/internal/store"
)

func pathID(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }

const (
	playbackModeSequential = "sequential"
	playbackModeRandom     = "random"
)

func validPlaybackMode(mode string) bool {
	return mode == playbackModeSequential || mode == playbackModeRandom
}

type playbackSnapshot struct {
	State    string            `json:"state"`
	Entry    *store.QueueEntry `json:"entry,omitempty"`
	Position time.Duration     `json:"position"`
	Duration time.Duration     `json:"duration"`
	Volume   int               `json:"volume"`
	Mode     string            `json:"mode"`
	Error    string            `json:"error,omitempty"`
}

func (s *Server) currentState() playbackSnapshot {
	s.pbMu.Lock()
	defer s.pbMu.Unlock()
	return s.snapshotLocked()
}
func (s *Server) snapshotLocked() playbackSnapshot {
	snap := s.lastState
	if !validPlaybackMode(snap.Mode) {
		snap.Mode = playbackModeSequential
	}
	if s.pbEntry != nil {
		entry := *s.pbEntry
		snap.Entry = &entry
	}
	if s.pbHandle != nil {
		snap.State = "playing"
		status, err := s.pbHandle.Status()
		if err != nil {
			snap.Error = "playback status unavailable"
			return snap
		}
		if status.Paused {
			snap.State = "paused"
		}
		snap.Position = status.Position
		snap.Duration = status.Duration
		// Volume is a persisted global setting, not a per-handle snapshot.
		snap.Error = ""
	}
	return snap
}
func (s *Server) persistLocked() error {
	s.lastState = s.snapshotLocked()
	return s.store.SetState("playback", s.lastState)
}
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"version": s.version, "playback": s.currentState()})
}

type playbackFailure struct {
	status  int
	message string
	cause   error
}

func (e *playbackFailure) Error() string { return e.message }
func (e *playbackFailure) Unwrap() error { return e.cause }

func writePlaybackStartError(w http.ResponseWriter, err error) {
	var failure *playbackFailure
	if errors.As(err, &failure) {
		httpError(w, failure.status, "%s", failure.message)
		return
	}
	reply(w, nil, err)
}

func (s *Server) markAutomaticFailure(entry store.QueueEntry, err error) error {
	if archiveErr := s.store.ArchiveQueueEntry(entry.ID, "failed"); archiveErr != nil {
		s.log.Error("archive automatic playback failure failed", "err", archiveErr)
	}
	return err
}

func (s *Server) startPendingLocked(requestCtx context.Context, automatic bool) (store.QueueEntry, platform.ResolvedSource, error) {
	entries, err := s.store.QueueEntries("pending")
	if err != nil {
		return store.QueueEntry{}, platform.ResolvedSource{}, &playbackFailure{status: http.StatusInternalServerError, message: "read queue failed", cause: err}
	}
	if len(entries) == 0 {
		return store.QueueEntry{}, platform.ResolvedSource{}, &playbackFailure{status: http.StatusConflict, message: "queue has no pending entries", cause: errors.New("no pending queue entries")}
	}
	entry := entries[0]
	if s.lastState.Mode == playbackModeRandom {
		entry = entries[rand.IntN(len(entries))]
	}
	return s.startEntryLocked(requestCtx, entry, automatic)
}

func (s *Server) startEntryLocked(requestCtx context.Context, entry store.QueueEntry, automatic bool) (store.QueueEntry, platform.ResolvedSource, error) {
	adapter, ok := s.adapters[platform.Platform(entry.Platform)]
	if !ok {
		failure := &playbackFailure{status: http.StatusNotImplemented, message: "platform adapter not available", cause: errors.New("platform adapter not available")}
		if automatic {
			return store.QueueEntry{}, platform.ResolvedSource{}, s.markAutomaticFailure(entry, failure)
		}
		return store.QueueEntry{}, platform.ResolvedSource{}, failure
	}
	ctx, cancel := context.WithCancel(requestCtx)
	stopCancel := context.AfterFunc(s.ctx, cancel)
	defer func() { stopCancel(); cancel() }()
	source, err := s.freshSource(ctx, adapter, platform.TrackRef{Platform: platform.Platform(entry.Platform), TrackID: entry.TrackID})
	if err != nil {
		if automatic {
			return store.QueueEntry{}, platform.ResolvedSource{}, s.markAutomaticFailure(entry, err)
		}
		return store.QueueEntry{}, platform.ResolvedSource{}, err
	}
	handle, err := s.player.Play(s.ctx, source)
	if err != nil {
		if automatic {
			return store.QueueEntry{}, platform.ResolvedSource{}, s.markAutomaticFailure(entry, err)
		}
		return store.QueueEntry{}, platform.ResolvedSource{}, err
	}
	if err := handle.SetVolume(s.lastState.Volume); err != nil {
		_ = handle.Stop()
		if automatic {
			return store.QueueEntry{}, platform.ResolvedSource{}, s.markAutomaticFailure(entry, err)
		}
		return store.QueueEntry{}, platform.ResolvedSource{}, err
	}
	if err := s.store.UpdateQueueStatus(entry.ID, "playing"); err != nil {
		_ = handle.Stop()
		return store.QueueEntry{}, platform.ResolvedSource{}, &playbackFailure{status: http.StatusInternalServerError, message: "persist playback failed", cause: err}
	}
	entry.Status = "playing"
	s.pbHandle = handle
	s.pbEntry = &entry
	s.lastState.Position = 0
	s.lastState.Duration = 0
	s.lastState.Error = ""
	if err := s.persistLocked(); err != nil {
		_ = handle.Stop()
		s.pbHandle = nil
		s.pbEntry = nil
		_ = s.store.ArchiveQueueEntry(entry.ID, "failed")
		return store.QueueEntry{}, platform.ResolvedSource{}, &playbackFailure{status: http.StatusInternalServerError, message: "persist playback failed", cause: err}
	}
	s.monitors.Add(1)
	go s.monitor(entry, handle)
	return entry, source, nil
}

// Starts the next pending track according to the persisted playback mode.
func (s *Server) handlePlaybackStart(w http.ResponseWriter, r *http.Request) {
	s.pbMu.Lock()
	defer s.pbMu.Unlock()
	if s.closed {
		httpError(w, http.StatusServiceUnavailable, "service stopping")
		return
	}
	if s.pbHandle != nil {
		httpError(w, http.StatusConflict, "playback already active")
		return
	}
	entry, source, err := s.startPendingLocked(r.Context(), false)
	if err != nil {
		writePlaybackStartError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"entry_id": entry.ID, "state": "playing", "quality": source.Quality})
}

// Skips the active entry, if any, and starts the next pending entry according to mode.
// With no active playback it behaves like /api/playback/start.
func (s *Server) handlePlaybackNext(w http.ResponseWriter, r *http.Request) {
	s.pbMu.Lock()
	defer s.pbMu.Unlock()
	if s.closed {
		httpError(w, http.StatusServiceUnavailable, "service stopping")
		return
	}
	if entries, err := s.store.QueueEntries("pending"); err != nil {
		httpError(w, http.StatusInternalServerError, "read queue failed")
		return
	} else if len(entries) == 0 {
		httpError(w, http.StatusConflict, "queue has no pending entries")
		return
	}
	if s.pbHandle != nil {
		if s.pbEntry == nil {
			httpError(w, http.StatusInternalServerError, "playback state unavailable")
			return
		}
		h := s.pbHandle
		entry := *s.pbEntry
		if err := h.Stop(); err != nil {
			httpError(w, http.StatusBadGateway, "playback command failed")
			return
		}
		if err := s.store.ArchiveQueueEntry(entry.ID, "skipped"); err != nil {
			httpError(w, http.StatusInternalServerError, "archive playback failed")
			return
		}
		s.pbHandle = nil
		s.pbEntry = nil
		s.lastState.State = "idle"
		s.lastState.Entry = nil
		s.lastState.Position = 0
		s.lastState.Duration = 0
		s.lastState.Error = ""
		if err := s.persistLocked(); err != nil {
			httpError(w, http.StatusInternalServerError, "persist playback failed")
			return
		}
	}
	entry, source, err := s.startPendingLocked(r.Context(), false)
	if err != nil {
		writePlaybackStartError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"entry_id": entry.ID, "state": "playing", "quality": source.Quality})
}
func (s *Server) freshSource(ctx context.Context, a platform.Adapter, ref platform.TrackRef) (platform.ResolvedSource, error) {
	for attempt := 0; attempt < 2; attempt++ {
		source, err := a.ResolveSource(ctx, ref, "")
		if err != nil {
			return source, err
		}
		// Protocol preparation belongs to the adapter. Players only see authorized URLs.
		if source.Kind != platform.SourceURL {
			return source, nil
		}
		// Synthetic sources in offline consumer tests use a different kind. Real URL sources are checked.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
		if err != nil {
			return source, errors.New("invalid source")
		}
		req.Header.Set("Range", "bytes=0-0")
		response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return source, errors.New("source CDN unavailable")
		}
		_ = response.Body.Close()
		if response.StatusCode == 403 {
			continue
		}
		if response.StatusCode != 200 && response.StatusCode != 206 {
			return source, errors.New("source CDN rejected playback")
		}
		return source, nil
	}
	return platform.ResolvedSource{}, platform.ErrSourceExpired
}
func (s *Server) monitor(entry store.QueueEntry, h player.Handle) {
	defer s.monitors.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case res, ok := <-h.Done():
			s.pbMu.Lock()
			if s.pbHandle != h {
				s.pbMu.Unlock()
				return
			}
			if !ok {
				res = player.Result{Reason: player.EndError}
			}
			outcome := "completed"
			if res.Reason == player.EndStopped {
				outcome = "stopped"
			}
			if res.Reason == player.EndError {
				outcome = "failed"
			}
			if s.closed {
				outcome = "interrupted"
			}
			terminalErr := s.store.ArchiveQueueEntry(entry.ID, outcome)
			if terminalErr != nil {
				s.log.Error("persist terminal status failed")
			}
			if s.pbHandle == h {
				s.lastState = s.snapshotLocked()
				s.lastState.State = "idle"
				s.lastState.Entry = nil
				s.lastState.Position = 0
				s.lastState.Duration = 0
				if res.Reason == player.EndError {
					s.lastState.Error = "playback failed"
				} else {
					s.lastState.Error = ""
				}
				if s.closed {
					s.lastState.State = "interrupted"
					entry.Status = outcome
					s.lastState.Entry = &entry
				}
				s.pbHandle = nil
				s.pbEntry = nil
				if err := s.persistLocked(); err != nil {
					s.log.Error("persist playback state failed")
					terminalErr = err
				}
				if res.Reason == player.EndEOF && !s.closed && terminalErr == nil {
					ctx, cancel := context.WithTimeout(s.ctx, 45*time.Second)
					_, _, err := s.startPendingLocked(ctx, true)
					cancel()
					var failure *playbackFailure
					if err != nil && (!errors.As(err, &failure) || failure.status != http.StatusConflict) {
						s.lastState.State = "idle"
						s.lastState.Entry = nil
						s.lastState.Error = "automatic next playback failed"
						if err := s.persistLocked(); err != nil {
							s.log.Error("persist automatic playback failure failed")
						}
					}
				}
			}
			s.pbMu.Unlock()
			return
		case <-s.ctx.Done():
			s.pbMu.Lock()
			if s.pbHandle == h {
				entry.Status = "interrupted"
				if err := s.store.ArchiveQueueEntry(entry.ID, entry.Status); err != nil {
					s.log.Error("archive interrupted playback failed")
				}
				s.lastState = s.snapshotLocked()
				s.lastState.State = "interrupted"
				s.lastState.Entry = &entry
				s.pbHandle = nil
				s.pbEntry = nil
				if err := s.persistLocked(); err != nil {
					s.log.Error("persist interrupted playback state failed")
				}
			}
			s.pbMu.Unlock()
			return
		case <-tick.C:
			s.pbMu.Lock()
			err := s.persistLocked()
			s.pbMu.Unlock()
			if err != nil {
				_ = h.Stop()
				s.log.Error("persist playback position failed")
			}
		}
	}
}

func (s *Server) Close() error {
	s.pbMu.Lock()
	s.closed = true
	h := s.pbHandle
	if h != nil {
		s.lastState = s.snapshotLocked()
	}
	s.pbMu.Unlock()

	var closeErr error
	if h != nil {
		closeErr = h.Stop()
	}
	s.cancel()
	s.monitors.Wait()
	if s.player != nil {
		closeErr = errors.Join(closeErr, s.player.Close())
	}
	return closeErr
}

type playbackModeRequest struct {
	Mode string `json:"mode"`
}

func (s *Server) handlePlaybackMode(w http.ResponseWriter, r *http.Request) {
	var req playbackModeRequest
	if !decode(w, r, &req) {
		return
	}
	if !validPlaybackMode(req.Mode) {
		httpError(w, http.StatusBadRequest, "mode must be sequential or random")
		return
	}
	s.pbMu.Lock()
	defer s.pbMu.Unlock()
	if s.closed {
		httpError(w, http.StatusServiceUnavailable, "service stopping")
		return
	}
	previous := s.lastState.Mode
	s.lastState.Mode = req.Mode
	if err := s.persistLocked(); err != nil {
		s.lastState.Mode = previous
		httpError(w, http.StatusInternalServerError, "persist playback mode failed")
		return
	}
	writeJSON(w, http.StatusOK, s.snapshotLocked())
}
