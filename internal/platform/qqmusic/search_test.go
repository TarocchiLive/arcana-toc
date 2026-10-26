package qqmusic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/LingLambda/arcana-toc/internal/platform"
)

func searchSongFixture(mid string) map[string]any {
	item := playlistSongFixture(mid)
	for _, key := range strings.Fields("search_title title_main title_extra fav_show desc desc_icon hotness_desc content") {
		item[key] = ""
	}
	item["newStatus"], item["protect"] = 0, 0
	item["hotness"], item["relatedword_group"], item["vec_hotness"] = map[string]any{}, map[string]any{}, []any{}
	return item
}

func TestSearchPagesUseUpstreamContinuation(t *testing.T) {
	for _, kind := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Req struct {
						Param struct {
							Page  int `json:"page_num"`
							Limit int `json:"num_per_page"`
							Kind  int `json:"search_type"`
						} `json:"param"`
					} `json:"req_0"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				page := request.Req.Param.Page
				if request.Req.Param.Limit != 20 || request.Req.Param.Kind != kind || page < 1 || page > 3 {
					t.Errorf("request: %+v", request)
					w.WriteHeader(400)
					return
				}
				body := map[string]any{}
				for _, key := range []string{"item_song", "singer", "item_album", "item_songlist", "item_user", "item_audio", "item_mv"} {
					body[key] = []any{}
				}
				items := []any{}
				if page < 3 {
					for _, suffix := range []string{"B", "A"} {
						id := fmt.Sprintf("page%d%s", page, suffix)
						switch kind {
						case 0:
							items = append(items, searchSongFixture(id))
						case 1:
							items = append(items, map[string]any{"id": 1, "mid": id, "name": id, "singerPic": "", "subtitle": "", "songNum": 0, "albumNum": 0, "mvNum": 0})
						case 3:
							items = append(items, map[string]any{"id": page*10 + len(items) + 1, "title": id, "nickname": "", "dirid": 0, "picurl": "", "desc": "", "songnum": 0, "listennum": 0})
						}
					}
				}
				field := "item_song"
				if kind == 1 {
					field = "singer"
				}
				if kind == 3 {
					field = "item_songlist"
				}
				body[field] = items
				next := -1
				if page == 1 {
					next = 2
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "req_0": map[string]any{"code": 0, "data": map[string]any{"meta": map[string]any{"searchid": "test", "perpage": 20, "nextpage": next, "estimate_sum": 100, "sum": 100}, "body": body}}})
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			a := NewForTest(server.Client(), endpoint, Credential{})
			for page := 1; page <= 3; page++ {
				options := platform.SearchOptions{Page: page}
				var names []string
				var more bool
				var err error
				switch kind {
				case 0:
					result, searchErr := a.Search(context.Background(), "query", options)
					err, more = searchErr, result.HasMore
					for _, item := range result.Items {
						names = append(names, item.Title)
					}
				case 1:
					result, searchErr := a.SearchArtists(context.Background(), "query", options)
					err, more = searchErr, result.HasMore
					for _, item := range result.Items {
						names = append(names, item.Name)
					}
				case 3:
					result, searchErr := a.SearchPlaylists(context.Background(), "query", options)
					err, more = searchErr, result.HasMore
					for _, item := range result.Items {
						names = append(names, item.Name)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if more != (page == 1) {
					t.Fatalf("page %d hasMore=%v", page, more)
				}
				if page == 3 {
					if len(names) != 0 {
						t.Fatalf("exhausted: %v", names)
					}
					continue
				}
				if len(names) != 2 || names[0] != fmt.Sprintf("page%dB", page) || names[1] != fmt.Sprintf("page%dA", page) {
					t.Fatalf("page %d order: %v", page, names)
				}
			}
		})
	}
}

func TestSearchContinuationUsesNormalizedProviderIntegers(t *testing.T) {
	options := platform.SearchOptions{Page: 2, Limit: 20}
	for _, test := range []struct {
		next, total any
		want        bool
	}{
		{"3", "100", true},
		{"-1", "100", false},
		{"0", "100", true},
		{json.Number("0"), json.Number("22"), false},
	} {
		data := map[string]any{"meta": map[string]any{"nextpage": test.next, "sum": test.total}}
		if got := searchHasMore(data, options, 2); got != test.want {
			t.Fatalf("next=%v total=%v: has_more=%v, want %v", test.next, test.total, got, test.want)
		}
	}
}
