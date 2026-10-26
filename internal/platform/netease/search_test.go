package netease

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/LingLambda/arcana-toc/internal/platform"
)

func TestSearchPagesPreserveOrderAndContinueShortPages(t *testing.T) {
	original := rand.Reader
	rand.Reader = bytes.NewReader(make([]byte, 16384))
	defer func() { rand.Reader = original }()
	for _, kind := range []string{"1", "100", "1000"} {
		t.Run(kind, func(t *testing.T) {
			a, err := New(filepath.Join(t.TempDir(), "session.json"))
			if err != nil {
				t.Fatal(err)
			}
			a.client.Transport = refreshTransport(func(req *http.Request) (*http.Response, error) {
				payload := decryptWeapiPayload(t, req)
				if payload["type"] != kind || payload["limit"] != "20" {
					t.Fatalf("request: %v", payload)
				}
				var offset int
				if _, err := fmt.Sscan(fmt.Sprint(payload["offset"]), &offset); err != nil {
					t.Fatal(err)
				}
				items := []map[string]any{}
				if offset < 40 {
					items = append(items, map[string]any{"id": offset + 2, "name": "second"}, map[string]any{"id": offset + 1, "name": "first"})
				}
				field, total := "songs", "songCount"
				if kind == "100" {
					field, total = "artists", "artistCount"
				}
				if kind == "1000" {
					field, total = "playlists", "playlistCount"
				}
				body, _ := json.Marshal(map[string]any{"code": 200, "result": map[string]any{field: items, total: 22}})
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			for page := 1; page <= 3; page++ {
				options := platform.SearchOptions{Page: page, Limit: 50}
				var ids []string
				var more bool
				switch kind {
				case "1":
					result, searchErr := a.Search(context.Background(), "query", options)
					err, more = searchErr, result.HasMore
					for _, item := range result.Items {
						ids = append(ids, item.Ref.TrackID)
					}
				case "100":
					result, searchErr := a.SearchArtists(context.Background(), "query", options)
					err, more = searchErr, result.HasMore
					for _, item := range result.Items {
						ids = append(ids, item.ID)
					}
				case "1000":
					result, searchErr := a.SearchPlaylists(context.Background(), "query", options)
					err, more = searchErr, result.HasMore
					for _, item := range result.Items {
						ids = append(ids, item.ID)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if more != (page == 1) {
					t.Fatalf("page %d hasMore=%v", page, more)
				}
				if page == 3 {
					if len(ids) != 0 {
						t.Fatalf("exhausted: %v", ids)
					}
					continue
				}
				if len(ids) != 2 || ids[0] != fmt.Sprint((page-1)*20+2) || ids[1] != fmt.Sprint((page-1)*20+1) {
					t.Fatalf("page %d order: %v", page, ids)
				}
			}
		})
	}
}

func TestSearchWithoutTotalsContinuesUntilEmpty(t *testing.T) {
	options := platform.SearchOptions{Page: 2, Limit: 20}
	for _, field := range []string{"songCount", "artistCount", "playlistCount"} {
		if !searchHasMore([]byte(`{"result":{}}`), field, options, 10) {
			t.Fatal("short uncounted page must continue")
		}
		if searchHasMore([]byte(`{"result":{}}`), field, options, 0) {
			t.Fatal("empty page must stop")
		}
		if searchHasMore([]byte(`{"result":{"hasMore":false}}`), field, options, 10) {
			t.Fatal("explicit exhaustion must stop")
		}
	}
}
