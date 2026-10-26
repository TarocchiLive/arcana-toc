package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

func updateModel(m tuiModel, msg tea.Msg) tuiModel { next, _ := m.Update(msg); return next.(tuiModel) }
func pressModel(m tuiModel, key string) tuiModel {
	return updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}

func TestSearchReplacementRejectsObsoleteReply(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page = tuiPageSearch
	m.search("first")
	first := m.searchGeneration
	m.search("second")
	m = updateModel(m, tuiSearchMsg{generation: m.searchGeneration, platform: m.platform, results: []tuiSearchHit{{Title: "second"}}})
	m = updateModel(m, tuiSearchMsg{generation: first, platform: m.platform, results: []tuiSearchHit{{Title: "first"}}})
	if len(m.results) != 1 || m.results[0].Title != "second" {
		t.Fatalf("obsolete search replaced current results: %+v", m.results)
	}
	m.cancelSearch()
}

func TestPlaylistScopeSwitchRejectsOldResults(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page = tuiPageSearch
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyTab})
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyTab})
	m.search("first")
	oldGeneration := m.searchGeneration
	m = pressModel(m, "P")
	if m.searchMode != tuiSearchPlaylists || m.platform != tuiPlatformNetEase {
		t.Fatal("platform switch left playlist search mode")
	}
	m.search("second")
	m = updateModel(m, tuiSearchMsg{
		generation: m.searchGeneration, platform: m.platform, mode: tuiSearchPlaylists,
		playlists: []tuiPlaylist{{Platform: tuiPlatformNetEase, ID: "new", Name: "new"}},
	})
	m = updateModel(m, tuiSearchMsg{
		generation: oldGeneration, platform: tuiPlatformAll, mode: tuiSearchPlaylists,
		playlists: []tuiPlaylist{{ID: "old"}},
	})
	if m.itemCount() != 1 || m.playlists[0].ID != "new" {
		t.Fatal("old scope response replaced current playlists")
	}
	m.cancelSearch()
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.searchMode != tuiSearchSongs || m.itemCount() != 0 {
		t.Fatal("playlist mode did not cycle back to a clean song search")
	}
}

