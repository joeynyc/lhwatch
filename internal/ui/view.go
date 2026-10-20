package ui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	bg    = lg.Color("#101722")
	panel = lg.Color("#141F2C")
	ink   = lg.Color("#DCE6EE")
	muted = lg.Color("#8193A8")
	mint  = lg.Color("#83E9C0")
	cyan  = lg.Color("#79CFE6")
	amber = lg.Color("#EDBF7B")
	line  = lg.Color("#304052")
)

func paint(s string, c color.Color) string { return lg.NewStyle().Foreground(c).Render(s) }

func dim(s string) string { return lg.NewStyle().Foreground(muted).Render(s) }

func accent(s string) string { return lg.NewStyle().Foreground(mint).Bold(true).Render(s) }

func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}

func cell(s string, w int) string { return fit(clean(s), w) }

// Nested foreground styles reset SGR attributes. Restore the containing surface
// after those resets so text and padding share one uninterrupted background.
func surface(s string, c color.Color) string {
	bgCode := ansi.Style{}.BackgroundColor(c).String()
	resets := strings.NewReplacer("\x1b[0m", "\x1b[0m"+bgCode, "\x1b[m", "\x1b[m"+bgCode, "\x1b[49m", "\x1b[49m"+bgCode)
	return bgCode + resets.Replace(s) + "\x1b[0m"
}

func age(t, now time.Time) string {
	if t.IsZero() {
		return "not observed"
	}
	d := max(time.Duration(0), now.Sub(t))
	if d < time.Second {
		return "just now"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

func box(title string, content []string, w, h int) []string {
	if w < 4 || h < 2 {
		return nil
	}
	inner := w - 4
	out := []string{lg.NewStyle().Foreground(line).Render("╭─ ") + accent(fit(title, min(ansi.StringWidth(title), w-6))) + lg.NewStyle().Foreground(line).Render(strings.Repeat("─", max(0, w-4-min(ansi.StringWidth(title), w-6)))+"╮")}
	for i := 0; i < h-2; i++ {
		s := ""
		if i < len(content) {
			s = content[i]
		}
		out = append(out, lg.NewStyle().Foreground(line).Render("│")+surface(" "+fit(s, inner)+" ", panel)+lg.NewStyle().Foreground(line).Render("│"))
	}
	out = append(out, lg.NewStyle().Foreground(line).Render("╰"+strings.Repeat("─", w-2)+"╯"))
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (m Model) View() tea.View {
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		v := tea.NewView("Initializing localhost radar…")
		v.AltScreen = true
		if !m.noMouse {
			v.MouseMode = tea.MouseModeCellMotion
		}
		return v
	}
	var lines []string
	if !m.usableViewport() {
		lines = []string{accent("lhwatch"), "", dim("LOCALHOST RADAR"), "", "Resize to at least 55 × 16", "to explore listening services.", "", dim("q quit · r refresh")}
	} else {
		inner := w - 4
		brand := accent("lhwatch") + "  " + dim("/  LOCALHOST RADAR")
		mode := dim("LIVE OBSERVATIONS")
		if m.demo {
			mode = paint("DEMO · SIMULATED", amber)
		}
		lines = append(lines, "", "  "+brand+strings.Repeat(" ", max(1, inner-ansi.StringWidth(brand)-ansi.StringWidth(mode)))+mode)
		total, services, unidentified, issues := 0, 0, 0, 0
		for i, host := range m.hosts {
			total += len(host.snapshot.Services)
			groups := m.hostGroups(i)
			services += len(groups)
			for _, g := range groups {
				if g.unidentified() {
					unidentified++
				}
			}
			if host.err != "" || host.snapshot.Partial || m.stale(host) && !host.observed.IsZero() {
				issues++
			}
		}
		summary := fmt.Sprintf("%d services · %d endpoints · %d hosts", services, total, len(m.hosts))
		if w >= 100 {
			summary += " · listening, not health-checked"
		}
		if unidentified > 0 {
			summary += fmt.Sprintf(" · %d unidentified", unidentified)
		}
		if issues > 0 && w >= 120 {
			summary += fmt.Sprintf(" · %d host%s with limited visibility [e]", issues, plural(issues))
		}
		lines = append(lines, "  "+dim(summary), "")
		search := "/  Search projects, ports, paths…"
		if m.filter != "" {
			search = "/  " + clean(m.filter)
		}
		if m.searching {
			search = "/  " + clean(m.filter) + "▏  [Enter applies · Esc clears]"
		}
		lines = append(lines, "  "+lg.NewStyle().Foreground(cyan).Background(panel).Render(fit(" "+search, inner)), "")
		l := m.layout()
		var body []string
		switch {
		case l.panel.w > 0:
			body = m.modal(l.panel.w, l.panel.h)
		case l.preview.w > 0:
			a, b, c := m.sidebar(l.sidebar.w, l.sidebar.h), m.table(l.table.w, l.table.h), box("PROJECT DETAILS", m.detailPreview(l.preview.w-4, l.preview.h-2), l.preview.w, l.preview.h)
			for i := range l.table.h {
				body = append(body, a[i]+" "+b[i]+" "+c[i])
			}
		case l.sidebar.w > 0:
			a, b := m.sidebar(l.sidebar.w, l.sidebar.h), m.table(l.table.w, l.table.h)
			for i := range l.table.h {
				body = append(body, a[i]+" "+b[i])
			}
		default:
			body = m.table(l.table.w, l.table.h)
		}
		for _, l := range body {
			lines = append(lines, "  "+l)
		}
		note := m.notice
		if note == "" {
			note = "Service observations · refresh " + m.interval.String() + " · Enter for full details"
		}
		lines = append(lines, "  "+dim(clean(note)))
		lines = append(lines, "  "+dim(m.footerText()))
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for i := range lines {
		lines[i] = surface(lg.NewStyle().Foreground(ink).Render(fit(lines[i], w)), bg)
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.BackgroundColor = bg
	v.ForegroundColor = ink
	v.WindowTitle = "lhwatch · localhost radar"
	if !m.noMouse {
		// Clicks, releases, wheel, and drags only; idle movement sends nothing.
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}
