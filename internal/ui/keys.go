package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/joeynyc/lhwatch/internal/domain"
)

// Keyboard handling for the dashboard, its dialogs, and search.

// updateKey routes a key press to the open dialog, search, panel, or list.
func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.confirm != nil {
		if !m.usableViewport() {
			m.confirm = nil
			return m, nil
		}
		switch key {
		case "esc", "n", "enter", "q":
			m.confirm = nil
			m.scroll = 0
		case "down", "j":
			m.scrollBy(1)
		case "up", "k":
			m.scrollBy(-1)
		case "y":
			c := *m.confirm
			m.confirm = nil
			// The target is about to go away; return to the list, not its details.
			m.detail = false
			if reason := m.stopUnavailable(serviceGroup{service: c.service, endpoints: []domain.Service{c.service}}); reason != "" {
				m.notice = "Stop unavailable: " + reason
				m.scroll = 0
				return m, nil
			}
			m.stopping = true
			m.notice = "Requesting SIGTERM…"
			m.scroll = 0
			backend, ctx := m.backend, m.ctx
			return m, func() tea.Msg {
				r, err := backend.Stop(ctx, c.host, c.service)
				return stopMsg{r, err, c.host.Name, c.service.PID}
			}
		}
		return m, nil
	}
	if m.searching {
		switch key {
		case "esc":
			m.searching = false
			m.filter = ""
		case "enter":
			m.searching = false
		case "backspace":
			r := []rune(m.filter)
			if len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		case "ctrl+u":
			m.filter = ""
		default:
			if msg.Text != "" {
				m.filter += clean(msg.Text)
			}
		}
		m.reconcile()
		return m, nil
	}
	if m.help || m.errors || m.detail {
		switch key {
		case "esc", "enter", "?":
			m.help = false
			m.errors = false
			m.detail = false
			m.scroll = 0
		case "q":
			return m, tea.Quit
		case "x":
			// Details advertise x; cancelling the confirmation returns here.
			if m.detail && !m.stopping {
				m.openStop()
			}
		case "o":
			if m.detail {
				return m, m.openSelected()
			}
		case "O":
			if m.detail {
				return m, m.closeSelectedForward()
			}
		case "down", "j":
			m.scrollBy(1)
		case "up", "k":
			m.scrollBy(-1)
		case "pgdown":
			m.scrollBy(max(1, m.height-10))
		case "pgup":
			m.scrollBy(-max(1, m.height-10))
		}
		return m, nil
	}
	switch key {
	case "q":
		return m, tea.Quit
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "home", "g":
		m.move(-len(m.groups()))
	case "end", "G":
		m.move(len(m.groups()))
	case "tab":
		m.hostFilter++
		if m.hostFilter >= len(m.hosts) {
			m.hostFilter = -1
		}
		m.reconcile()
	case "shift+tab":
		m.hostFilter--
		if m.hostFilter < -1 {
			m.hostFilter = len(m.hosts) - 1
		}
		m.reconcile()
	case "/":
		m.searching = true
	case "esc":
		m.filter = ""
		m.notice = ""
		m.reconcile()
	case "enter":
		m.detail = true
		m.scroll = 0
	case "?":
		m.help = true
		m.scroll = 0
	case "e":
		m.errors = true
		m.scroll = 0
	case "r":
		var cmds []tea.Cmd
		for i := range m.hosts {
			cmds = append(cmds, m.refresh(i))
		}
		return m, tea.Batch(cmds...)
	case "x":
		if !m.stopping {
			m.openStop()
		}
	case "o":
		return m, m.openSelected()
	case "O":
		return m, m.closeSelectedForward()
	}
	return m, nil
}
