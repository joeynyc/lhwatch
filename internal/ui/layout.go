package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Screen geometry shared by rendering and mouse hit-testing, so what is drawn
// and what is clickable cannot drift apart.

const (
	gutter    = 2 // left margin of every dashboard line
	searchRow = 4 // screen row of the search bar
	bodyTop   = 6 // first screen row of the body
)

type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return r.w > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// bodyLayout places the body's boxes. Absent boxes have zero width.
type bodyLayout struct{ sidebar, table, preview, panel rect }

func (m Model) panelOpen() bool { return m.confirm != nil || m.help || m.detail || m.errors }

func (m Model) layout() bodyLayout {
	inner, bodyH := m.modalSize()
	switch {
	case m.panelOpen():
		return bodyLayout{panel: rect{gutter, bodyTop, inner, bodyH}}
	case m.width >= 120:
		sideW, detailW := 22, 34
		table := rect{gutter + sideW + 1, bodyTop, inner - sideW - detailW - 2, bodyH}
		return bodyLayout{sidebar: rect{gutter, bodyTop, sideW, bodyH}, table: table, preview: rect{table.x + table.w + 1, bodyTop, detailW, bodyH}}
	case m.width >= 76:
		sideW := 20
		return bodyLayout{sidebar: rect{gutter, bodyTop, sideW, bodyH}, table: rect{gutter + sideW + 1, bodyTop, inner - sideW - 1, bodyH}}
	default:
		return bodyLayout{table: rect{gutter, bodyTop, inner, bodyH}}
	}
}

func (m Model) noteRow() int   { return bodyTop + m.height - 10 }
func (m Model) footerRow() int { return m.noteRow() + 1 }

// tableWindow returns the table's header line count and the first index and
// number of visible groups for a table box of height h. Each group takes a
// name line and a context line.
func (m Model) tableWindow(h int) (header, start, visible int) {
	header = 1
	if h > 7 {
		header = 2 // column titles and a rule
	}
	visible = max(1, (h-3-header)/2)
	// Keep the window where it was unless the selection left it.
	start = min(max(m.listTop, m.selectionIndex-visible+1), m.selectionIndex)
	start = max(0, min(start, len(m.groups())-visible))
	return header, start, visible
}

// syncListTop records the table window after the selection or size changes.
func (m *Model) syncListTop() {
	_, start, _ := m.tableWindow(m.layout().table.h)
	m.listTop = start
}

// sidebarWindow returns the first visible host and how many fit; each host
// takes four lines below the three-line "All hosts" heading.
func (m Model) sidebarWindow(h int) (start, available int) {
	available = max(1, (h-6)/4)
	if m.hostFilter >= available {
		start = m.hostFilter - available + 1
	}
	return start, available
}

// footerItem is one footer hint; key is what a click sends, "" if inert.
type footerItem struct{ label, key string }

func (m Model) footerItems() ([]footerItem, string) {
	switch {
	case m.searching:
		return []footerItem{{"Type to filter", ""}, {"Backspace edit", ""}, {"Ctrl+U clear", "ctrl+u"}, {"Enter apply", "enter"}, {"Esc cancel", "esc"}}, "   "
	case m.confirm != nil:
		// Sending a signal stays keyboard-only; a click can only cancel.
		return []footerItem{{"Enter / Esc cancel", "esc"}, {"y send SIGTERM", ""}, {"↑↓ scroll", ""}}, "   "
	case m.width < 76:
		return []footerItem{{"↑↓ select", ""}, {"/ find", "/"}, {"↵ info", "enter"}, {"o open", "o"}, {"x stop", "x"}, {"q quit", "q"}}, "  "
	case m.width < 112:
		return []footerItem{{"↑↓ select", ""}, {"/ search", "/"}, {"Tab host", "tab"}, {"↵ details", "enter"}, {"o open", "o"}, {"x stop", "x"}, {"? help", "?"}, {"q quit", "q"}}, "  "
	default:
		return []footerItem{{"↑↓ select", ""}, {"/ search", "/"}, {"Tab host", "tab"}, {"Enter details", "enter"}, {"o open", "o"}, {"x stop", "x"}, {"r refresh", "r"}, {"e diagnostics", "e"}, {"? help", "?"}, {"q quit", "q"}}, "   "
	}
}

func (m Model) footerText() string {
	items, sep := m.footerItems()
	labels := make([]string, len(items))
	for i, item := range items {
		labels[i] = item.label
	}
	return strings.Join(labels, sep)
}

// footerKey returns the key for a click at screen column x on the footer.
func (m Model) footerKey(x int) string {
	items, sep := m.footerItems()
	at := gutter
	for _, item := range items {
		w := ansi.StringWidth(item.label)
		if x >= at && x < at+w {
			return item.key
		}
		at += w + ansi.StringWidth(sep)
	}
	return ""
}

// keyPress builds the key event a footer click stands for.
func keyPress(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(name)[0], Text: name}
}
