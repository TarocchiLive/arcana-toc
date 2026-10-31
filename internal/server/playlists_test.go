package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LingLambda/arcana-toc/internal/config"
	"github.com/LingLambda/arcana-toc/internal/platform"
	"github.com/LingLambda/arcana-toc/internal/player"
	"github.com/LingLambda/arcana-toc/internal/store"
)

type playlistCatalog struct {
	platform.Adapter
	hits       []platform.SearchHit
	resolveErr error
}

func (*playlistCatalog) SearchPlaylists(context.Context, string, platform.SearchOptions) (platform.SearchPage[platform.Playlist], error) {
	return platform.SearchPage[platform.Playlist]{}, platform.ErrUnsupportedCapability
}

func (a *playlistCatalog) PlaylistSongs(context.Context, string) ([]platform.SearchHit, error) {
	return a.hits, nil
}
func (a *playlistCatalog) ResolveSource(_ context.Context, ref platform.TrackRef, _ string) (platform.ResolvedSource, error) {
	return platform.ResolvedSource{Kind: platform.SourceClient, URI: ref.String()}, a.resolveErr
}

type playlistPlayer struct{ player.Player }

func (*playlistPlayer) Play(context.Context, context.Context, platform.ResolvedSource) (player.Handle, error) {
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
}
