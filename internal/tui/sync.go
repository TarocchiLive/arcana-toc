package tui

import tea "github.com/charmbracelet/bubbletea"

const (
	syncStatus = iota
	syncQueue
	syncHistory
)

// Reads started before a mutation completes are discarded, then followed by a fresh read.
func (m *tuiModel) invalidate() {
	for i := range m.sync {
		m.sync[i].generation++
		m.sync[i].dirty = true
	}
}
func (m *tuiModel) refresh(visibleOnly bool) tea.Cmd {
	var cmds []tea.Cmd
	for kind := range m.sync {
		visible := kind == syncStatus || (m.page == tuiPageQueue && ((kind == syncHistory) == m.historyView))
		if !visible || m.mutationPending {
			continue
		}
		state := &m.sync[kind]
		if state.pending {
			continue
		}
		if visibleOnly && kind != syncStatus && !state.dirty {
			continue
		}
		state.pending = true
		generation := state.generation
		cmds = append(cmds, m.readSnapshot(kind, generation))
	}
	return tea.Batch(cmds...)
}
func (m tuiModel) readSnapshot(kind int, generation uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.client.requestContext()
		defer cancel()
		msg := tuiSnapshotMsg{kind: kind, generation: generation}
		switch kind {
		case syncStatus:
			msg.status, msg.err = m.client.status(ctx)
		case syncQueue:
			msg.queue, msg.err = m.client.queue(ctx)
		case syncHistory:
			msg.history, msg.err = m.client.history(ctx)
		}
		return msg
	}
}
func (m *tuiModel) acceptSnapshot(msg tuiSnapshotMsg) tea.Cmd {
	state := &m.sync[msg.kind]
	state.pending = false
	if m.mutationPending {
		state.dirty = true
		return nil
	}
	if msg.generation != state.generation {
		state.dirty = true
		return m.refresh(true)
	}
	state.err = msg.err
	if msg.err != nil {
		return nil
	}
	state.dirty = false
	switch msg.kind {
	case syncStatus:
		m.status = msg.status
	case syncQueue:
		selectedID := int64(0)
		if m.queueSelection >= 0 && m.queueSelection < len(m.queue) {
			selectedID = m.queue[m.queueSelection].ID
		}
		m.queue = msg.queue
		for i, entry := range m.queue {
			if entry.ID == selectedID {
				m.queueSelection = i
				break
			}
		}
		m.queueSelection = boundedSelection(m.queueSelection, len(m.queue))
	case syncHistory:
		selectedID := int64(0)
		if m.historySelection >= 0 && m.historySelection < len(m.history) {
			selectedID = m.history[m.historySelection].ID
		}
		m.history = msg.history
		for i, entry := range m.history {
			if entry.ID == selectedID {
				m.historySelection = i
				break
			}
		}
		m.historySelection = boundedSelection(m.historySelection, len(m.history))
	}
	return nil
}
func boundedSelection(index, count int) int {
	if count == 0 || index < 0 {
		return 0
	}
	if index >= count {
		return count - 1
	}
	return index
}
func (m *tuiModel) selected() *int {
	if m.page == tuiPageQueue {
		if m.historyView {
			return &m.historySelection
		}
		return &m.queueSelection
	}
	return &m.searchSelection
}
