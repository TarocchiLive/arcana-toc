package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

func (m tuiModel) itemCount() int {
	switch m.page {
	case tuiPageSearch:
		if m.searchMode == tuiSearchPlaylists {
			return len(m.playlists)
		}
		if m.searchMode == tuiSearchArtists && !m.artistSongs {
			return len(m.artists)
		}
		return len(m.results)
	case tuiPageQueue:
		if m.historyView {
			return len(m.history)
		}
		return len(m.queue)
	default:
		return 0
	}
}

func (m tuiModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	width := m.width
	title := "ARCANA ToC  " + m.tabs()
	if runewidth.StringWidth(title) > width {
		title = fmt.Sprintf("%d %s", m.page+1, [...]string{"Now Playing", "Search", "Queue", "Account"}[m.page])
		title = runewidth.Truncate(title, width, "")
	}
	if m.height < 6 {
		if m.height == 1 {
			return title
		}
		return title + "\n" + fit("Resize terminal", width)
	}
	var body string
	if m.fullHelp {
		body = m.fullHelpView()
	} else {
		switch m.page {
		case tuiPageNowPlaying:
			body = m.nowPlayingView(width)
		case tuiPageSearch:
			body = m.searchView(width)
		case tuiPageQueue:
			body = m.queueView(width)
		case tuiPageAccount:
			body = m.accountView(width)
		}
	}
	feedback := m.feedback[m.page]
	footer := feedback.notice
	warning := ""
	if m.page == tuiPageSearch {
		warning = m.searchWarning
	}
	if m.page == tuiPageAccount {
		warning = m.accountWarning
	}
	if feedback.err != nil {
		footer = "Error: " + feedback.err.Error()
	} else if warning != "" {
		footer = "Warning: " + warning
	} else {
		for kind, state := range m.sync {
			visible := kind == syncStatus || (m.page == tuiPageQueue && ((kind == syncHistory) == m.historyView))
			if visible && state.err != nil {
				footer = []string{"Status", "Queue", "History"}[kind] + " sync: " + state.err.Error()
				break
			}
		}
	}
	if m.page == tuiPageSearch && m.searchErr != nil {
		footer = "Search error: " + m.searchErr.Error()
	}
	if m.mutationPending || m.searchPending || m.accountPending || m.loginPending {
		footer = "Working…  " + footer
	}
	rows := []string{title, strings.Repeat("-", width)}
	content := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if m.fullHelp {
		content = strings.Split(runewidth.Wrap(body, width), "\n")
		start := min(m.helpOffset, max(0, len(content)-max(1, m.height-5)))
		content = content[start:]
	}
	available := max(0, m.height-5)
	if len(content) > available {
		content = content[:available]
	}
	rows = append(rows, content...)
	rows = append(rows, strings.Repeat("-", width), footer, m.help())
	if len(rows) > m.height {
		rows = rows[:m.height]
	}
	for i := range rows {
		rows[i] = fit(rows[i], width)
	}
	return strings.Join(rows, "\n")
}
func (m tuiModel) fullHelpView() string {
	return `Help
1-4 pages   Ctrl-C quit   q quit outside Account
? / Esc close help   Up/Down scroll
Search: / input   Tab songs/artists/playlists   P platform
Search: PgUp/[ previous page   PgDown/] next or retry failed page
r reload/retry same page; partial platform failures block next page
Up/Down or j/k select; Home/End first/last result on current page
Enter open artist / add song   a add song   Esc return to artists
Playlists: Enter replaces entire queue and plays from first song
Input: arrows, Home/End or Ctrl-A/E move   Backspace/Delete edit
Enter search   Esc cancel input or pending search
Playback: Enter/s start   space/p play/pause   n next   x stop
Left/h and Right seek   +/- volume   m sequential/random
Queue: Up/Down or j/k select   d/Delete remove   h/Enter history
History: Esc/h/Enter return (read only)
Queue/history/artist songs: PgUp/[ and PgDown/] screen pages
Home/End first/last entry; page size follows terminal height
Account: l NetEase   q QQ   w WeChat   m QQ Music APP
f refresh sessions   Esc cancel QR login
r refresh current page; canceling a request does not undo server changes`
}

