package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LingLambda/arcana-toc/internal/config"
	"github.com/LingLambda/arcana-toc/internal/platform"
)

type pagedCatalog struct {
	platform.Adapter
	source platform.Platform
}

func (a pagedCatalog) Search(_ context.Context, _ string, options platform.SearchOptions) (platform.SearchPage[platform.SearchHit], error) {
	if a.source == platform.QQMusic && options.Page == 2 {
		return platform.SearchPage[platform.SearchHit]{}, errors.New("catalog unavailable")
	}
	return platform.SearchPage[platform.SearchHit]{Items: []platform.SearchHit{{Ref: platform.TrackRef{Platform: a.source, TrackID: fmt.Sprint(options.Page)}, Title: fmt.Sprintf("page %d", options.Page)}}, HasMore: options.Page == 1}, nil
}
func (a pagedCatalog) SearchArtists(ctx context.Context, query string, options platform.SearchOptions) (platform.SearchPage[platform.Artist], error) {
	songs, err := a.Search(ctx, query, options)
	if err != nil {
		return platform.SearchPage[platform.Artist]{}, err
	}
	return platform.SearchPage[platform.Artist]{Items: []platform.Artist{{Platform: a.source, ID: songs.Items[0].Ref.TrackID, Name: songs.Items[0].Title}}, HasMore: songs.HasMore}, nil
}
func (a pagedCatalog) ArtistSongs(context.Context, string) ([]platform.SearchHit, error) {
	return nil, nil
}
func (a pagedCatalog) SearchPlaylists(ctx context.Context, query string, options platform.SearchOptions) (platform.SearchPage[platform.Playlist], error) {
	songs, err := a.Search(ctx, query, options)
	if err != nil {
		return platform.SearchPage[platform.Playlist]{}, err
	}
	return platform.SearchPage[platform.Playlist]{Items: []platform.Playlist{{Platform: a.source, ID: songs.Items[0].Ref.TrackID, Name: songs.Items[0].Title}}, HasMore: songs.HasMore}, nil
}
func (a pagedCatalog) PlaylistSongs(context.Context, string) ([]platform.SearchHit, error) {
	return nil, nil
}

func TestCatalogPagesKeepResultsAndContinuationPlatformSpecific(t *testing.T) {
	s := New(Deps{Config: config.Default(), Adapters: map[platform.Platform]platform.Adapter{
		platform.Netease: pagedCatalog{source: platform.Netease}, platform.QQMusic: pagedCatalog{source: platform.QQMusic},
	}})
	for _, kind := range []struct {
		path, key string
		get       bool
	}{{"/api/search", "hits", false}, {"/api/artists/search", "artists", false}, {"/api/playlists/search", "playlists", true}} {
		t.Run(kind.key, func(t *testing.T) {
			for _, page := range []int{1, 2} {
				req := httptest.NewRequest(http.MethodPost, kind.path, strings.NewReader(fmt.Sprintf(`{"keyword":"test","platform":"all","limit":20,"page":%d}`, page)))
				if kind.get {
					req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("%s?q=test&platform=all&limit=20&page=%d", kind.path, page), nil)
				}
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("page %d: %d %s", page, w.Code, w.Body.String())
				}
				var body struct {
					Page   int               `json:"page"`
					Count  int               `json:"count"`
					More   map[string]bool   `json:"has_more"`
					Errors map[string]string `json:"errors"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
					t.Fatal(err)
				}
				var rows []json.RawMessage
				if err := json.Unmarshal(raw[kind.key], &rows); err != nil {
					t.Fatal(err)
				}
				wantCount := 3 - page
				if body.Page != page || body.Count != wantCount || len(rows) != wantCount || body.More["netease"] != (page == 1) {
					t.Fatalf("page %d response: %s", page, w.Body.String())
				}
				if !strings.Contains(string(rows[0]), fmt.Sprintf("page %d", page)) {
					t.Fatalf("wrong page results: %s", rows[0])
				}
				if page == 2 {
					if body.Errors["qqmusic"] == "" {
						t.Fatal("lost partial platform error")
					}
					if _, ok := body.More["qqmusic"]; ok {
						t.Fatal("failed platform advertised continuation")
					}
				}
			}
		})
	}
}

func TestCatalogRejectsOverflowingPages(t *testing.T) {
	s := New(Deps{Config: config.Default(), Adapters: map[platform.Platform]platform.Adapter{
		platform.Netease: pagedCatalog{source: platform.Netease},
	}})
	lastPage := int(^uint(0)>>1) / 20
	for _, path := range []string{"/api/search", "/api/artists/search", "/api/playlists/search"} {
		t.Run(path, func(t *testing.T) {
			for _, page := range []int{lastPage, lastPage + 1} {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(fmt.Sprintf(`{"keyword":"test","limit":100,"page":%d}`, page)))
				if path == "/api/playlists/search" {
					req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("%s?q=test&limit=100&page=%d", path, page), nil)
				}
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, req)
				want := http.StatusOK
				if page > lastPage {
					want = http.StatusBadRequest
				}
				if w.Code != want {
					t.Fatalf("page %d: got %d, want %d: %s", page, w.Code, want, w.Body.String())
				}
			}
		})
	}
}
