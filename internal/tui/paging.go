package tui

import tea "github.com/charmbracelet/bubbletea"

// Only the displayed catalog page is retained. Revisiting a page always asks the
// service again because upstream search ranking can change between requests.
func (m *tuiModel) acceptSearchPage(msg tuiSearchMsg) {
	if msg.page == m.searchPage {
		// A retry must not erase healthy results when another platform fails.
		for _, hit := range m.results {
			if _, failed := msg.errors[hit.Ref.Platform]; failed {
				msg.results = append(msg.results, hit)
			}
		}
		for _, artist := range m.artists {
			if _, failed := msg.errors[string(artist.Platform)]; failed {
				msg.artists = append(msg.artists, artist)
			}
		}
		for _, playlist := range m.playlists {
			if _, failed := msg.errors[string(playlist.Platform)]; failed {
				msg.playlists = append(msg.playlists, playlist)
			}
		}
	}
	m.results, m.artists, m.playlists = msg.results, msg.artists, msg.playlists
	m.searchPage, m.searchMore, m.searchFailures = msg.page, msg.hasMore, msg.errors
}

func (m *tuiModel) turnSearchPage(direction int) tea.Cmd {
	if m.searchPending || m.searchQuery == "" || m.artistSongs {
		return nil
	}
	if direction < 0 {
		if m.searchPage > 1 {
			return m.loadSearchPage(m.searchPage - 1)
		}
		return nil
	}
	// Failed transitions retry their exact target; partial pages must resolve
	// every platform before advancing, rather than silently skipping its page.
	if m.searchErr != nil || len(m.searchFailures) > 0 || m.searchPage == 0 {
		return m.loadSearchPage(max(1, m.searchTarget))
	}
	for _, more := range m.searchMore {
		if more {
			return m.loadSearchPage(m.searchPage + 1)
		}
	}
	return nil
}

func (m *tuiModel) moveListPage(direction int) {
	*m.selected() = boundedSelection(*m.selected()+direction*max(1, m.listPageSize()), m.itemCount())
}