func (m tuiModel) maxHelpOffset() int {
	lines := strings.Count(runewidth.Wrap(m.fullHelpView(), max(1, m.width)), "\n") + 1
	return max(0, lines-max(1, m.height-5))
}
func (m tuiModel) tabs() string {
	tabs := []string{"1 Now Playing", "2 Search", "3 Queue", "4 Account"}
	for i := range tabs {
		if int(m.page) == i {
			tabs[i] = "[" + tabs[i] + "]"
		}
	}
	return strings.Join(tabs, "  ")
}

func (m tuiModel) nowPlayingView(width int) string {
	pb := m.status.Playback
	var b strings.Builder
	fmt.Fprintf(&b, "State: %s    Mode: %s    Volume: %d%%\n\n", safeText(pb.State), m.playbackMode(), pb.Volume)
	if pb.Entry == nil {
		b.WriteString("No active track.\n")
	} else {
		b.WriteString("Track: ")
		b.WriteString(fit("["+platformShortLabel(pb.Entry.Platform)+"] "+queueEntryLabel(*pb.Entry), width-7))
		b.WriteString("\n")
		fmt.Fprintf(&b, "Position: %s / %s\n", tuiDuration(pb.Position), tuiDuration(pb.Duration))
	}
	if pb.Error != "" {
		b.WriteString("Playback error: ")
		b.WriteString(fit(pb.Error, width-16))
		b.WriteString("\n")
	}
	return b.String()
}

const playlistActionHint = "Enter: replace entire queue and play from first song"

func (m tuiModel) searchView(width int) string {
	var b strings.Builder
	b.WriteString(fit("Search: "+m.platform.label()+" / "+m.searchModeLabel(), width) + "\n")
	if m.searchMode != tuiSearchPlaylists {
		b.WriteString(m.searchPagingLabel() + "\n")
	}
	if m.searchMode == tuiSearchPlaylists {
		b.WriteString(runewidth.Wrap(playlistActionHint, max(1, width)) + "\n")
		b.WriteString(m.searchPagingLabel() + "\n")
		if m.input {
			b.WriteString(m.inputView(width) + "\n")
		}
		if len(m.playlists) == 0 && !m.input && !m.searchPending {
			b.WriteString("No playlists found.\n")
		}
		for i, end := m.listRange(len(m.playlists)); i < end; i++ {
			marker := " "
			if i == *m.selected() {
				marker = ">"
			}
			playlist := m.playlists[i]
			label := "[" + platformShortLabel(string(playlist.Platform)) + "] " + playlist.Name
			b.WriteString(marker + " " + fit(label, width-2) + "\n")
		}
		return b.String()
	}
	if m.searchMode == tuiSearchArtists && !m.artistSongs {
		if m.input {
			b.WriteString(m.inputView(width) + "\n")
		}
		if len(m.artists) == 0 && !m.input && !m.searchPending {
			b.WriteString("No artists found.\n")
		}
		for i, end := m.listRange(len(m.artists)); i < end; i++ {
			marker := " "
			if i == *m.selected() {
				marker = ">"
			}
			label := "[" + platformShortLabel(string(m.artists[i].Platform)) + "] " + m.artists[i].Name
			b.WriteString(marker + " " + fit(label, width-2) + "\n")
		}
		return b.String()
	}
	if m.input {
		b.WriteString("Search: ")
		b.WriteString(m.inputView(width - 8))
		b.WriteString("\n")
	}
	if len(m.results) == 0 && !m.input {
		b.WriteString("No search results.\n")
	}
	for i, end := m.listRange(len(m.results)); i < end; i++ {
		hit := m.results[i]
		marker := " "
		if i == *m.selected() {
			marker = ">"
		}
		label := "[" + platformShortLabel(hit.Ref.Platform) + "] " + hit.Title
		if hit.PaidHint {
			label += " [VIP]"
		}
		if len(hit.Artists) > 0 {
			label += " - " + strings.Join(hit.Artists, ", ")
		}
		if hit.Album != "" {
			label += " [" + hit.Album + "]"
		}
		fmt.Fprintf(&b, "%s %s\n", marker, fit(label, width-2))
	}
	return b.String()
}