func TestPlaylistEnterReplacesInsteadOfEnqueueing(t *testing.T) {
	played := make(chan tuiPlaylist, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/playlists/search":
			if r.Method != http.MethodGet || r.URL.Query().Get("q") != "夜 & 光" || r.URL.Query().Get("platform") != "all" {
				http.Error(w, "invalid playlist search", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"playlists":[{"platform":"qqmusic","id":"42","name":"夜曲"}],"errors":{"netease":"offline"}}`))
		case "/api/playlists/play":
			var playlist tuiPlaylist
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&playlist) != nil {
				http.Error(w, "invalid playlist action", http.StatusBadRequest)
				return
			}
			played <- playlist
			_, _ = w.Write([]byte(`{"state":"playing"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := newTUIModel(&tuiClient{baseURL: server.URL, http: server.Client()})
	m.page, m.searchMode = tuiPageSearch, tuiSearchPlaylists
	cmd := m.search("夜 & 光")
	m = updateModel(m, cmd())
	if m.itemCount() != 1 || !strings.Contains(m.searchWarning, "offline") {
		t.Fatalf("partial playlist results were lost: %+v, %q", m.playlists, m.searchWarning)
	}
	if _, cmd := m.handleSearchKey("a"); cmd != nil {
		t.Fatal("song enqueue shortcut activated a playlist")
	}
	next, cmd := m.handleSearchKey("enter")
	m = next.(tuiModel)
	if cmd == nil {
		t.Fatal("Enter did not start playlist playback")
	}
	if _, duplicate := m.handleSearchKey("enter"); duplicate != nil {
		t.Fatal("playlist action allowed a concurrent queue mutation")
	}
	action := cmd().(tuiActionMsg)
	if action.err != nil {
		t.Fatal(action.err)
	}
	select {
	case playlist := <-played:
		if playlist.Platform != tuiPlatformQQMusic || playlist.ID != "42" {
			t.Fatalf("wrong selected playlist played: %+v", playlist)
		}
	default:
		t.Fatal("playlist playback endpoint was not called")
	}
	generation := m.sync[syncStatus].generation
	m = updateModel(m, action)
	if m.mutationPending || m.sync[syncStatus].generation == generation || !m.sync[syncQueue].dirty {
		t.Fatal("playlist replacement did not invalidate old playback and queue snapshots")
	}
}

func TestAccountQueryCannotOverrideNewerRefresh(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.loadAccounts()
	old := m.accountGeneration
	m.refreshAccounts()
	m = updateModel(m, tuiAccountRefreshMsg{generation: m.accountGeneration, results: map[tuiPlatform]tuiAccountRefreshResult{tuiPlatformQQMusic: {account: tuiAccount{Name: "connected", LoggedIn: true}, accountKnown: true, refreshed: true}}})
	m = updateModel(m, tuiAccountMsg{generation: old, accounts: map[tuiPlatform]tuiAccount{tuiPlatformQQMusic: {Name: "obsolete"}}})
	if m.accounts[tuiPlatformQQMusic].Name != "connected" {
		t.Fatal("old query replaced refreshed account")
	}
	m.accountCancel()
}

func TestSyncDoesNotEraseActionFailure(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.width, m.height = 100, 20
	m = updateModel(m, tuiActionMsg{err: errors.New("queue write refused")})
	m = updateModel(m, tuiSnapshotMsg{kind: syncStatus, generation: m.sync[syncStatus].generation})
	if !strings.Contains(m.View(), "queue write refused") {
		t.Fatal("successful background read erased action failure")
	}
	m = updateModel(m, tuiSnapshotMsg{kind: syncQueue, generation: m.sync[syncQueue].generation, err: errors.New("queue unavailable")})
	m = updateModel(m, tuiSnapshotMsg{kind: syncStatus, generation: m.sync[syncStatus].generation})
	if m.sync[syncQueue].err == nil || !strings.Contains(m.View(), "queue write refused") {
		t.Fatal("endpoint errors did not retain independent ownership")
	}
}

func TestPendingSearchAllowsNavigationAndPlayback(t *testing.T) {
	playedNext := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/playback/next" {
			http.NotFound(w, r)
			return
		}
		playedNext <- struct{}{}
	}))
	defer server.Close()
	m := newTUIModel(&tuiClient{baseURL: server.URL, http: server.Client()})
	m = updateModel(m, tuiSnapshotMsg{kind: syncStatus, status: tuiStatus{Playback: tuiPlayback{State: "playing"}}})
	m.page = tuiPageSearch
	m.searchPending = true
	m = pressModel(m, "1")
	if m.page != tuiPageNowPlaying {
		t.Fatal("pending search blocked page switch")
	}
	_, cmd := m.handlePlaybackKey("n")
	if cmd == nil {
		t.Fatal("pending search blocked playback")
	}
	if result := cmd().(tuiActionMsg); result.err != nil {
		t.Fatal(result.err)
	}
	select {
	case <-playedNext:
	default:
		t.Fatal("next track was not started during pending search")
	}
}

func TestControlCInterruptsUnicodeInput(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page = tuiPageSearch
	m.input = true
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("你好吗"), Paste: true})
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyLeft})
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.query != "你吗" {
		t.Fatalf("rune deletion produced %q", m.query)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl-C swallowed by input")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl-C did not quit")
	}
}

func TestQueueSelectionFollowsIdentityAndHistoryIsIndependent(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page = tuiPageQueue
	m.queue = []tuiQueueEntry{{ID: 1}, {ID: 2}, {ID: 3}}
	m.queueSelection = 1
	m = updateModel(m, tuiSnapshotMsg{kind: syncQueue, queue: []tuiQueueEntry{{ID: 3}, {ID: 1}, {ID: 2}}})
	if m.queue[m.queueSelection].ID != 2 {
		t.Fatal("selection moved to a different track after reorder")
	}
	m.historySelection = 4
	m = pressModel(m, "h")
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.queueSelection != 2 || m.historySelection != 4 {
		t.Fatal("queue/history selections leaked into each other")
	}
	m = updateModel(m, tuiSnapshotMsg{kind: syncQueue, queue: []tuiQueueEntry{{ID: 1}}})
	if m.queueSelection != 0 {
		t.Fatal("removed selection was not clamped")
	}
}

