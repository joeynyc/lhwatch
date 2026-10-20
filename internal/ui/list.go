package ui

import (
	"fmt"
	"strings"

	lg "charm.land/lipgloss/v2"
)

// The host sidebar and the service list.

func (m Model) status(h hostState) string {
	if h.err != "" {
		if h.observed.IsZero() {
			return paint("! unavailable", amber)
		}
		return paint("! stale · error", amber)
	}
	if h.observed.IsZero() {
		return dim("◌ connecting")
	}
	if m.stale(h) {
		return paint("! stale", amber)
	}
	if h.inflight {
		return paint("◌ refreshing", cyan)
	}
	if h.snapshot.Partial {
		return paint("! limited details", amber)
	}
	return accent("● observed")
}

func (m Model) sidebar(w, h int) []string {
	rows := []string{}
	all := "  All hosts"
	if m.hostFilter == -1 {
		all = "› All hosts"
	}
	rows = append(rows, accent(all), dim(fmt.Sprintf("  %d targets", len(m.hosts))), "")
	// Keep the active host visible when many configured hosts exceed the viewport.
	start, available := m.sidebarWindow(h)
	for i := start; i < len(m.hosts) && i < start+available; i++ {
		host := m.hosts[i]
		prefix := "  "
		if m.hostFilter == i {
			prefix = "› "
		}
		name := prefix + clean(host.host.Name)
		detail := age(host.observed, m.now)
		unidentified := 0
		for _, g := range m.hostGroups(i) {
			if g.unidentified() {
				unidentified++
			}
		}
		if unidentified > 0 {
			detail = fmt.Sprintf("%d unidentified", unidentified)
		}
		rows = append(rows, lg.NewStyle().Foreground(ink).Bold(m.hostFilter == i).Render(name), m.status(host), dim("  "+detail), "")
	}
	if len(m.hosts) > available {
		rows = append(rows, dim("Tab · next host"))
	}
	return box("HOSTS", rows, w, h)
}

func (m Model) table(w, h int) []string {
	inner := w - 4
	rows := m.groups()
	hostW, portW := 10, 12
	nameW := inner - hostW - portW - 5
	heading := "  " + fit("PROJECT / SERVICE", nameW) + " " + fit("PORTS", portW) + "  " + fit("HOST", hostW)
	content := []string{dim(heading)}
	header, start, visible := m.tableWindow(h)
	if header > 1 {
		content = append(content, dim(strings.Repeat("─", inner)))
	}
	for i := start; i < len(rows) && i < start+visible; i++ {
		g := rows[i]
		s := g.service
		indicator := "  "
		if g.key() == m.selected || (m.selected == "" && i == m.selectionIndex) {
			indicator = "› "
		}
		row := indicator + cell(s.DisplayName(), nameW) + " " + fit(portSummary(g, portW), portW) + "  " + cell(s.Host, hostW)
		if indicator == "› " {
			row = lg.NewStyle().Foreground(mint).Background(lg.Color("#233B41")).Bold(true).Render(fit(row, inner))
		} else {
			row = lg.NewStyle().Foreground(ink).Bold(true).Render(row)
		}
		context := serviceContext(g)
		content = append(content, row, dim(cell("  "+context, inner)))
	}
	if len(rows) == 0 {
		loading := false
		for _, host := range m.hosts {
			if host.inflight && host.observed.IsZero() {
				loading = true
			}
		}
		switch {
		case loading:
			content = append(content, accent("Listening for observations…"), dim("Hosts refresh independently."))
		case m.filter != "":
			content = append(content, accent("No matches"), dim("Esc clears the search."))
		default:
			content = append(content, accent("No listening services"), dim("Press e for host diagnostics."))
		}
	}
	for len(content) < h-3 {
		content = append(content, "")
	}
	endpoints := 0
	for _, g := range rows {
		endpoints += len(g.endpoints)
	}
	content = append(content, dim(fmt.Sprintf("%d service%s · %d endpoints", len(rows), plural(len(rows)), endpoints)))
	return box("LISTENING", content, w, h)
}

// Port numbers are indivisible: show an overflow count instead of a clipped number.
func portSummary(g serviceGroup, w int) string {
	ports := g.ports()
	if len(ports) <= w {
		return ports
	}
	values := strings.Split(ports, ", ")
	return fmt.Sprintf("%s +%d", values[0], len(values)-1)
}

func serviceContext(g serviceGroup) string {
	s := g.service
	if g.container() {
		context := fmt.Sprintf("Docker · %d endpoint%s", len(g.endpoints), plural(len(g.endpoints)))
		if s.ContainerImage != "" {
			context += " · " + s.ContainerImage
		}
		return context
	}
	if g.unidentified() {
		if s.VisibilityReason != "" {
			return s.VisibilityReason
		}
		return "Process details unavailable · Enter to inspect"
	}
	context := s.Name
	if context == "" || context == "unknown" {
		context = "Process details unavailable"
	}
	if len(g.endpoints) > 1 {
		context += fmt.Sprintf(" · %d endpoints", len(g.endpoints))
	}
	if s.Project == "" {
		context += " · project unidentified"
	} else if s.ProjectSource == "Working directory (inferred)" {
		context += " · folder inferred"
	}
	if s.ProjectPath != "" {
		context += " · " + s.ProjectPath
	}
	return context
}
