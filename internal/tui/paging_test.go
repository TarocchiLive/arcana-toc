package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSearchPagesRefetchAndRetryWithoutSkipping(t *testing.T) {
	for _, mode := range []tuiSearchMode{tuiSearchSongs, tuiSearchArtists, tuiSearchPlaylists} {
		t.Run(strconv.Itoa(int(mode)), func(t *testing.T) {
			var pages []int
			fail := true
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Page, Limit int }
				if mode == tuiSearchPlaylists {
					request.Page, _ = strconv.Atoi(r.URL.Query().Get("page"))
					request.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
				} else if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Limit != 20 {
					t.Errorf("unbounded search: %d", request.Limit)
				}
				pages = append(pages, request.Page)
				if request.Page == 2 && fail {
					fail = false
					w.WriteHeader(http.StatusBadGateway)
					_, _ = fmt.Fprint(w, `{"errors":{"netease":"offline","qqmusic":"offline"}}`)
					return
				}
				label := fmt.Sprintf("page-%d-request-%d", request.Page, len(pages))
				response := tuiSearchPage{Page: request.Page, HasMore: map[string]bool{"netease": request.Page == 1}}
				switch mode {
				case tuiSearchSongs:
					response.Hits = []tuiSearchHit{{Title: label}}
				case tuiSearchArtists:
					response.Artists = []tuiArtist{{Name: label}}
				case tuiSearchPlaylists:
					response.Playlists = []tuiPlaylist{{Name: label}}
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			m := newTUIModel(&tuiClient{baseURL: server.URL, http: server.Client()})
			m.page, m.searchMode = tuiPageSearch, mode
			cmd := m.search("query")
			m = updateModel(m, cmd())
			cmd = m.turnSearchPage(1)
			m = updateModel(m, cmd())
			if m.searchPage != 1 || m.itemCount() != 1 || m.searchErr == nil {
				t.Fatal("failed next page discarded the visible page")
			}
			cmd = m.turnSearchPage(1)
			m = updateModel(m, cmd())
			if m.searchPage != 2 || m.searchErr != nil {
				t.Fatal("retry did not recover page two")
			}
			if m.turnSearchPage(1) != nil {
				t.Fatal("exhausted search requested another page")
			}
			cmd = m.turnSearchPage(-1)
			m = updateModel(m, cmd())
			if !reflect.DeepEqual(pages, []int{1, 2, 2, 1}) {
				t.Fatalf("requested pages %v", pages)
			}
			var label string
			switch mode {
			case tuiSearchSongs:
				label = m.results[0].Title
			case tuiSearchArtists:
				label = m.artists[0].Name
			case tuiSearchPlaylists:
				label = m.playlists[0].Name
			}
			if label != "page-1-request-4" {
				t.Fatal("previous page reused stale ranking")
			}
		})
	}
}

func TestPartialPageRetryPreservesHealthyPlatforms(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page = tuiPageSearch
	m.searchQuery, m.searchPage, m.searchTarget = "query", 2, 2
	hit := tuiSearchHit{Title: "healthy"}
	hit.Ref.Platform = "netease"
	m.results = []tuiSearchHit{hit}
	m.searchFailures = map[string]string{"qqmusic": "offline"}
	cmd := m.turnSearchPage(1)
	if cmd == nil || m.searchTarget != 2 {
		t.Fatal("partial page skipped the failed platform")
	}
	m = updateModel(m, tuiSearchMsg{generation: m.searchGeneration, platform: m.platform, page: 2,
		errors: map[string]string{"netease": "now offline"}, results: []tuiSearchHit{{Title: "recovered"}}})
	if len(m.results) != 2 || m.results[1].Title != "healthy" {
		t.Fatal("retry erased previously successful platform results")
	}
	m.cancelSearch()
}

func TestCancelledPagingCannotReplaceNewCatalog(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page = tuiPageSearch
	m.search("old")
	old := m.searchGeneration
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	m.search("new")
	m = updateModel(m, tuiSearchMsg{generation: old, platform: m.platform, page: 4, results: []tuiSearchHit{{Title: "obsolete"}}})
	if len(m.results) != 0 || !m.searchPending {
		t.Fatal("cancelled page replaced a newer query")
	}
	m.cancelSearch()
}

func TestListPagingResizeAndSnapshotClamping(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page, m.width, m.height = tuiPageQueue, 100, 12
	for i := range 13 {
		m.queue = append(m.queue, tuiQueueEntry{ID: int64(i + 1)})
	}
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.queueSelection != 5 {
		t.Fatalf("page step selected %d", m.queueSelection)
	}
	m = updateModel(m, tea.WindowSizeMsg{Width: 100, Height: 10})
	start, end := m.listRange(len(m.queue))
	if start != 3 || end != 6 {
		t.Fatalf("resize hid selection: %d-%d", start, end)
	}
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyEnd})
	m = pressModel(m, "]")
	if m.queueSelection != 12 {
		t.Fatal("paging past end escaped the list")
	}
	m = updateModel(m, tuiSnapshotMsg{kind: syncQueue, queue: []tuiQueueEntry{{ID: 1}}})
	if m.queueSelection != 0 {
		t.Fatal("deleted last page left selection out of bounds")
	}
	m.historyView = true
	m.history = []tuiHistoryEntry{{ID: 3}, {ID: 2}, {ID: 1}}
	m.historySelection = 1
	m = updateModel(m, tuiSnapshotMsg{kind: syncHistory, history: []tuiHistoryEntry{{ID: 4}, {ID: 3}, {ID: 2}, {ID: 1}}})
	if m.history[m.historySelection].ID != 2 {
		t.Fatal("history poll changed selected identity")
	}
	m = updateModel(m, tuiSnapshotMsg{kind: syncHistory})
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyPgUp})
	if m.historySelection != 0 || m.queueSelection != 0 {
		t.Fatal("empty history paging corrupted selection")
	}
}

func TestArtistDetailKeepsCatalogPagination(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page, m.searchMode = tuiPageSearch, tuiSearchArtists
	m.searchPage, m.searchTarget, m.searchQuery = 3, 3, "artist"
	m.searchMore = map[string]bool{"netease": true}
	m.artists = []tuiArtist{{Name: "first"}, {Name: "selected"}}
	m.searchSelection = 1
	m.searchArtist(m.artists[1])
	m = updateModel(m, tuiSearchMsg{generation: m.searchGeneration, platform: m.platform, mode: tuiSearchArtists,
		artistSongs: true, results: []tuiSearchHit{{Title: "song"}}})
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.searchPage != 3 || m.searchSelection != 1 || !m.searchMore["netease"] {
		t.Fatal("artist songs overwrote catalog pagination")
	}
	cmd := m.turnSearchPage(1)
	if cmd == nil || m.searchTarget != 4 {
		t.Fatal("artist back lost next catalog page")
	}
	m.cancelSearch()
}