func TestSnapshotBeforeWriteCompletionCannotRestoreDeletedTrack(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page = tuiPageQueue
	m.queue = []tuiQueueEntry{{ID: 1}}
	m.mutationPending = true
	m = updateModel(m, tuiSnapshotMsg{kind: syncQueue, queue: []tuiQueueEntry{{ID: 1}, {ID: 2}}})
	if len(m.queue) != 1 {
		t.Fatal("in-flight mutation accepted an old read")
	}
	m.mutationPending = false
	m.invalidate()
	m = updateModel(m, tuiSnapshotMsg{kind: syncQueue, generation: m.sync[syncQueue].generation, queue: []tuiQueueEntry{}})
	m = updateModel(m, tuiSnapshotMsg{kind: syncQueue, generation: 0, queue: []tuiQueueEntry{{ID: 1}, {ID: 2}}})
	if len(m.queue) != 0 {
		t.Fatal("obsolete snapshot restored deleted entries")
	}
}

func TestViewsRespectTerminalBoundsAndNeverCropQR(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.loginQR = "████████\n██    ██\n████████"
	m.results = []tuiSearchHit{{Title: "bad\x1b[2Jtitle\n你好"}}
	for _, size := range [][2]int{{1, 1}, {8, 6}, {32, 12}, {80, 30}} {
		m.width, m.height = size[0], size[1]
		for page := tuiPageNowPlaying; page <= tuiPageAccount; page++ {
			m.page = page
			output := m.View()
			lines := strings.Split(output, "\n")
			if len(lines) > m.height {
				t.Fatalf("page %d exceeded height %d", page, m.height)
			}
			for _, line := range lines {
				if runewidth.StringWidth(line) > m.width {
					t.Fatalf("page %d exceeded width %d: %q", page, m.width, line)
				}
			}
			if strings.Contains(output, "\x1b") {
				t.Fatal("remote ANSI escaped sanitization")
			}
		}
	}
	m.page = tuiPageAccount
	m.width, m.height = 80, 30
	if !strings.Contains(m.View(), m.loginQR) {
		t.Fatal("QR omitted despite sufficient room")
	}
	m.height = 9
	if strings.Contains(m.View(), "████████") {
		t.Fatal("small terminal received a cropped QR code")
	}
}