func (m tuiModel) queueView(width int) string {
	var b strings.Builder
	if m.historyView {
		b.WriteString("Playback history (read only)\n")
		b.WriteString(m.listPagingLabel(len(m.history)) + "\n")
		if len(m.history) == 0 {
			b.WriteString("History is empty.\n")
		}
		for i, end := m.listRange(len(m.history)); i < end; i++ {
			entry := m.history[i]
			marker := " "
			if i == *m.selected() {
				marker = ">"
			}
			label := fmt.Sprintf("%d. [%s] %s", i+1, platformShortLabel(entry.Platform), queueEntryLabel(tuiQueueEntry{Title: entry.Title, Artist: entry.Artist}))
			if entry.VIP {
				label += " [VIP]"
			}
			label += " (" + entry.Outcome + ")"
			fmt.Fprintf(&b, "%s %s\n", marker, fit(label, width-2))
		}
		return b.String()
	}
	b.WriteString("Active queue\n")
	b.WriteString(m.listPagingLabel(len(m.queue)) + "\n")
	if len(m.queue) == 0 {
		b.WriteString("Queue is empty.\n")
	} else {
		for i, end := m.listRange(len(m.queue)); i < end; i++ {
			entry := m.queue[i]
			marker := " "
			if i == *m.selected() {
				marker = ">"
			}
			label := fmt.Sprintf("%d. [%s] %s", i+1, platformShortLabel(entry.Platform), queueEntryLabel(entry))
			if entry.VIP {
				label += " [VIP]"
			}
			fmt.Fprintf(&b, "%s %s\n", marker, fit(label, width-2))
		}
	}
	return b.String()
}

func (m tuiModel) listPageSize() int {
	overhead := 2
	if m.page == tuiPageSearch && m.input {
		overhead++
	}
	if m.page == tuiPageSearch && m.searchMode == tuiSearchPlaylists {
		overhead += strings.Count(runewidth.Wrap(playlistActionHint, max(1, m.width)), "\n") + 1
	}
	return max(0, m.height-5-overhead)
}

func (m tuiModel) listRange(count int) (int, int) {
	limit := m.listPageSize()
	if limit == 0 || count == 0 {
		return 0, 0
	}
	start := boundedSelection(*m.selected(), count) / limit * limit
	return start, min(count, start+limit)
}

func (m tuiModel) listPagingLabel(count int) string {
	if count == 0 {
		return "0 entries"
	}
	if m.listPageSize() == 0 {
		return "Resize terminal to show entries"
	}
	start, end := m.listRange(count)
	size := max(1, m.listPageSize())
	return fmt.Sprintf("%d-%d of %d | Page %d/%d", start+1, end, count, start/size+1, (count+size-1)/size)
}

