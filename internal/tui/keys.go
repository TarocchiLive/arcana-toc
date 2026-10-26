package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if key == "q" && !m.input && m.page != tuiPageAccount {
		return m, tea.Quit
	}
	if key == "esc" && !m.input && !m.fullHelp && m.page == tuiPageSearch && m.searchPending {
		m.cancelSearch()
		m.feedback[tuiPageSearch].notice = "Search cancelled"
		return m, nil
	}
	if key == "?" && !m.input {
		m.fullHelp = !m.fullHelp
		m.helpOffset = 0
		return m, nil
	}
	if m.fullHelp {
		switch key {
		case "esc":
			m.fullHelp = false
		case "up", "k":
			m.helpOffset = max(0, m.helpOffset-1)
		case "down", "j":
			m.helpOffset = min(m.helpOffset+1, m.maxHelpOffset())
		}
		return m, nil
	}
	if m.page == tuiPageSearch && key == "tab" && !m.input {
		m.cancelSearch()
		m.searchMode = (m.searchMode + 1) % (tuiSearchPlaylists + 1)
		m.results, m.artists, m.playlists = nil, nil, nil
		m.artistSongs, m.artistName = false, ""
		m.searchQuery, m.searchPage, m.searchTarget = "", 0, 0
		m.searchMore, m.searchFailures = nil, nil
		m.searchWarning = ""
		m.searchErr = nil
		m.feedback[tuiPageSearch] = pageFeedback{}
		*m.selected() = 0
		m.feedback[tuiPageSearch].notice = "Search mode: " + m.searchModeLabel()
		return m, nil
	}
	if m.input {
		switch key {
		case "esc":
			m.input = false
			m.cancelSearch()
			m.feedback[tuiPageSearch].notice = "Search cancelled"
			return m, nil
		case "enter":
			query := strings.TrimSpace(m.query)
			m.input = false
			if query == "" {
				m.searchErr = fmt.Errorf("search query must not be empty")
				return m, nil
			}
			cmd := m.search(query)
			return m, cmd
		case "left":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "right":
			if m.cursor < len([]rune(m.query)) {
				m.cursor++
			}
			return m, nil
		case "home", "ctrl+a":
			m.cursor = 0
			return m, nil
		case "end", "ctrl+e":
			m.cursor = len([]rune(m.query))
			return m, nil
		case "backspace", "ctrl+h":
			r := []rune(m.query)
			if m.cursor > 0 {
				r = append(r[:m.cursor-1], r[m.cursor:]...)
				m.cursor--
				m.query = string(r)
			}
			return m, nil
		case "delete":
			r := []rune(m.query)
			if m.cursor < len(r) {
				m.query = string(append(r[:m.cursor], r[m.cursor+1:]...))
			}
			return m, nil
		default:
			if len(msg.Runes) > 0 {
				r := []rune(m.query)
				added := []rune(safeText(string(msg.Runes)))
				m.query = string(r[:m.cursor]) + string(added) + string(r[m.cursor:])
				m.cursor += len(added)
			}
			return m, nil
		}

	}
	if m.page == tuiPageSearch || m.page == tuiPageQueue {
		switch key {
		case "pgup", "[", "pgdown", "]":
			direction := 1
			if key == "pgup" || key == "[" {
				direction = -1
			}
			if m.page == tuiPageSearch && !m.artistSongs {
				cmd := m.turnSearchPage(direction)
				return m, cmd
			}
			m.moveListPage(direction)
			return m, nil
		case "home":
			*m.selected() = 0
			return m, nil
		case "end":
			*m.selected() = max(0, m.itemCount()-1)
			return m, nil
		}
	}

	if m.page == tuiPageQueue && m.historyView {
		switch key {
		case "esc", "enter", "h":
			m.historyView = false
			m.feedback[tuiPageQueue].notice = ""
			cmd := m.refresh(false)
			return m, cmd
		case "up", "k":
			if *m.selected() > 0 {
				*m.selected()--
			}
			return m, nil
		case "down", "j":
			if *m.selected() < len(m.history)-1 {
				*m.selected()++
			}
			return m, nil
		}
	}
	switch key {
	case "1":
		m.page = tuiPageNowPlaying
		m.historyView = false
		return m, nil
	case "2":
		m.page = tuiPageSearch
		m.historyView = false
		return m, nil
	case "3":
		m.page = tuiPageQueue
		cmd := m.refresh(false)
		return m, cmd
	case "4":
		m.page = tuiPageAccount
		m.historyView = false
		cmd := m.loadAccounts()
		return m, cmd
	case "P":
		m.cancelSearch()
		switch m.platform {
		case tuiPlatformAll:
			m.platform = tuiPlatformNetEase
		case tuiPlatformNetEase:
			m.platform = tuiPlatformQQMusic
		default:
			m.platform = tuiPlatformAll
		}
		m.results, m.artists, m.playlists = nil, nil, nil
		if m.searchMode != tuiSearchPlaylists {
			m.searchMode = tuiSearchSongs
		}
		m.artistSongs, m.artistName = false, ""
		m.searchQuery, m.searchPage, m.searchTarget = "", 0, 0
		m.searchMore, m.searchFailures = nil, nil
		m.searchWarning = ""
		m.searchErr = nil
		m.feedback[tuiPageSearch] = pageFeedback{}
		m.searchSelection = 0
		m.feedback[tuiPageSearch].notice = "Search scope: " + m.platform.label()
		return m, nil
	case "r":
		if m.page == tuiPageSearch {
			if m.searchPending || m.artistSongs || m.searchQuery == "" {
				return m, nil
			}
			cmd := m.loadSearchPage(max(1, m.searchTarget))
			return m, cmd
		}
		if m.page == tuiPageAccount {
			cmd := m.loadAccounts()
			return m, cmd
		}
		cmd := m.refresh(false)
		return m, cmd
	case "up", "k":
		if m.itemCount() > 0 && *m.selected() > 0 {
			*m.selected()--
		}
		return m, nil
	case "down", "j":
		if *m.selected() < m.itemCount()-1 {
			*m.selected()++
		}
		return m, nil
	}

	switch m.page {
	case tuiPageNowPlaying:
		return m.handlePlaybackKey(key)
	case tuiPageSearch:
		return m.handleSearchKey(key)
	case tuiPageQueue:
		return m.handleQueueKey(key)
	case tuiPageAccount:
		return m.handleAccountKey(key)
	}
	return m, nil
}