func TestFailedStatusDoesNotSuppressQueueOrHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case "/api/queue":
			_, _ = fmt.Fprint(w, `{"entries":[{"id":7,"title":"queued"}]}`)
		case "/api/history":
			_, _ = fmt.Fprint(w, `{"entries":[{"id":9,"title":"played"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := newTUIModel(&tuiClient{baseURL: server.URL, http: server.Client()})
	for _, kind := range []int{syncStatus, syncQueue, syncHistory} {
		m = updateModel(m, m.readSnapshot(kind, 0)())
	}
	if len(m.queue) != 1 || m.queue[0].Title != "queued" || len(m.history) != 1 || m.history[0].Title != "played" {
		t.Fatal("one endpoint failure suppressed healthy endpoint data")
	}
	if m.sync[syncStatus].err == nil {
		t.Fatal("failed status endpoint was not reported")
	}
}

func TestQQKeyReplacesPendingAccountQueryWithoutQuitting(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page = tuiPageAccount
	m.loadAccounts()
	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m = next.(tuiModel)
	if cmd == nil || m.loginPlatform != tuiPlatformQQMusic || m.loginChannel != "qq" || !m.loginPending {
		t.Fatal("q lost its account-login meaning while account query was pending")
	}
	m.loginCancel()
}

func TestObsoleteLoginReplyCannotReplaceCurrentQR(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page = tuiPageAccount
	next, _ := m.handleAccountKey("l")
	m = next.(tuiModel)
	old := m.loginGeneration
	next, _ = m.handleAccountKey("w")
	m = next.(tuiModel)
	m = updateModel(m, tuiLoginMsg{platform: tuiPlatformQQMusic, channel: "wx", generation: m.loginGeneration, key: "current", qr: "current QR"})
	m = updateModel(m, tuiLoginMsg{platform: tuiPlatformNetEase, channel: "netease", generation: old, key: "old", qr: "obsolete QR"})
	if m.loginQR != "current QR" || m.loginChannel != "wx" {
		t.Fatal("obsolete login replaced current channel QR")
	}
	m.loginCancel()
}

func TestBatchedKeystrokesEnterUnicodeSearch(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m = pressModel(m, "2/你好")
	if m.page != tuiPageSearch || !m.input || m.query != "你好" {
		t.Fatalf("coalesced terminal input was lost: page=%d input=%v query=%q", m.page, m.input, m.query)
	}
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q4"), Paste: true})
	if m.page != tuiPageSearch {
		t.Fatal("pasted text outside input executed navigation shortcuts")
	}
}

func TestPageFailuresStayWithTheirOperation(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.width, m.height = 100, 24
	m.page = tuiPageSearch
	m = updateModel(m, tuiActionMsg{page: tuiPageNowPlaying, err: errors.New("playback refused")})
	if strings.Contains(m.View(), "playback refused") {
		t.Fatal("background playback failure leaked onto search page")
	}
	m = pressModel(m, "1")
	if !strings.Contains(m.View(), "playback refused") {
		t.Fatal("playback failure was lost after page navigation")
	}
	m = updateModel(m, tuiSnapshotMsg{kind: syncHistory, generation: m.sync[syncHistory].generation, err: errors.New("history unavailable")})
	m = pressModel(m, "2")
	if strings.Contains(m.View(), "history unavailable") {
		t.Fatal("hidden history endpoint failure leaked onto search page")
	}
	m.searchErr = errors.New("previous query failed")
	m = pressModel(m, "P")
	if strings.Contains(m.View(), "previous query failed") {
		t.Fatal("new search scope retained obsolete search failure")
	}
}

func TestHistoryCannotDeleteActiveQueue(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page = tuiPageQueue
	m = updateModel(m, tuiSnapshotMsg{kind: syncQueue, queue: []tuiQueueEntry{{ID: 7, Title: "active"}}})
	m = pressModel(m, "h")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if cmd != nil || len(next.(tuiModel).queue) != 1 {
		t.Fatal("read-only history allowed deletion of an active queue entry")
	}
}

func TestPlaybackWaitsForPostMutationSnapshot(t *testing.T) {
	volumes := make(chan int, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/playback/volume" {
			http.NotFound(w, r)
			return
		}
		var request struct{ Value int }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		volumes <- request.Value
	}))
	defer server.Close()
	m := newTUIModel(&tuiClient{baseURL: server.URL, http: server.Client()})
	adjust := func() {
		next, cmd := m.handlePlaybackKey("-")
		m = next.(tuiModel)
		if cmd != nil {
			result := cmd().(tuiActionMsg)
			if result.err != nil {
				t.Fatal(result.err)
			}
			m = updateModel(m, result)
		}
	}
	expectVolume := func(want int) {
		t.Helper()
		select {
		case got := <-volumes:
			if got != want {
				t.Fatalf("volume=%d, want %d", got, want)
			}
		default:
			t.Fatal("volume adjustment did not reach the service")
		}
	}
	m = updateModel(m, tuiSnapshotMsg{kind: syncStatus, status: tuiStatus{Playback: tuiPlayback{State: "playing", Volume: 50}}})
	adjust()
	expectVolume(45)
	adjust()
	select {
	case volume := <-volumes:
		t.Fatalf("stale snapshot caused an extra volume write: %d", volume)
	default:
	}
	m = updateModel(m, tuiSnapshotMsg{kind: syncStatus, generation: m.sync[syncStatus].generation, status: tuiStatus{Playback: tuiPlayback{State: "playing", Volume: 45}}})
	adjust()
	expectVolume(40)
}

func TestHelpScrollCanReverseAtEnd(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.width, m.height = 40, 10
	m = pressModel(m, "?")
	for range 100 {
		m = updateModel(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	end := m.View()
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.View() == end {
		t.Fatal("scrolling past the end trapped the help viewport")
	}
}

func TestArtistSongsCanReturnToSelectedArtist(t *testing.T) {
	m := newTUIModel(&tuiClient{})
	m.page, m.searchMode = tuiPageSearch, tuiSearchArtists
	m.artists = []tuiArtist{{Name: "first"}, {Name: "selected"}}
	m.searchSelection = 1
	next, _ := m.handleSearchKey("enter")
	m = next.(tuiModel)
	m = updateModel(m, tuiSearchMsg{generation: m.searchGeneration, platform: m.platform, mode: tuiSearchArtists, artistSongs: true, artistName: "selected", results: []tuiSearchHit{{Title: "song"}}})
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.artistSongs || len(m.artists) != 2 || m.artists[m.searchSelection].Name != "selected" {
		t.Fatal("artist detail navigation discarded the original artist list or selection")
	}
	m.cancelSearch()
}
