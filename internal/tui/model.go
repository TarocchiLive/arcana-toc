package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const tuiPollInterval = 2 * time.Second

type tuiPage uint8

const (
	tuiPageNowPlaying tuiPage = iota
	tuiPageSearch
	tuiPageQueue
	tuiPageAccount
)

type tuiSearchMode uint8

const (
	tuiSearchSongs tuiSearchMode = iota
	tuiSearchArtists
	tuiSearchPlaylists
)

type playbackState struct {
	status          tuiStatus
	mutationPending bool // A single playback/queue mutation may run at a time.
}
type searchState struct {
	platform         tuiPlatform
	results          []tuiSearchHit
	artists          []tuiArtist
	playlists        []tuiPlaylist
	searchMode       tuiSearchMode
	artistSongs      bool
	artistName       string
	query            string
	searchQuery      string
	searchPage       int
	searchTarget     int
	searchMore       map[string]bool
	searchFailures   map[string]string
	input            bool
	cursor           int
	searchSelection  int
	artistSelection  int
	searchPending    bool
	searchErr        error
	searchWarning    string
	searchGeneration uint64
	searchCancel     context.CancelFunc
}
type queueState struct {
	queue            []tuiQueueEntry
	history          []tuiHistoryEntry
	historyView      bool
	queueSelection   int
	historySelection int
}
type accountState struct {
	accounts          map[tuiPlatform]tuiAccount
	accountErrors     map[tuiPlatform]error
	accountGeneration uint64
	accountPending    bool
	accountCancel     context.CancelFunc
	loginPlatform     tuiPlatform
	loginChannel      string
	loginKey          string
	loginQR           string
	loginStatus       string
	accountWarning    string
	accountWriting    bool
	loginGeneration   uint64
	loginCancel       context.CancelFunc
	loginContext      context.Context
	loginPending      bool
}
type syncState struct {
	pending    bool
	dirty      bool
	generation uint64
	err        error
}

type pageFeedback struct {
	notice string
	err    error
}
type tuiModel struct {
	client        *tuiClient
	page          tuiPage
	width, height int
	playbackState
	searchState
	queueState
	accountState
	sync       [3]syncState
	feedback   [4]pageFeedback
	fullHelp   bool
	helpOffset int
}

type tuiSnapshotMsg struct {
	kind       int
	generation uint64
	status     tuiStatus
	queue      []tuiQueueEntry
	history    []tuiHistoryEntry
	err        error
}
type tuiAccountMsg struct {
	generation uint64
	accounts   map[tuiPlatform]tuiAccount
	errors     map[tuiPlatform]error
}
type tuiAccountRefreshResult struct {
	account      tuiAccount
	accountKnown bool
	skipped      bool
	refreshed    bool
	refreshErr   error
	queryErr     error
}

type tuiAccountRefreshMsg struct {
	generation uint64
	results    map[tuiPlatform]tuiAccountRefreshResult
}

type tuiSearchMsg struct {
	generation  uint64
	platform    tuiPlatform
	query       string
	page        int
	hasMore     map[string]bool
	results     []tuiSearchHit
	artists     []tuiArtist
	playlists   []tuiPlaylist
	errors      map[string]string
	mode        tuiSearchMode
	artistSongs bool
	artistName  string
	err         error
}

type tuiActionMsg struct {
	page   tuiPage
	notice string
	err    error
}

type tuiLoginMsg struct {
	platform   tuiPlatform
	channel    string
	key        string
	qr         string
	generation uint64
	err        error
}

type tuiLoginPollMsg struct {
	platform   tuiPlatform
	channel    string
	key        string
	generation uint64
	code       int
	err        error
}

type tuiLoginPollTickMsg struct {
	platform   tuiPlatform
	channel    string
	key        string
	generation uint64
}

func (m tuiModel) scheduleLoginPoll(platform tuiPlatform, channel, key string, generation uint64) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return tuiLoginPollTickMsg{platform: platform, channel: channel, key: key, generation: generation}
	})
}

func (m tuiModel) searchModeLabel() string {
	if m.artistSongs {
		return "songs by " + m.artistName
	}
	if m.searchMode == tuiSearchArtists {
		return "artists"
	}
	if m.searchMode == tuiSearchPlaylists {
		return "playlists"
	}
	return "songs"
}

func (m tuiModel) playbackMode() string {
	if m.status.Playback.Mode == "random" {
		return "random"
	}
	return "sequential"
}

type tuiTickMsg time.Time

func newTUIModel(client *tuiClient) tuiModel {
	return tuiModel{
		client:      client,
		searchState: searchState{platform: tuiPlatformAll},
		accountState: accountState{
			accounts:      make(map[tuiPlatform]tuiAccount),
			accountErrors: make(map[tuiPlatform]error),
		},
		sync: [3]syncState{{dirty: true}, {dirty: true}, {dirty: true}},
	}
}

func (m tuiModel) Init() tea.Cmd { return tuiTickNow() }
func tuiTickNow() tea.Cmd        { return func() tea.Msg { return tuiTickMsg(time.Now()) } }

func tuiTick() tea.Cmd {
	return tea.Tick(tuiPollInterval, func(t time.Time) tea.Msg { return tuiTickMsg(t) })
}
