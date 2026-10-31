// Package server exposes the loopback control API and rejects browser origins.
// External access and subscriptions remain deferred.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/LingLambda/arcana-toc/internal/config"
	"github.com/LingLambda/arcana-toc/internal/platform"
	"github.com/LingLambda/arcana-toc/internal/player"
	"github.com/LingLambda/arcana-toc/internal/store"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Deps wires the server to its collaborators.
type Deps struct {
	Config   config.Config
	Logger   *slog.Logger
	Version  string
	Store    *store.Store
	Adapters map[platform.Platform]platform.Adapter
	Player   player.Player
	Music    Music
}

// Server wraps the control HTTP surface, including queue playback scheduling and mode persistence.
type Server struct {
	http            *http.Server
	log             *slog.Logger
	version         string
	store           *store.Store
	adapters        map[platform.Platform]platform.Adapter
	player          player.Player
	skipUnavailable bool
	allowPreview    bool
	skipOnError     bool
	sourceTimeout   time.Duration

	pbMu      sync.Mutex
	pbHandle  player.Handle
	pbEntry   *store.QueueEntry
	ctx       context.Context
	cancel    context.CancelFunc
	monitors  sync.WaitGroup
	closed    bool
	lastState playbackSnapshot
}

// New builds the server and its routing table.
func New(d Deps) *Server {
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		log:             d.Logger,
		version:         d.Version,
		store:           d.Store,
		adapters:        d.Adapters,
		player:          d.Player,
		skipUnavailable: d.Config.Player.SkipUnavailable,
		allowPreview:    d.Config.Player.AllowPreview,
		skipOnError:     d.Config.Player.SkipOnError,
		sourceTimeout:   d.Config.Player.SourceTimeout,
		ctx:             ctx, cancel: cancel, lastState: playbackSnapshot{State: "idle", Volume: 100, Mode: playbackModeSequential},
	}
	if d.Store != nil {
		if _, err := d.Store.GetState("playback", &s.lastState); err != nil {
			s.closed = true
			s.lastState.Error = "state recovery failed"
		}
		if entries, err := d.Store.QueueEntries("playing"); err == nil {
			for _, entry := range entries {
				_ = d.Store.ArchiveQueueEntry(entry.ID, "interrupted")
			}
		} else {
			s.closed = true
			s.lastState.Error = "queue recovery failed"
		}
		if s.lastState.State == "playing" || s.lastState.State == "paused" {
			s.lastState.State = "interrupted"
			if s.lastState.Entry != nil {
				s.lastState.Entry.Status = "interrupted"
			}
		}
		if s.lastState.Entry != nil && !s.closed {
			entries, err := d.Store.QueueEntries()
			if err != nil {
				s.closed = true
				s.lastState.Error = "queue recovery failed"
			} else {
				found := false
				for _, entry := range entries {
					if entry.ID == s.lastState.Entry.ID {
						found = true
						break
					}
				}
				if !found {
					s.lastState = playbackSnapshot{State: "idle", Volume: s.lastState.Volume, Mode: s.lastState.Mode}
					if err := s.persistLocked(); err != nil {
						s.closed = true
						s.lastState.Error = "state recovery failed"
					}
				}
			}
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /api/search", s.handleSearch)
	mux.HandleFunc("POST /api/artists/search", s.handleArtistSearch)
	mux.HandleFunc("GET /api/artists/{platform}/{id}/songs", s.handleArtistSongs)
	mux.HandleFunc("GET /api/playlists/search", s.handlePlaylistSearch)
	mux.HandleFunc("POST /api/playlists/play", s.handlePlaylistPlay)
	mux.HandleFunc("GET /api/queue", s.handleQueueList)
	mux.HandleFunc("GET /api/history", s.handleHistoryList)
	mux.HandleFunc("POST /api/queue", s.handleQueueEnqueue)
	mux.HandleFunc("DELETE /api/queue/{id}", s.handleQueueDelete)
	mux.HandleFunc("POST /api/playback/start", s.handlePlaybackStart)
	mux.HandleFunc("POST /api/playback/next", s.handlePlaybackNext)
	mux.HandleFunc("POST /api/playback/mode", s.handlePlaybackMode)
	mux.HandleFunc("POST /api/playback/pause", s.requireHandle(s.playPause(true)))
	mux.HandleFunc("POST /api/playback/resume", s.requireHandle(s.playPause(false)))
	mux.HandleFunc("POST /api/playback/stop", s.requireHandle(s.playStop))
	mux.HandleFunc("GET /api/status", s.handleStatus)
	bindMusic(mux, d.Music)
	mux.HandleFunc("POST /api/playback/seek", s.valueControl("seek"))
	mux.HandleFunc("POST /api/playback/volume", s.valueControl("volume"))

	s.http = &http.Server{
		Addr:              d.Config.Addr(),
		Handler:           controlPolicy(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second,
	}
	return s
}

// Handler exposes the routing table for tests and advanced embedding.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// ListenAndServe blocks serving until Shutdown is called. http.ErrServerClosed
// is swallowed: a clean shutdown is not an error.
func (s *Server) ListenAndServe() error {
	s.log.Info("http server listening", "addr", s.http.Addr)
	err := s.http.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown gracefully drains connections within the ctx deadline.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

// decodeBody decodes a JSON request body into dst (empty bodies allowed).
func decodeBody(r *http.Request, dst any) error {
	err := json.NewDecoder(r.Body).Decode(dst)
	if err != nil {
		if err.Error() == "EOF" {
			return nil
		}
		return err
	}
	return nil
}

type searchRequest struct {
	Platform string `json:"platform"`
	Keyword  string `json:"keyword"`
	Limit    int    `json:"limit"`
	Page     int    `json:"page"`
}

var aggregatePlatformOrder = [...]platform.Platform{platform.Netease, platform.QQMusic}

type adapterSelection struct {
	platform platform.Platform
	adapter  platform.Adapter
}

func (s *Server) selectAdapters(raw string) ([]adapterSelection, int, string) {
	name := strings.TrimSpace(raw)
	if name == "" || name == "all" {
		selected := make([]adapterSelection, 0, len(aggregatePlatformOrder))
		for _, p := range aggregatePlatformOrder {
			if adapter, ok := s.adapters[p]; ok {
				selected = append(selected, adapterSelection{platform: p, adapter: adapter})
			}
		}
		if len(selected) == 0 {
			return nil, http.StatusNotImplemented, "no platform adapters available"
		}
		return selected, 0, ""
	}

	p := platform.Platform(name)
	if p != platform.Netease && p != platform.QQMusic {
		return nil, http.StatusBadRequest, "platform must be netease, qqmusic, or all"
	}
	adapter, ok := s.adapters[p]
	if !ok {
		return nil, http.StatusNotImplemented, "platform adapter not available: " + name
	}
	return []adapterSelection{{platform: p, adapter: adapter}}, 0, ""
}

func decodeSearchRequest(w http.ResponseWriter, r *http.Request) (searchRequest, bool) {
	req := searchRequest{Page: 1}
	if !decode(w, r, &req) {
		return searchRequest{}, false
	}
	req.Keyword = strings.TrimSpace(req.Keyword)
	if req.Keyword == "" {
		httpError(w, http.StatusBadRequest, "keyword must not be empty")
		return searchRequest{}, false
	}
	if req.Limit < 0 {
		httpError(w, http.StatusBadRequest, "limit must not be negative")
		return searchRequest{}, false
	}
	if req.Page < 1 {
		httpError(w, http.StatusBadRequest, "page must be positive")
		return searchRequest{}, false
	}
	if req.Limit == 0 || req.Limit > 20 {
		req.Limit = 20
	}
	if err := (platform.SearchOptions{Limit: req.Limit, Page: req.Page}).Validate(); err != nil {
		httpError(w, http.StatusBadRequest, "%s", err)
		return searchRequest{}, false
	}
	return req, true
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeSearchRequest(w, r)
	if !ok {
		return
	}
	selected, status, message := s.selectAdapters(req.Platform)
	if status != 0 {
		httpError(w, status, "%s", message)
		return
	}

	type result struct {
		hits platform.SearchPage[platform.SearchHit]
		err  error
	}
	results := make([]result, len(selected))
	if len(selected) == 1 {
		results[0].hits, results[0].err = selected[0].adapter.Search(r.Context(), req.Keyword, platform.SearchOptions{Limit: req.Limit, Page: req.Page})
	} else {
		var group sync.WaitGroup
		group.Add(len(selected))
		for i, target := range selected {
			go func() {
				defer group.Done()
				results[i].hits, results[i].err = target.adapter.Search(r.Context(), req.Keyword, platform.SearchOptions{Limit: req.Limit, Page: req.Page})
			}()
		}
		group.Wait()
	}

	total := 0
	for i := range results {
		if results[i].err == nil {
			total += len(results[i].hits.Items)
		}
	}
	hits := make([]platform.SearchHit, 0, total)
	errorsByPlatform := make(map[string]string)
	hasMore := make(map[string]bool)
	for i, target := range selected {
		if results[i].err != nil {
			errorsByPlatform[string(target.platform)] = results[i].err.Error()
			continue
		}
		hits = append(hits, results[i].hits.Items...)
		hasMore[string(target.platform)] = results[i].hits.HasMore
	}
	response := map[string]any{"hits": hits, "count": len(hits), "errors": errorsByPlatform, "page": req.Page, "has_more": hasMore}
	if len(errorsByPlatform) == len(selected) {
		writeJSON(w, http.StatusBadGateway, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleArtistSearch(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeSearchRequest(w, r)
	if !ok {
		return
	}
	selected, status, message := s.selectAdapters(req.Platform)
	if status != 0 {
		httpError(w, status, "%s", message)
		return
	}

	type result struct {
		artists platform.SearchPage[platform.Artist]
		err     error
	}
	results := make([]result, len(selected))
	if len(selected) == 1 {
		adapter, supported := selected[0].adapter.(platform.ArtistAdapter)
		if !supported {
			results[0].err = platform.ErrUnsupportedCapability
		} else {
			results[0].artists, results[0].err = adapter.SearchArtists(r.Context(), req.Keyword, platform.SearchOptions{Limit: req.Limit, Page: req.Page})
		}
	} else {
		var group sync.WaitGroup
		group.Add(len(selected))
		for i, target := range selected {
			go func() {
				defer group.Done()
				adapter, supported := target.adapter.(platform.ArtistAdapter)
				if !supported {
					results[i].err = platform.ErrUnsupportedCapability
					return
				}
				results[i].artists, results[i].err = adapter.SearchArtists(r.Context(), req.Keyword, platform.SearchOptions{Limit: req.Limit, Page: req.Page})
			}()
		}
		group.Wait()
	}

	total := 0
	for i := range results {
		if results[i].err == nil {
			total += len(results[i].artists.Items)
		}
	}
	artists := make([]platform.Artist, 0, total)
	errorsByPlatform := make(map[string]string)
	hasMore := make(map[string]bool)
	for i, target := range selected {
		if results[i].err != nil {
			errorsByPlatform[string(target.platform)] = results[i].err.Error()
			continue
		}
		artists = append(artists, results[i].artists.Items...)
		hasMore[string(target.platform)] = results[i].artists.HasMore
	}
	response := map[string]any{"artists": artists, "count": len(artists), "errors": errorsByPlatform, "page": req.Page, "has_more": hasMore}
	if len(errorsByPlatform) == len(selected) {
		writeJSON(w, http.StatusBadGateway, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleArtistSongs(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("platform"))
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		httpError(w, http.StatusBadRequest, "artist id must not be empty")
		return
	}
	if name != string(platform.Netease) && name != string(platform.QQMusic) {
		httpError(w, http.StatusBadRequest, "platform must be netease or qqmusic")
		return
	}
	selected, status, message := s.selectAdapters(name)
	if status != 0 {
		httpError(w, status, "%s", message)
		return
	}
	if len(selected) != 1 {
		httpError(w, http.StatusBadRequest, "artist songs require one platform")
		return
	}
	adapter, ok := selected[0].adapter.(platform.ArtistAdapter)
	if !ok {
		reply(w, nil, platform.ErrUnsupportedCapability)
		return
	}
	hits, err := adapter.ArtistSongs(r.Context(), id)
	if hits == nil && err == nil {
		hits = []platform.SearchHit{}
	}
	reply(w, map[string]any{"hits": hits}, err)
}

type enqueueRequest struct {
	Platform string `json:"platform"`
	TrackID  string `json:"track_id"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	VIP      bool   `json:"vip"`
}

func (s *Server) handleQueueEnqueue(w http.ResponseWriter, r *http.Request) {
	var req enqueueRequest
	if err := decodeBody(r, &req); err != nil {
		httpError(w, http.StatusBadRequest, "bad enqueue body: %v", err)
		return
	}
	p := platform.Platform(req.Platform)
	if !p.Valid() {
		httpError(w, http.StatusBadRequest, "unknown platform %q", req.Platform)
		return
	}
	if req.TrackID == "" {
		httpError(w, http.StatusBadRequest, "track_id must not be empty")
		return
	}
	s.pbMu.Lock()
	defer s.pbMu.Unlock()
	if s.closed {
		httpError(w, http.StatusServiceUnavailable, "service stopping")
		return
	}
	id, err := s.store.AddQueueEntry(store.QueueEntry{
		Platform: req.Platform,
		TrackID:  req.TrackID,
		Title:    req.Title,
		Artist:   req.Artist,
		VIP:      req.VIP,
	})
	if err != nil {
		httpError(w, http.StatusInternalServerError, "enqueue failed: %v", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) handleQueueList(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	var (
		entries []store.QueueEntry
		err     error
	)
	if status == "" {
		entries, err = s.store.QueueEntries()
	} else {
		entries, err = s.store.QueueEntries(status)
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, "list queue failed: %v", err)
		return
	}
	if entries == nil {
		entries = []store.QueueEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
}

func (s *Server) handleHistoryList(w http.ResponseWriter, _ *http.Request) {
	entries, err := s.store.HistoryEntries()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "list playback history failed: %v", err)
		return
	}
	if entries == nil {
		entries = []store.HistoryEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
}

func (s *Server) handleQueueDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "bad id: %v", err)
		return
	}
	s.pbMu.Lock()
	defer s.pbMu.Unlock()
	if s.closed {
		httpError(w, http.StatusServiceUnavailable, "service stopping")
		return
	}
	active := s.pbEntry != nil && s.pbEntry.ID == id && s.pbHandle != nil
	if active {
		if err := s.pbHandle.Stop(); err != nil {
			httpError(w, http.StatusBadGateway, "playback command failed")
			return
		}
		s.pbHandle = nil
		s.pbEntry = nil
	}
	if active {
		if err := s.store.ArchiveQueueEntry(id, "removed"); err != nil {
			httpError(w, http.StatusInternalServerError, "archive playback failed")
			return
		}
	} else if err := s.store.DeleteQueueEntry(id); err != nil {
		httpError(w, http.StatusNotFound, "queue entry %d not found", id)
		return
	}
	if active || (s.lastState.Entry != nil && s.lastState.Entry.ID == id) {
		s.lastState = playbackSnapshot{State: "idle", Volume: s.lastState.Volume, Mode: s.lastState.Mode}
		if err := s.persistLocked(); err != nil {
			httpError(w, http.StatusInternalServerError, "persist playback failed")
			return
		}
	}
	if active {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		_, _, nextErr := s.startPendingLocked(ctx, true)
		cancel()
		var failure *playbackFailure
		if nextErr != nil && !errors.Is(nextErr, errPlaybackRetryExhausted) && (!errors.As(nextErr, &failure) || failure.status != http.StatusConflict) {
			s.lastState.Error = "automatic next playback failed"
			_ = s.persistLocked()
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// requireHandle serializes selection, control, and the acknowledged snapshot.
func (s *Server) requireHandle(fn func(player.Handle) httpStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		s.pbMu.Lock()
		defer s.pbMu.Unlock()
		h := s.pbHandle
		if h == nil {
			httpError(w, http.StatusConflict, "no active playback")
			return
		}
		st := fn(h)
		if st.err != nil {
			httpError(w, st.code, "playback command failed")
			return
		}
		writeJSON(w, st.code, map[string]any{"state": s.snapshotLocked().State})
	}
}

func (s *Server) playPause(pause bool) func(player.Handle) httpStatus {
	return func(h player.Handle) httpStatus {
		var err error
		if pause {
			err = h.Pause()
		} else {
			err = h.Resume()
		}
		if err == nil {
			err = s.persistLocked()
		}
		return httpStatusFrom(err)
	}
}

func (s *Server) playStop(h player.Handle) httpStatus {
	return httpStatusFrom(h.Stop())
}

// httpStatus is a tiny helper so requireHandle wrappers stay terse.
type httpStatus struct {
	code int
	err  error
}

func httpStatusFrom(err error) httpStatus {
	if err != nil {
		return httpStatus{code: http.StatusBadGateway, err: err}
	}
	return httpStatus{code: http.StatusOK}
}
