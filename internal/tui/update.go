package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyRunes && !key.Paste && len(key.Runes) > 1 {
		var commands []tea.Cmd
		for _, r := range key.Runes {
			quit := r == 'q' && !m.input && m.page != tuiPageAccount
			next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: key.Alt})
			m = next.(tuiModel)
			if cmd != nil {
				commands = append(commands, cmd)
			}
			if quit {
				break
			}
		}
		return m, tea.Batch(commands...)
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.helpOffset = min(m.helpOffset, m.maxHelpOffset())
		m.queueSelection = boundedSelection(m.queueSelection, len(m.queue))
		m.historySelection = boundedSelection(m.historySelection, len(m.history))
		if m.page == tuiPageSearch {
			m.searchSelection = boundedSelection(m.searchSelection, m.itemCount())
		}
		return m, nil
	case tuiTickMsg:
		cmd := m.refresh(false)
		return m, tea.Batch(cmd, tuiTick())
	case tuiLoginPollTickMsg:
		if msg.generation != m.loginGeneration || msg.platform != m.loginPlatform || msg.channel != m.loginChannel || msg.key == "" || msg.key != m.loginKey {
			return m, nil
		}
		return m, m.pollLogin(msg.platform, msg.channel, msg.key, msg.generation)
	case tuiSnapshotMsg:
		cmd := m.acceptSnapshot(msg)
		return m, cmd
	case tuiAccountMsg:
		if msg.generation != m.accountGeneration {
			return m, nil
		}
		m.accountPending = false
		if m.accounts == nil {
			m.accounts = make(map[tuiPlatform]tuiAccount)
		}
		if m.accountErrors == nil {
			m.accountErrors = make(map[tuiPlatform]error)
		}
		for _, platform := range []tuiPlatform{tuiPlatformNetEase, tuiPlatformQQMusic} {
			if err := msg.errors[platform]; err != nil {
				delete(m.accounts, platform)
				m.accountErrors[platform] = err
				continue
			}
			if account, ok := msg.accounts[platform]; ok {
				m.accounts[platform] = account
				delete(m.accountErrors, platform)
			}
		}
		m.accountWarning = accountErrorsWarning(msg.errors)
		return m, nil
	case tuiAccountRefreshMsg:
		if msg.generation != m.accountGeneration {
			return m, nil
		}
		m.accountPending = false
		m.accountWriting = false
		if m.accounts == nil {
			m.accounts = make(map[tuiPlatform]tuiAccount)
		}
		if m.accountErrors == nil {
			m.accountErrors = make(map[tuiPlatform]error)
		}
		for _, platform := range []tuiPlatform{tuiPlatformNetEase, tuiPlatformQQMusic} {
			result, ok := msg.results[platform]
			if !ok || !result.accountKnown {
				delete(m.accounts, platform)
				if ok && result.queryErr != nil {
					m.accountErrors[platform] = result.queryErr
				} else {
					delete(m.accountErrors, platform)
				}
				continue
			}
			m.accounts[platform] = result.account
			delete(m.accountErrors, platform)
		}
		summary, allRefreshed := accountRefreshSummary(msg.results)
		if allRefreshed {
			m.feedback[tuiPageAccount].notice = summary
			m.accountWarning = ""
		} else {
			m.accountWarning = summary
		}
		return m, nil
	case tuiSearchMsg:
		if msg.generation != m.searchGeneration || msg.platform != m.platform || msg.mode != m.searchMode {
			return m, nil
		}
		m.searchPending = false
		if msg.err != nil {
			m.searchErr = msg.err
			return m, nil
		}
		if msg.artistSongs {
			m.results = msg.results
		} else {
			m.acceptSearchPage(msg)
		}
		m.artistName = msg.artistName
		m.artistSongs = msg.artistSongs
		m.searchSelection = 0
		m.feedback[tuiPageSearch].notice = fmt.Sprintf("%d %s result(s)", m.itemCount(), m.platform.label())
		if !msg.artistSongs {
			m.searchWarning = searchErrorsWarning(msg.errors)
		}
		m.searchErr = nil
		return m, nil
	case tuiActionMsg:
		m.mutationPending = false
		m.feedback[msg.page] = pageFeedback{notice: msg.notice, err: msg.err}
		m.invalidate()
		cmd := m.refresh(true)
		return m, cmd
	case tuiLoginMsg:
		if msg.generation != m.loginGeneration || msg.platform != m.loginPlatform {
			return m, nil
		}
		m.loginPending = false
		if msg.err != nil {
			m.loginKey = ""
			m.loginQR = ""
			m.loginStatus = "Login error: " + msg.err.Error()
			m.feedback[tuiPageAccount].err = msg.err
			return m, nil
		}
		m.loginPlatform = msg.platform
		m.loginChannel = msg.channel
		m.loginKey = msg.key
		m.loginQR = msg.qr
		m.loginStatus = "Scan the QR code with " + msg.platform.label() + " and confirm on the phone."
		m.feedback[tuiPageAccount] = pageFeedback{notice: "Waiting for login confirmation"}
		return m, m.scheduleLoginPoll(msg.platform, msg.channel, msg.key, msg.generation)
	case tuiLoginPollMsg:
		if msg.generation != m.loginGeneration || msg.platform != m.loginPlatform || msg.channel != m.loginChannel || msg.key == "" || msg.key != m.loginKey {
			return m, nil
		}
		if msg.err != nil {
			m.loginKey = ""
			m.loginQR = ""
			m.loginStatus = "Login error: " + msg.err.Error()
			m.feedback[tuiPageAccount].err = msg.err
			return m, nil
		}
		if msg.platform == tuiPlatformQQMusic {
			switch msg.code {
			case 65:
				m.loginKey, m.loginQR = "", ""
				m.loginStatus = "QR code expired."
			case 66:
				m.loginStatus = "Waiting for scan."
				return m, m.scheduleLoginPoll(msg.platform, msg.channel, msg.key, msg.generation)
			case 67:
				m.loginStatus = "Scanned. Confirm the login on the phone."
				return m, m.scheduleLoginPoll(msg.platform, msg.channel, msg.key, msg.generation)
			case 0:
				m.loginKey, m.loginQR = "", ""
				m.loginStatus = "Login completed."
				m.feedback[tuiPageAccount].notice = "QQ Music account connected"
				m.invalidate()
				syncCmd := m.refresh(true)
				accountCmd := m.loadAccounts()
				return m, tea.Batch(syncCmd, accountCmd)
			case 68:
				m.loginKey, m.loginQR = "", ""
				m.loginStatus = "Login refused on the phone."
			default:
				m.loginKey, m.loginQR = "", ""
				m.loginStatus = fmt.Sprintf("Login error: service returned unknown login status %d", msg.code)
				m.feedback[tuiPageAccount].err = fmt.Errorf("service returned unknown login status %d", msg.code)
			}
			return m, nil
		}
		switch msg.code {
		case 800:
			m.loginKey, m.loginQR = "", ""
			m.loginStatus = "QR code expired."
		case 801:
			m.loginStatus = "Waiting for scan."
			return m, m.scheduleLoginPoll(msg.platform, msg.channel, msg.key, msg.generation)
		case 802:
			m.loginStatus = "Scanned. Confirm the login on the phone."
			return m, m.scheduleLoginPoll(msg.platform, msg.channel, msg.key, msg.generation)
		case 803:
			m.loginKey, m.loginQR = "", ""
			m.loginStatus = "Login completed."
			m.feedback[tuiPageAccount].notice = "NetEase account connected"
			m.invalidate()
			syncCmd := m.refresh(true)
			accountCmd := m.loadAccounts()
			return m, tea.Batch(syncCmd, accountCmd)
		default:
			m.loginKey, m.loginQR = "", ""
			m.loginStatus = fmt.Sprintf("Login error: service returned unknown login status %d", msg.code)
			m.feedback[tuiPageAccount].err = fmt.Errorf("service returned unknown login status %d", msg.code)
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}
