package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LingLambda/arcana-toc/internal/config"
	"github.com/LingLambda/arcana-toc/internal/platform"
	"github.com/LingLambda/arcana-toc/internal/player"
	"github.com/LingLambda/arcana-toc/internal/store"
)

type playlistCatalog struct {
	platform.Adapter
	hits       []platform.SearchHit
	err        error
	resolveErr error
}

func (a *playlistCatalog) SearchPlaylists(context.Context, string, platform.SearchOptions) (platform.SearchPage[platform.Playlist], error) {
	return platform.SearchPage[platform.Playlist]{Items: []platform.Playlist{{Platform: platform.Netease, ID: "123", Name: "list"}}}, a.err
}
func (a *playlistCatalog) PlaylistSongs(context.Context, string) ([]platform.SearchHit, error) {
	return a.hits, a.err
}
func (a *playlistCatalog) ResolveSource(_ context.Context, ref platform.TrackRef, _ string) (platform.ResolvedSource, error) {
	return platform.ResolvedSource{Kind: platform.SourceClient, URI: ref.String()}, a.resolveErr
}

type playlistPlayer struct{ player.Player }

func (*playlistPlayer) Play(context.Context, platform.ResolvedSource) (player.Handle, error) {
	return &playlistHandle{done: make(chan player.Result, 1)}, nil
}
func (*playlistPlayer) Close() error { return nil }

type playlistHandle struct {
	player.Handle
	done chan player.Result
	once sync.Once
}

func (h *playlistHandle) Stop() error {
	h.once.Do(func() { h.done <- player.Result{Reason: player.EndStopped} })
	return nil
}
func (*playlistHandle) SetVolume(int) error            { return nil }
func (*playlistHandle) Status() (player.Status, error) { return player.Status{}, nil }
func (h *playlistHandle) Done() <-chan player.Result   { return h.done }

func playlistServer(t *testing.T) (*Server, *playlistCatalog, *store.Store) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &playlistCatalog{hits: []platform.SearchHit{
		{Ref: platform.TrackRef{Platform: platform.Netease, TrackID: "first"}, Title: "First"},
		{Ref: platform.TrackRef{Platform: platform.Netease, TrackID: "second"}},
		{Ref: platform.TrackRef{Platform: platform.Netease, TrackID: "first"}},
	}}
	s := New(Deps{Config: config.Default(), Store: db, Adapters: map[platform.Platform]platform.Adapter{platform.Netease: a}, Player: &playlistPlayer{}})
	t.Cleanup(func() { _ = s.Close(); _ = db.Close() })
	return s, a, db
}
func playlistRequest(s *Server) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/playlists/play", strings.NewReader(`{"platform":"netease","id":"123"}`)))
	return w
}
func TestPlaylistReplacesActiveQueueAndStartsFirstInRandomMode(t *testing.T) {
	s, _, db := playlistServer(t)
	old, err := db.AddQueueEntry(store.QueueEntry{Platform: "netease", TrackID: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddQueueEntry(store.QueueEntry{Platform: "qqmusic", TrackID: "discard"}); err != nil {
		t.Fatal(err)
	}
	s.pbMu.Lock()
	_, _, err = s.startPendingLocked(context.Background(), false)
	oldHandle := s.pbHandle.(*playlistHandle)
	s.lastState.Mode = playbackModeRandom
	s.pbMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	w := playlistRequest(s)
	if w.Code != http.StatusAccepted {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	snap := s.currentState()
	if snap.Entry == nil || snap.Entry.TrackID != "first" || snap.Mode != playbackModeRandom {
		t.Fatalf("snapshot=%+v", snap)
	}
	rows, err := db.QueueEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].TrackID != "first" || rows[1].TrackID != "second" || rows[2].TrackID != "first" || rows[0].Status != "playing" || rows[1].Status != "pending" {
		t.Fatalf("queue=%+v", rows)
	}
	for _, row := range rows {
		if row.ID <= old {
			t.Fatalf("reused old identity: %+v", row)
		}
	}
	history, err := db.HistoryEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].QueueID != old || history[0].Outcome != "replaced" {
		t.Fatalf("history=%+v", history)
	}
	// A delayed old completion must not archive or advance the replacement queue.
	if err := oldHandle.Stop(); err != nil {
		t.Fatal(err)
	}
	s.pbMu.Lock()
	active := s.pbHandle.(*playlistHandle)
	s.pbMu.Unlock()
	active.done <- player.Result{Reason: player.EndEOF}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap = s.currentState()
		if snap.Entry != nil && snap.Entry.ID != rows[0].ID {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if snap.Entry == nil || snap.Entry.ID == rows[0].ID || (snap.Entry.ID != rows[1].ID && snap.Entry.ID != rows[2].ID) {
		t.Fatalf("automatic next=%+v", snap)
	}
}
func TestPlaylistReadFailuresLeaveCurrentPlaybackUntouched(t *testing.T) {
	for _, kind := range []string{"upstream", "empty", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			s, a, db := playlistServer(t)
			id, err := db.AddQueueEntry(store.QueueEntry{Platform: "netease", TrackID: "old"})
			if err != nil {
				t.Fatal(err)
			}
			s.pbMu.Lock()
			_, _, err = s.startPendingLocked(context.Background(), false)
			original := s.pbHandle
			s.pbMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "upstream":
				a.err = errors.New("unavailable")
			case "empty":
				a.hits = nil
			case "invalid":
				a.hits[0].Ref.TrackID = ""
			}
			w := playlistRequest(s)
			if w.Code < 400 {
				t.Fatalf("status=%d", w.Code)
			}
			rows, err := db.QueueEntries()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].ID != id || rows[0].Status != "playing" {
				t.Fatalf("queue=%+v", rows)
			}
			s.pbMu.Lock()
			unchanged := s.pbHandle == original
			s.pbMu.Unlock()
			if !unchanged {
				t.Fatal("active playback replaced")
			}
		})
	}
}
func TestPlaylistPlaybackFailureRetainsReplacementForRetry(t *testing.T) {
	s, a, db := playlistServer(t)
	if _, err := db.AddQueueEntry(store.QueueEntry{Platform: "netease", TrackID: "old"}); err != nil {
		t.Fatal(err)
	}
	a.resolveErr = platform.ErrPermissionDenied
	w := playlistRequest(s)
	if w.Code < 400 {
		t.Fatalf("status=%d", w.Code)
	}
	rows, err := db.QueueEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].TrackID != "first" || rows[0].Status != "pending" {
		t.Fatalf("queue=%+v", rows)
	}
	if snap := s.currentState(); snap.Entry != nil || snap.State != "idle" || snap.Error == "" {
		t.Fatalf("snapshot=%+v", snap)
	}
}

func TestPlaylistPlayOutlivesOrdinaryControlWriteDeadline(t *testing.T) {
	s, _, db := playlistServer(t)
	httpServer := httptest.NewUnstartedServer(s.Handler())
	// An already-expiring ordinary deadline must not govern this long operation.
	httpServer.Config.WriteTimeout = time.Nanosecond
	httpServer.Start()
	defer httpServer.Close()
	resp, err := httpServer.Client().Post(httpServer.URL+"/api/playlists/play", "application/json", strings.NewReader(`{"platform":"netease","id":"123"}`))
	if err != nil {
		t.Fatalf("playlist response lost to ordinary deadline: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	rows, err := db.QueueEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Status != "playing" {
		t.Fatalf("replacement did not start: %+v", rows)
	}
}