func (m tuiModel) searchPagingLabel() string {
	if m.artistSongs {
		return m.listPagingLabel(len(m.results))
	}
	if m.searchPending {
		return fmt.Sprintf("Loading page %d… | showing page %d", m.searchTarget, m.searchPage)
	}
	if m.searchErr != nil {
		return fmt.Sprintf("Page %d failed | r retry | showing page %d", m.searchTarget, m.searchPage)
	}
	if m.searchPage == 0 {
		return "/ search to begin"
	}
	parts := []string{fmt.Sprintf("Page %d", m.searchPage)}
	for _, platform := range []tuiPlatform{tuiPlatformNetEase, tuiPlatformQQMusic} {
		if m.platform != tuiPlatformAll && m.platform != platform {
			continue
		}
		state := "end"
		if _, failed := m.searchFailures[string(platform)]; failed {
			state = "failed: r retry"
		} else if m.searchMore[string(platform)] {
			state = "more"
		}
		parts = append(parts, platformShortLabel(string(platform))+": "+state)
	}
	return strings.Join(parts, " | ")
}
func (m tuiModel) accountView(width int) string {
	var b strings.Builder
	for _, platform := range []tuiPlatform{tuiPlatformNetEase, tuiPlatformQQMusic} {
		label := platform.label() + " account: "
		if err := m.accountErrors[platform]; err != nil {
			b.WriteString(label + fit("status unavailable: "+err.Error(), width-len(label)) + "\n")
			continue
		}
		account, known := m.accounts[platform]
		if !known {
			b.WriteString(label + "status not loaded\n")
			continue
		}
		if account.LoggedIn {
			identity := account.Name
			if identity == "" {
				identity = account.ID
			} else if account.ID != "" && account.ID != identity {
				identity += " (" + account.ID + ")"
			}
			b.WriteString(label + fit(identity, width-len(label)) + "\n")
		} else {
			b.WriteString(label + "not logged in\n")
		}
	}
	if m.loginQR != "" {
		qr := strings.Trim(m.loginQR, "\n")
		lines := strings.Split(qr, "\n")
		reserved := 2 + 1
		if m.loginStatus != "" {
			reserved += 2
		}
		fits := len(lines) <= max(0, m.height-5-reserved)
		for _, line := range lines {
			if runewidth.StringWidth(line) > width {
				fits = false
			}
		}
		if fits {
			b.WriteString("\n" + qr + "\n")
		} else {
			b.WriteString("\nResize terminal for login QR.\n")
		}
	}
	if m.loginStatus != "" {
		b.WriteString("\n" + fit(m.loginStatus, width) + "\n")
	}
	return b.String()
}

func (m tuiModel) help() string {
	items := []string{"? help"}
	switch {
	case m.fullHelp:
		items = []string{"Up/Down scroll", "Esc close"}
	case m.input:
		items = []string{"Enter search", "Esc cancel"}
	case m.page == tuiPageNowPlaying:
		items = append(items, "space play/pause", "n next", "+/- volume")
	case m.page == tuiPageSearch && m.searchPending:
		items = append(items, "Esc cancel", "/ new search")
	case m.page == tuiPageSearch && m.artistSongs:
		items = append(items, "Enter add", "Esc artists")
	case m.page == tuiPageSearch && m.searchMode == tuiSearchArtists:
		items = append(items, "/ search", "Enter songs", "Tab mode")
	case m.page == tuiPageSearch && m.searchMode == tuiSearchPlaylists:
		items = []string{"Enter replace+play", "/ search", "P platform", "Tab mode", "? help"}
	case m.page == tuiPageSearch:
		items = append(items, "/ search", "Enter add", "Tab mode")
	case m.page == tuiPageQueue && m.historyView:
		items = append(items, "Esc queue")
	case m.page == tuiPageQueue:
		items = append(items, "d remove", "h history")
	case m.page == tuiPageAccount && (m.loginPending || m.loginKey != ""):
		items = append(items, "Esc cancel")
	case m.page == tuiPageAccount:
		items = append(items, "l/q/w/m login", "f refresh")
	}
	if !m.fullHelp && !m.input && (m.page == tuiPageSearch || m.page == tuiPageQueue) {
		if m.page == tuiPageSearch {
			items = append([]string{"[ prev", "] next", "PgUp/PgDown pages"}, items...)
		} else {
			items = append([]string{"[/] pages"}, items...)
		}
	}
	var b strings.Builder
	used := 0
	for _, item := range items {
		separator := 0
		if used > 0 {
			separator = 2
		}
		width := runewidth.StringWidth(item)
		if used+separator+width > m.width {
			break
		}
		if separator > 0 {
			b.WriteString("  ")
		}
		b.WriteString(item)
		used += separator + width
	}
	return b.String()
}