func (m tuiModel) handlePlaybackKey(key string) (tea.Model, tea.Cmd) {
	if m.mutationPending || m.sync[syncStatus].dirty || m.sync[syncStatus].err != nil {
		return m, nil
	}
	var path, notice string
	switch key {
	case "enter", "s":
		path, notice = "/api/playback/start", "Playback started"
	case "n":
		path, notice = "/api/playback/next", "Playing next track"
	case "x":
		path, notice = "/api/playback/stop", "Playback stopped"
	case " ", "p":
		switch m.status.Playback.State {
		case "paused":
			path, notice = "/api/playback/resume", "Playback resumed"
		case "playing":
			path, notice = "/api/playback/pause", "Playback paused"
		default:
			path, notice = "/api/playback/start", "Playback started"
		}
	case "left", "h":
		return m.seek(m.status.Playback.Position - 5*time.Second)
	case "right":
		return m.seek(m.status.Playback.Position + 5*time.Second)
	case "m":
		mode := "random"
		if m.playbackMode() == "random" {
			mode = "sequential"
		}
		m.mutationPending = true
		return m, m.setPlaybackMode(mode)
	case "+", "=":
		return m.volume(m.status.Playback.Volume + 5)
	case "-":
		return m.volume(m.status.Playback.Volume - 5)
	default:
		return m, nil
	}
	m.mutationPending = true
	return m, m.playback(path, notice)
}

