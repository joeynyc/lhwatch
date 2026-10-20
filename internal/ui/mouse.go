package ui

import tea "charm.land/bubbletea/v2"

// Mouse handling. Clicks select, filter, open, and press footer hints; the
// wheel moves the selection or scrolls a panel. A click never sends a
// signal: confirming a stop always needs the y key.

// OSC 22 asks the terminal for a mouse pointer shape by its CSS name, so the
// dashboard shows an arrow instead of a text I-beam. Kitty, Ghostty, foot, and
// recent iTerm2 support it; other terminals ignore it. An empty name restores
// the terminal's own pointer.
const (
	ArrowPointer = "\x1b]22;default\x1b\\"
	ResetPointer = "\x1b]22;\x1b\\"
)

func (m Model) updateClick(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	if mouse.Button != tea.MouseLeft || !m.usableViewport() {
		return m, nil
	}
	x, y := mouse.X, mouse.Y
	if y == m.footerRow() {
		if key := m.footerKey(x); key != "" {
			return m.updateKey(keyPress(key))
		}
		return m, nil
	}
	if m.confirm != nil {
		return m, nil // only the footer's cancel is clickable here
	}
	if m.panelOpen() {
		// Clicking the header or status line closes a panel; clicks inside
		// it do nothing, so text can be read without surprises.
		if y < bodyTop || y == m.noteRow() {
			m.help, m.errors, m.detail, m.scroll = false, false, false, 0
		}
		return m, nil
	}
	if y == searchRow {
		m.searching = true
		return m, nil
	}
	if m.searching {
		m.searching = false // a click elsewhere keeps the query, like Enter
	}
	l := m.layout()
	switch {
	case l.sidebar.contains(x, y):
		m.clickSidebar(y - l.sidebar.y - 1)
	case l.table.contains(x, y):
		header, start, visible := m.tableWindow(l.table.h)
		line := y - l.table.y - 1 - header
		if line < 0 || line >= 2*visible {
			return m, nil
		}
		rows := m.groups()
		i := start + line/2
		if i >= len(rows) {
			return m, nil
		}
		if rows[i].key() == m.selected {
			m.detail, m.scroll = true, 0 // a second click opens details
			return m, nil
		}
		m.selectionIndex, m.selected = i, rows[i].key()
		m.syncListTop()
	case l.preview.contains(x, y):
		if _, ok := m.current(); ok {
			m.detail, m.scroll = true, 0
		}
	}
	return m, nil
}

// clickSidebar handles a click on content line n of the host sidebar.
func (m *Model) clickSidebar(n int) {
	if n < 0 {
		return
	}
	if n <= 1 {
		m.hostFilter = -1
		m.reconcile()
		return
	}
	start, available := m.sidebarWindow(m.layout().sidebar.h)
	i := start + (n-3)/4
	if n >= 3 && i < len(m.hosts) && i < start+available {
		m.hostFilter = i
		m.reconcile()
	}
}

func (m Model) updateWheel(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	delta := 0
	switch mouse.Button {
	case tea.MouseWheelUp:
		delta = -1
	case tea.MouseWheelDown:
		delta = 1
	default:
		return m, nil
	}
	if !m.usableViewport() {
		return m, nil
	}
	if m.panelOpen() {
		m.scrollBy(3 * delta)
		return m, nil
	}
	m.move(delta)
	return m, nil
}
