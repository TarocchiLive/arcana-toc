package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LingLambda/arcana-toc/internal/platform"
	"github.com/LingLambda/arcana-toc/internal/store"
)

func (s *Server) handlePlaylistSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		httpError(w, http.StatusBadRequest, "keyword must not be empty")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			httpError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpError(w, http.StatusBadRequest, "page must be positive")
			return
		}
		page = n
	}
	if limit == 0 || limit > 20 {
		limit = 20
	}
	if err := (platform.SearchOptions{Limit: limit, Page: page}).Validate(); err != nil {
		httpError(w, http.StatusBadRequest, "%s", err)
		return
	}
	selected, status, message := s.selectAdapters(r.URL.Query().Get("platform"))
	if status != 0 {
		httpError(w, status, "%s", message)
		return
	}
	type result struct {
		playlists platform.SearchPage[platform.Playlist]
		err       error
	}
	results := make([]result, len(selected))
	var group sync.WaitGroup
	for i, target := range selected {
		group.Add(1)
		go func() {
			defer group.Done()
			adapter, ok := target.adapter.(platform.PlaylistAdapter)
			if !ok {
				results[i].err = platform.ErrUnsupportedCapability
				return
			}
			results[i].playlists, results[i].err = adapter.SearchPlaylists(r.Context(), query, platform.SearchOptions{Limit: limit, Page: page})
		}()
	}
	group.Wait()
	playlists := []platform.Playlist{}
	failures := make(map[string]string)
	hasMore := make(map[string]bool)
	for i, target := range selected {
		if results[i].err != nil {
			failures[string(target.platform)] = results[i].err.Error()
			continue
		}
		playlists = append(playlists, results[i].playlists.Items...)
		hasMore[string(target.platform)] = results[i].playlists.HasMore
	}
	status = http.StatusOK
	if len(failures) == len(selected) {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]any{"playlists": playlists, "count": len(playlists), "errors": failures, "page": page, "has_more": hasMore})
}

func (s *Server) handlePlaylistPlay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Platform platform.Platform `json:"platform"`
		ID       string            `json:"id"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" || (req.Platform != platform.Netease && req.Platform != platform.QQMusic) {
		httpError(w, http.StatusBadRequest, "playlist requires netease or qqmusic and a nonempty id")
		return
	}
	adapter, ok := s.adapters[req.Platform].(platform.PlaylistAdapter)
	if !ok {
		reply(w, nil, platform.ErrUnsupportedCapability)
		return
	}
	// Complete catalog loading and initial audio preparation can exceed the
	// ordinary control API's 45-second response deadline.
	deadline := time.Now().Add(3 * time.Minute)
	if err := http.NewResponseController(w).SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		httpError(w, http.StatusInternalServerError, "set playlist response deadline failed")
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	stopCancel := context.AfterFunc(s.ctx, cancel)
	defer func() { stopCancel(); cancel() }()
	hits, err := adapter.PlaylistSongs(ctx, req.ID)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if len(hits) == 0 {
		httpError(w, http.StatusConflict, "playlist is empty; queue unchanged")
		return
	}
	entries := make([]store.QueueEntry, len(hits))
	for i, hit := range hits {
		if hit.Ref.Platform != req.Platform || hit.Ref.TrackID == "" {
			httpError(w, http.StatusBadGateway, "playlist contains invalid tracks; queue unchanged")
			return
		}
		entries[i] = store.QueueEntry{Platform: string(hit.Ref.Platform), TrackID: hit.Ref.TrackID,
			Title: hit.Title, Artist: strings.Join(hit.Artists, ", "), VIP: hit.PaidHint}
	}
	s.pbMu.Lock()
	defer s.pbMu.Unlock()
	if s.closed {
		httpError(w, http.StatusServiceUnavailable, "service stopping")
		return
	}
	if ctx.Err() != nil {
		reply(w, nil, ctx.Err())
		return
	}
	if s.pbHandle != nil {
		if err := s.pbHandle.Stop(); err != nil {
			httpError(w, http.StatusBadGateway, "playback command failed; queue unchanged")
			return
		}
		s.pbHandle = nil
		s.pbEntry = nil
	}
	s.lastState = playbackSnapshot{State: "idle", Volume: s.lastState.Volume, Mode: s.lastState.Mode}
	if err := s.store.ReplaceQueue(entries); err != nil {
		s.lastState.Error = "replace queue failed; playback stopped"
		s.log.Error("replace playlist queue failed after playback stop", "err", err)
		_ = s.persistLocked()
		httpError(w, http.StatusInternalServerError, "replace queue failed")
		return
	}
	if err := s.persistLocked(); err != nil {
		httpError(w, http.StatusInternalServerError, "persist playback failed")
		return
	}
	pending, err := s.store.QueueEntries("pending")
	if err != nil || len(pending) == 0 {
		httpError(w, http.StatusInternalServerError, "read replacement queue failed")
		return
	}
	// The initial track follows playlist order even when subsequent playback is random.
	if _, _, err := s.startEntriesLocked(ctx, pending, false, false); err != nil {
		if !errors.Is(err, errPlaybackRetryExhausted) {
			s.lastState.Error = "playlist replaced; playback start failed"
			_ = s.persistLocked()
		}
		writePlaybackStartError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.snapshotLocked())
}
