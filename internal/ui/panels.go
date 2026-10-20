package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Scrollable panels: details, help, diagnostics, and stop confirmation.

func (m Model) diagnostics(w int) []string {
	out := []string{accent("HOST OBSERVATIONS"), dim("Listening sockets are not health checks."), ""}
	for _, h := range m.hosts {
		out = append(out, accent(clean(h.host.Name))+"  "+m.status(h), dim("Last observation: "+age(h.observed, m.now)))
		if h.err != "" {
			out = append(out, wrapField("ERROR", h.err, w)...)
		}
		for _, warning := range h.snapshot.Warnings {
			out = append(out, wrapField("WARNING", warning, w)...)
		}
		if h.snapshot.Partial {
			out = append(out, paint("Limited details: some process owners or endpoints could not be read.", amber))
		}
		out = append(out, "")
	}
	if m.forwarder != nil {
		tunnels := []string{}
		for _, f := range m.forwarder.Active() {
			if !f.Direct {
				tunnels = append(tunnels, describeForward(f))
			}
		}
		if len(tunnels) > 0 {
			out = append(out, accent("SSH TUNNELS"), dim("Bound to 127.0.0.1 here; closed when lhwatch exits."))
			out = append(out, tunnels...)
			out = append(out, "")
		}
	}
	if m.notice != "" {
		out = append(out, wrapField("LAST ACTION", m.notice, w)...)
	}
	return out
}

// modalSize is the outer size of the panel that replaces the dashboard body.
func (m Model) modalSize() (w, h int) { return m.width - 4, m.height - 10 }

// maxScroll is the last offset that still shows a full page of the open panel,
// so scrolling past the end never needs extra key presses to come back.
func (m Model) maxScroll() int {
	w, h := m.modalSize()
	_, lines := m.modalLines(w - 4)
	return max(0, len(lines)-max(1, h-3))
}

func (m *Model) scrollBy(delta int) { m.scroll = min(max(0, m.scroll+delta), m.maxScroll()) }

func (m Model) modalLines(inner int) (string, []string) {
	title := "DETAILS"
	var lines []string
	switch {
	case m.confirm != nil:
		c := m.confirm
		title = "CONFIRM PROCESS STOP"
		lines = []string{paint("Send SIGTERM to this exact process?", amber), ""}
		for _, f := range [][2]string{{"PROJECT / SERVICE", c.service.DisplayName()}, {"HOST", c.host.Name}, {"PROCESS / PID", fmt.Sprintf("%s / %d", c.service.Name, c.service.PID)}, {"COMMAND", c.service.Command}} {
			lines = append(lines, wrapField(f[0], f[1], inner)...)
			lines = append(lines, "")
		}
		lines = append(lines, dim("ALL KNOWN ENDPOINTS AFFECTED"))
		for _, ep := range c.endpoints {
			lines = append(lines, strings.Split(ansi.Hardwrap(ep, inner, false), "\n")...)
		}
		lines = append(lines, "", dim("The collector revalidates the process before signaling."), dim("No automatic force kill. A signal is not proof of exit."), "", accent("[ Enter / Esc: CANCEL ]    y: send SIGTERM"))
	case m.help:
		title = "KEYBOARD GUIDE"
		lines = []string{accent("A quieter view of what is listening."), "", "↑ / ↓ · j / k     Select service", "Tab / Shift+Tab   Cycle host filter", "/                 Search project, process, host, port, path", "Enter             Open full service details", "Esc               Close panel / clear search", "r                 Refresh all available hosts", "x                 Confirm selected process SIGTERM", "o                 Open in a browser (SSH tunnel if remote)", "O                 Close the selected service's tunnel", "e                 Full errors, warnings & last action", "?                 This guide", "q · Ctrl+C        Quit", "", dim("Mouse: click a service to select it, click again for details."), dim("Wheel moves the selection or scrolls a panel; footer hints are clickable."), dim("Stopping always needs the y key."), dim("To select text: Option-drag in iTerm2, ⌘R or Fn-drag in Terminal,"), dim("Shift-drag in most others, or start lhwatch with --no-mouse."), "", dim("In search: type freely, Backspace edits, Ctrl+U clears."), dim("Enter keeps your query; Esc clears it."), "", dim("In panels: ↑/↓ or j/k scroll; PgUp/PgDn page."), dim("Each row groups endpoints of one verified process or container."), dim("Observations show listening sockets, not health checks."), dim("Stale data remains visible when a host is unreachable.")}
	case m.errors:
		title = "DIAGNOSTICS"
		lines = m.diagnostics(inner)
	default:
		lines = m.detailLines(inner)
	}
	// Wrap static instructions too, so every word remains reachable on narrow screens.
	var wrapped []string
	for _, l := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Hardwrap(l, inner, false), "\n")...)
	}
	return title, wrapped
}

func (m Model) modal(w, h int) []string {
	title, lines := m.modalLines(w - 4)
	visible := max(1, h-3)
	start := min(m.scroll, max(0, len(lines)-visible))
	content := append([]string{}, lines[start:min(len(lines), start+visible)]...)
	for len(content) < h-3 {
		content = append(content, "")
	}
	content = append(content, dim(fmt.Sprintf("%d–%d / %d lines · ↑↓ scroll · Esc close", min(start+1, len(lines)), min(len(lines), start+visible), len(lines))))
	return box(title, content, w, h)
}