func platformShortLabel(platform string) string {
	if platform == string(tuiPlatformQQMusic) {
		return "QQ"
	}
	return "NetEase"
}

func searchErrorsWarning(errorsByPlatform map[string]string) string {
	parts := make([]string, 0, 2)
	for _, platform := range []tuiPlatform{tuiPlatformNetEase, tuiPlatformQQMusic} {
		if message := strings.TrimSpace(errorsByPlatform[string(platform)]); message != "" {
			parts = append(parts, platform.label()+": "+message)
		}
	}
	return strings.Join(parts, "; ")
}

func accountErrorsWarning(errorsByPlatform map[tuiPlatform]error) string {
	parts := make([]string, 0, 2)
	for _, platform := range []tuiPlatform{tuiPlatformNetEase, tuiPlatformQQMusic} {
		if err := errorsByPlatform[platform]; err != nil {
			parts = append(parts, platform.label()+": "+err.Error())
		}
	}
	return strings.Join(parts, "; ")
}

func accountRefreshSummary(results map[tuiPlatform]tuiAccountRefreshResult) (string, bool) {
	parts := make([]string, 0, 2)
	allRefreshed := len(results) == 2
	for _, platform := range []tuiPlatform{tuiPlatformNetEase, tuiPlatformQQMusic} {
		result, ok := results[platform]
		if !ok {
			allRefreshed = false
			parts = append(parts, platform.label()+": no refresh result")
			continue
		}
		message := "session refreshed"
		switch {
		case result.queryErr != nil && result.refreshErr != nil:
			message = "refresh failed: " + result.refreshErr.Error() + "; account status unavailable: " + result.queryErr.Error()
		case result.queryErr != nil:
			message = "account status unavailable: " + result.queryErr.Error()
		case result.skipped:
			message = "not logged in; log in first"
		case result.refreshErr != nil && result.accountKnown && !result.account.LoggedIn:
			message = "refresh failed: " + result.refreshErr.Error() + "; session is no longer logged in; log in again"
		case result.refreshErr != nil:
			message = "refresh failed: " + result.refreshErr.Error()
		case !result.accountKnown:
			message = "account status unavailable"
		case !result.account.LoggedIn:
			message = "session is no longer logged in; log in again"
		case !result.refreshed:
			message = "session was not refreshed"
		}
		if !result.refreshed {
			allRefreshed = false
		}
		parts = append(parts, platform.label()+": "+message)
	}
	return strings.Join(parts, "; "), allRefreshed
}

func queueEntryLabel(entry tuiQueueEntry) string {
	if entry.Artist == "" {
		return entry.Title
	}
	if entry.Title == "" {
		return entry.Artist
	}
	return entry.Title + " - " + entry.Artist
}

func tuiDuration(value time.Duration) string {
	if value <= 0 {
		return "--:--"
	}
	seconds := int(value.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

func fit(value string, width int) string {
	if width <= 0 {
		return ""
	}
	marker := strings.Repeat(".", min(width, 3))
	return runewidth.Truncate(safeText(value), width, marker)
}

// Strip terminal escapes before clipping; remote metadata must never become terminal commands.
func safeText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, ansi.Strip(value))
}
func (m tuiModel) inputView(width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(m.query)
	cursor := min(max(0, m.cursor), len(r))
	prefix := string(r[:cursor])
	for runewidth.StringWidth(prefix)+1 > width && len(prefix) > 0 {
		_, size := utf8.DecodeRuneInString(prefix)
		prefix = prefix[size:]
	}
	return fit(prefix+"▏"+string(r[cursor:]), width)
}
