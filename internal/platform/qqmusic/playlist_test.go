package qqmusic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func playlistSongFixture(mid string) map[string]any {
	return map[string]any{
		"id": 1, "mid": mid, "name": mid, "type": 0, "singer": []any{},
		"album": map[string]any{}, "mv": map[string]any{}, "file": map[string]any{}, "pay": map[string]any{},
		"interval": 1, "isonly": 0, "language": 0, "genre": 0, "index_cd": 0, "index_album": 0,
		"status": 0, "label": "", "bpm": 0, "ov": 0, "sa": 0, "es": "", "vs": []string{}, "vi": []int{}, "vf": []float64{},
	}
}

func TestPlaylistSongsFollowsOffsetsAndPreservesOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Req struct {
				Param struct {
					Begin int `json:"song_begin"`
				} `json:"param"`
			} `json:"req_0"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		begin := request.Req.Param.Begin
		if begin != 0 && begin != 2 {
			t.Errorf("unexpected offset %d", begin)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		songs := []any{playlistSongFixture("songFirst"), playlistSongFixture("songSecond")}
		more := 1
		if begin == 2 {
			songs = []any{playlistSongFixture("songThird")}
			more = 0
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "req_0": map[string]any{"code": 0, "data": map[string]any{
			"total_song_num": 3, "songlist_size": len(songs), "hasmore": more, "songlist": songs,
		}}})
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	a := NewForTest(server.Client(), endpoint, Credential{})
	hits, err := a.PlaylistSongs(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("got %d songs, want 3", len(hits))
	}
	for i, want := range []string{"songFirst", "songSecond", "songThird"} {
		if hits[i].Ref.TrackID != want {
			t.Fatalf("song %d = %s, want %s", i, hits[i].Ref.TrackID, want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.PlaylistSongs(ctx, "123"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestPlaylistPageRejectsIncompleteResults(t *testing.T) {
	cases := []map[string]any{
		{},
		{"total_song_num": 1, "songlist_size": 0, "songlist": []any{}},
		{"total_song_num": 0, "songlist_size": 0, "songlist": []any{}, "hasmore": 1},
		{"total_song_num": 0, "songlist_size": 0, "songlist": []any{}, "subcode": 100},
		{"total_song_num": 0.5, "songlist_size": 0, "songlist": []any{}},
	}
	for i, data := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, _, err := parsePlaylistPage(data, 0, -1); err == nil {
				t.Fatal("accepted incomplete or failed response")
			}
		})
	}
	if _, _, err := parsePlaylistPage(map[string]any{"total_song_num": 2, "songlist_size": 0, "songlist": []any{}}, 2, 3); err == nil {
		t.Fatal("accepted changing playlist total")
	}
}