func (m tuiModel) handleSearchKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		if m.artistSongs {
			m.artistSongs, m.artistName = false, ""
			m.results = nil
			m.searchSelection = boundedSelection(m.artistSelection, len(m.artists))
			m.searchErr = nil
			m.searchWarning = searchErrorsWarning(m.searchFailures)
			m.feedback[tuiPageSearch] = pageFeedback{}
		}
		return m, nil
	case "/":
		m.input = true
		m.query = ""
		m.cursor = 0
		m.cancelSearch()
		m.searchErr = nil
		m.searchWarning = ""
		m.feedback[tuiPageSearch] = pageFeedback{}
		return m, nil
	case "enter", "a":
		if m.searchPending {
			return m, nil
		}
		if m.searchMode == tuiSearchPlaylists {
			if key != "enter" || len(m.playlists) == 0 || m.mutationPending {
				return m, nil
			}
			m.mutationPending = true
			return m, m.playPlaylist(m.playlists[*m.selected()])
		}
		if m.searchMode == tuiSearchArtists && !m.artistSongs {
			if len(m.artists) == 0 {
				return m, nil
			}
			artist := m.artists[*m.selected()]
			cmd := m.searchArtist(artist)
			return m, cmd
		}
		if len(m.results) == 0 {
			return m, nil
		}
		if m.mutationPending {
			return m, nil
		}
		m.mutationPending = true
		return m, m.enqueue(m.results[*m.selected()])
	default:
		return m, nil
	}
}

func (m tuiModel) handleQueueKey(key string) (tea.Model, tea.Cmd) {
	if m.historyView {
		return m, nil
	}
	if key == "h" || key == "enter" {
		m.historyView = true
		m.feedback[tuiPageQueue].notice = ""
		cmd := m.refresh(false)
		return m, cmd
	}
	if key != "d" && key != "delete" {
		return m, nil
	}
	if len(m.queue) == 0 {
		return m, nil
	}
	if m.mutationPending || m.sync[syncQueue].dirty || m.sync[syncQueue].err != nil {
		return m, nil
	}
	entry := m.queue[*m.selected()]
	m.mutationPending = true
	return m, m.deleteQueueEntry(entry)
}

func (m tuiModel) handleAccountKey(key string) (tea.Model, tea.Cmd) {
	platform := tuiPlatformNetEase
	channel := "netease"
	switch key {
	case "esc":
		if m.loginCancel != nil {
			m.loginCancel()
			m.loginCancel = nil
		}
		m.loginGeneration++
		m.loginPending = false
		m.loginKey, m.loginQR = "", ""
		m.loginStatus = "Login cancelled"
		m.feedback[tuiPageAccount] = pageFeedback{}
		return m, nil
	case "l":
	case "q":
		platform, channel = tuiPlatformQQMusic, "qq"
	case "w":
		platform, channel = tuiPlatformQQMusic, "wx"
	case "m":
		platform, channel = tuiPlatformQQMusic, "mobile"
	case "f":
		if m.accountPending || m.loginPending || m.loginKey != "" {
			return m, nil
		}
		m.feedback[tuiPageAccount] = pageFeedback{notice: "Refreshing account sessions"}
		m.accountWarning = ""
		cmd := m.refreshAccounts()
		return m, cmd
	default:
		return m, nil
	}
	if m.accountWriting {
		return m, nil
	}
	if m.accountCancel != nil {
		m.accountCancel()
	}
	m.accountPending = false
	if m.loginCancel != nil {
		m.loginCancel()
	}
	lifetime := m.client.ctx
	if lifetime == nil {
		lifetime = context.Background()
	}
	m.loginContext, m.loginCancel = context.WithCancel(lifetime)
	m.accountGeneration++
	m.loginGeneration++
	m.feedback[tuiPageAccount] = pageFeedback{}
	m.accountWarning = ""
	m.loginPlatform, m.loginChannel, m.loginKey, m.loginQR = platform, channel, "", ""
	m.loginStatus = "Generating new " + platform.label() + " QR code..."
	m.loginPending = true
	return m, m.startLogin(platform, channel)
}

func (m tuiModel) volume(value int) (tea.Model, tea.Cmd) {
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	if value == m.status.Playback.Volume {
		return m, nil
	}
	m.mutationPending = true
	return m, m.setVolume(value)
}

func (m tuiModel) seek(value time.Duration) (tea.Model, tea.Cmd) {
	if value < 0 {
		value = 0
	}
	if m.status.Playback.Duration > 0 && value > m.status.Playback.Duration {
		value = m.status.Playback.Duration
	}
	m.mutationPending = true
	return m, m.setSeek(int(value / time.Millisecond))
}
