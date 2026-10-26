package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/skip2/go-qrcode"
)

func (m *tuiModel) cancelSearch() {
	if m.searchCancel != nil {
		m.searchCancel()
	}
	m.searchGeneration++
	m.searchPending = false
	m.searchCancel = nil
}
func (m *tuiModel) search(query string) tea.Cmd {
	m.results, m.artists, m.playlists = nil, nil, nil
	m.artistSongs, m.artistName = false, ""
	m.searchSelection = 0
	m.searchQuery, m.searchPage = query, 0
	m.searchMore, m.searchFailures = nil, nil
	return m.loadSearchPage(1)
}

func (m *tuiModel) loadSearchPage(page int) tea.Cmd {
	m.cancelSearch()
	m.searchErr, m.searchWarning = nil, ""
	m.feedback[tuiPageSearch] = pageFeedback{}
	m.searchPending, m.searchTarget = true, page
	ctx, cancel := m.client.requestContext()
	m.searchCancel = cancel
	client := m.client
	platform, mode, query, generation := m.platform, m.searchMode, m.searchQuery, m.searchGeneration
	return func() tea.Msg {
		defer cancel()
		response, err := client.searchPage(ctx, platform, mode, query, page)
		return tuiSearchMsg{platform: platform, mode: mode, query: query, generation: generation,
			page: page, hasMore: response.HasMore, results: response.Hits, artists: response.Artists,
			playlists: response.Playlists, errors: response.Errors, err: err}
	}
}
func (m *tuiModel) searchArtist(artist tuiArtist) tea.Cmd {
	m.cancelSearch()
	m.artistSelection = m.searchSelection
	m.searchErr = nil
	m.feedback[tuiPageSearch] = pageFeedback{}
	m.searchPending = true
	ctx, cancel := m.client.requestContext()
	m.searchCancel = cancel
	generation, scope := m.searchGeneration, m.platform
	return func() tea.Msg {
		defer cancel()
		hits, err := m.client.artistSongs(ctx, artist.Platform, artist.ID)
		return tuiSearchMsg{generation: generation, platform: scope, results: hits, mode: tuiSearchArtists, artistSongs: true, artistName: artist.Name, err: err}
	}
}

func (m tuiModel) enqueue(hit tuiSearchHit) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.client.requestContext()
		defer cancel()
		err := m.client.enqueue(ctx, hit)
		return tuiActionMsg{page: tuiPageSearch, notice: "Added " + hit.Title + " to the queue", err: err}
	}
}

func (m tuiModel) playPlaylist(playlist tuiPlaylist) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.client.requestContextWithTimeout(tuiPlaylistTimeout)
		defer cancel()
		err := m.client.playPlaylist(ctx, playlist)
		return tuiActionMsg{page: tuiPageSearch, notice: "Replaced entire queue and started playlist " + playlist.Name, err: err}
	}
}

func (m tuiModel) deleteQueueEntry(entry tuiQueueEntry) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.client.requestContext()
		defer cancel()
		err := m.client.deleteQueueEntry(ctx, entry.ID)
		return tuiActionMsg{page: tuiPageQueue, notice: "Removed " + queueEntryLabel(entry) + " from the queue", err: err}
	}
}

func (m tuiModel) playback(path, notice string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.client.requestContext()
		defer cancel()
		err := m.client.playback(ctx, path)
		return tuiActionMsg{page: tuiPageNowPlaying, notice: notice, err: err}
	}
}

func (m tuiModel) setPlaybackMode(mode string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.client.requestContext()
		defer cancel()
		err := m.client.setPlaybackMode(ctx, mode)
		return tuiActionMsg{page: tuiPageNowPlaying, notice: "Playback mode set to " + mode, err: err}
	}
}

func (m tuiModel) setVolume(value int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.client.requestContext()
		defer cancel()
		err := m.client.setVolume(ctx, value)
		return tuiActionMsg{page: tuiPageNowPlaying, notice: fmt.Sprintf("Volume set to %d%%", value), err: err}
	}
}

func (m *tuiModel) loadAccounts() tea.Cmd    { return m.accountCommand(false) }
func (m *tuiModel) refreshAccounts() tea.Cmd { return m.accountCommand(true) }
func (m *tuiModel) accountCommand(refresh bool) tea.Cmd {
	if m.accountWriting {
		return nil
	}
	m.accountWriting = refresh
	if m.accountCancel != nil {
		m.accountCancel()
	}
	m.accountGeneration++
	m.accountPending = true
	generation := m.accountGeneration
	ctx, cancel := m.client.requestContext()
	m.accountCancel = cancel
	return func() tea.Msg {
		defer cancel()
		type platformResult struct {
			platform tuiPlatform
			result   tuiAccountRefreshResult
		}
		ch := make(chan platformResult, 2)
		for _, platform := range []tuiPlatform{tuiPlatformNetEase, tuiPlatformQQMusic} {
			go func(platform tuiPlatform) {
				result := tuiAccountRefreshResult{}
				account, err := m.client.account(ctx, platform)
				result.account, result.accountKnown, result.queryErr = account, err == nil, err
				if refresh && err == nil {
					if !account.LoggedIn {
						result.skipped = true
					} else {
						result.refreshErr = m.client.refreshAccount(ctx, platform)
						account, err = m.client.account(ctx, platform)
						result.account, result.accountKnown, result.queryErr = account, err == nil, err
						result.refreshed = err == nil && result.refreshErr == nil && account.LoggedIn
					}
				}
				ch <- platformResult{platform, result}
			}(platform)
		}
		results := make(map[tuiPlatform]tuiAccountRefreshResult, 2)
		for range 2 {
			r := <-ch
			results[r.platform] = r.result
		}
		if refresh {
			return tuiAccountRefreshMsg{generation: generation, results: results}
		}
		accounts := make(map[tuiPlatform]tuiAccount, 2)
		errs := make(map[tuiPlatform]error, 2)
		for platform, result := range results {
			if result.queryErr != nil {
				errs[platform] = result.queryErr
			} else {
				accounts[platform] = result.account
			}
		}
		return tuiAccountMsg{generation: generation, accounts: accounts, errors: errs}
	}
}

func (m tuiModel) setSeek(value int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.client.requestContext()
		defer cancel()
		err := m.client.seek(ctx, value)
		return tuiActionMsg{page: tuiPageNowPlaying, notice: "Playback position updated", err: err}
	}
}
func (m tuiModel) startLogin(platform tuiPlatform, channel string) tea.Cmd {
	generation := m.loginGeneration
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.loginContext, tuiRequestTimeout)
		defer cancel()
		login, err := m.client.startLogin(ctx, platform, channel)
		if err != nil {
			return tuiLoginMsg{platform: platform, channel: channel, generation: generation, err: err}
		}
		var qr string
		if platform == tuiPlatformQQMusic {
			qr, err = renderLoginImage(login.Image)
		} else {
			var code *qrcode.QRCode
			code, err = qrcode.New(login.URL, qrcode.Medium)
			if err == nil {
				qr = code.ToSmallString(false)
			}
		}
		if err != nil {
			return tuiLoginMsg{platform: platform, channel: channel, generation: generation, err: err}
		}
		return tuiLoginMsg{platform: platform, channel: channel, generation: generation, key: login.Identifier, qr: qr}
	}
}

func (m tuiModel) pollLogin(platform tuiPlatform, channel, key string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.loginContext, tuiRequestTimeout)
		defer cancel()
		code, err := m.client.pollLogin(ctx, platform, channel, key)
		return tuiLoginPollMsg{platform: platform, channel: channel, key: key, generation: generation, code: code, err: err}
	}
}
