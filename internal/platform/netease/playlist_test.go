package netease

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlaylistReusesMetadataAndCompletesOnlyMissingSongs(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			a, err := New(filepath.Join(t.TempDir(), "session.json"))
			if err != nil {
				t.Fatal(err)
			}
			a.client.Transport = refreshTransport(func(req *http.Request) (*http.Response, error) {
				var payload any
				switch req.URL.Path {
				case "/weapi/v3/playlist/detail":
					songs := []any{
						map[string]any{"id": 1, "name": "one"},
						map[string]any{"id": 2, "name": "two"},
					}
					if complete {
						songs = append(songs, map[string]any{"id": 3, "name": "three"})
					}
					payload = map[string]any{"code": 200, "playlist": map[string]any{
						"trackCount": 4, "tracks": songs,
						"trackIds": []any{map[string]int{"id": 2}, map[string]int{"id": 1}, map[string]int{"id": 2}, map[string]int{"id": 3}},
					}}
				case "/weapi/v3/song/detail":
					if complete {
						// The available catalog should still work if extra detail
						// requests are unavailable or rate limited.
						return nil, fmt.Errorf("unnecessary detail lookup")
					}
					payload = map[string]any{"code": 200, "songs": []any{map[string]any{"id": 3, "name": "three"}}}
				default:
					return nil, fmt.Errorf("unexpected catalog operation")
				}
				body, _ := json.Marshal(payload)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
			})
			hits, err := a.PlaylistSongs(context.Background(), "123")
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) != 4 {
				t.Fatalf("truncated playlist: %v", hits)
			}
			for i, want := range []string{"two", "one", "two", "three"} {
				if hits[i].Title != want {
					t.Fatalf("song %d = %+v, want %s", i, hits[i], want)
				}
			}
		})
	}
}

func TestPlaylistSongsResolvesAllTrackIDsInOrder(t *testing.T) {
	a, err := New(filepath.Join(t.TempDir(), "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.client.Transport = refreshTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		var payload any
		switch calls {
		case 1:
			ids := make([]map[string]int, 501)
			for i := range ids {
				ids[i] = map[string]int{"id": i + 1}
			}
			// Real full-playlist metadata can exceed the account/search limit.
			payload = map[string]any{"code": 200, "playlist": map[string]any{
				"trackCount": 501, "trackIds": ids, "tracks": []any{},
				"description": strings.Repeat("x", maximumBodyBytes),
			}}
		case 2:
			songs := make([]map[string]any, 500)
			for i := range songs {
				songs[i] = map[string]any{"id": 500 - i, "name": fmt.Sprint(500 - i)}
			}
			payload = map[string]any{"code": 200, "songs": songs}
		case 3:
			payload = map[string]any{"code": 200, "songs": []any{map[string]any{"id": 501, "name": "last"}}}
		default:
			t.Fatal("unexpected extra detail request")
		}
		body, _ := json.Marshal(payload)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})
	hits, err := a.PlaylistSongs(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 501 {
		t.Fatalf("got %d songs, want 501", len(hits))
	}
	for i, hit := range hits {
		if hit.Ref.TrackID != fmt.Sprint(i+1) {
			t.Fatalf("song %d = %s", i, hit.Ref.TrackID)
		}
	}
}

func TestPlaylistRejectsIncompleteMetadata(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"playlist":{"trackCount":1,"trackIds":[]}}`,
		`{"playlist":{"trackCount":0}}`, `{"playlist":{"trackCount":1,"trackIds":[{"id":"bad"}]}}`,
	} {
		if _, _, err := parsePlaylist([]byte(body)); err == nil {
			t.Fatalf("accepted incomplete playlist %s", body)
		}
	}
	if _, err := parsePlaylistDetails([]byte(`{"songs":[{"id":1,"name":"available"}]}`), []string{"1", "2"}); err == nil {
		t.Fatal("missing song was silently omitted")
	}
	hits, err := parsePlaylistDetails([]byte(`{"songs":[{"id":1,"name":"one"},{"id":2,"name":"two"}]}`), []string{"2", "1", "2"})
	if err != nil || len(hits) != 3 || hits[0].Ref.TrackID != "2" || hits[1].Ref.TrackID != "1" || hits[2].Ref.TrackID != "2" {
		t.Fatalf("playlist order/duplicates lost: %v, %v", hits, err)
	}
}
