package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/joeynyc/lhwatch/internal/domain"
)

// locate finds text on the rendered screen inside region (whole screen when
// region is zero) and returns its cell position, so tests click exactly
// where things are drawn.
func locate(t *testing.T, m Model, text string, region rect) (int, int) {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		offset := 0
		for {
			i := strings.Index(line[offset:], text)
			if i < 0 {
				break
			}
			x := ansi.StringWidth(line[:offset+i])
			if region.w == 0 || region.contains(x, y) {
				return x, y
			}
			offset += i + len(text)
		}
	}
	t.Fatalf("%q not on screen in %+v:\n%s", text, region, ansi.Strip(m.View().Content))
	return 0, 0
}

func click(x, y int) tea.MouseClickMsg { return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft} }
func wheel(button tea.MouseButton) tea.MouseWheelMsg {
	return tea.MouseWheelMsg{X: 40, Y: 10, Button: button}
}
func sized(t *testing.T, m Model, w, h int) Model {
	t.Helper()
	m, _ = apply(m, tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func TestClickSelectsThenOpensDetailsInEveryLayout(t *testing.T) {
	for _, size := range [][2]int{{140, 32}, {90, 30}, {60, 26}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := sized(t, ready(t, &fakeBackend{}), size[0], size[1])
			table := m.layout().table
			x, y := locate(t, m, "5432", table)
			m, _ = apply(m, click(x, y))
			if g, _ := m.current(); g.service.Name != "db" || m.detail {
				t.Fatalf("click should select db only, got %q detail=%v", g.service.Name, m.detail)
			}
			// The context line under the name belongs to the same row.
			m, _ = apply(m, click(table.x+3, y+1))
			if !m.detail {
				t.Fatal("clicking the selected row should open its details")
			}
		})
	}
}

func TestClickingRowsKeepsTheListWindowStill(t *testing.T) {
	var services []domain.Service
	for i := range 30 {
		services = append(services, service("local", fmt.Sprintf("svc%02d", i), 100+i, 20000+i))
	}
	m := New(context.Background(), []domain.Host{{Name: "local"}}, &fakeBackend{}, time.Second, false)
	m = sized(t, m, 90, 24)
	m, _ = apply(m, scanMsg{index: 0, generation: 1, snapshot: domain.Snapshot{CollectedAt: time.Now(), Services: services}})
	for range 20 {
		m, _ = apply(m, key("j"))
	}
	top := m.listTop
	if top == 0 {
		t.Fatal("expected the window to have scrolled")
	}
	x, y := locate(t, m, fmt.Sprint(20000+top+1), m.layout().table)
	m, _ = apply(m, click(x, y))
	if g, _ := m.current(); g.service.Port != 20000+top+1 || m.listTop != top {
		t.Fatalf("clicked %d, selected %d, window %d→%d", 20000+top+1, g.service.Port, top, m.listTop)
	}
	// Moving back up within the window no longer drags it.
	m, _ = apply(m, key("k"))
	if m.listTop != top {
		t.Fatalf("window moved from %d to %d", top, m.listTop)
	}
}

func TestClickHostInSidebarFilters(t *testing.T) {
	m := sized(t, ready(t, &fakeBackend{}), 140, 32)
	side := m.layout().sidebar
	x, y := locate(t, m, "remote", side)
	m, _ = apply(m, click(x, y))
	if m.hostFilter != 1 || len(m.groups()) != 1 {
		t.Fatalf("host filter %d, %d rows", m.hostFilter, len(m.groups()))
	}
	x, y = locate(t, m, "All hosts", side)
	m, _ = apply(m, click(x, y))
	if m.hostFilter != -1 || len(m.groups()) != 3 {
		t.Fatalf("all hosts not restored: %d", m.hostFilter)
	}
}

func TestFooterClicksActLikeKeysButNeverConfirmAStop(t *testing.T) {
	backend := &fakeBackend{}
	m := sized(t, ready(t, backend), 140, 40) // tall enough to show the whole confirmation
	footer := rect{0, m.footerRow(), m.width, 1}
	x, y := locate(t, m, "? help", footer)
	m, _ = apply(m, click(x, y))
	if !m.help {
		t.Fatal("footer help click did nothing")
	}
	m, _ = apply(m, click(5, 1)) // the header closes a panel
	if m.help {
		t.Fatal("clicking the header should close the panel")
	}

	x, y = locate(t, m, "x stop", footer)
	m, _ = apply(m, click(x, y))
	if m.confirm == nil {
		t.Fatal("footer x should open the confirmation")
	}
	for _, target := range []string{"y send SIGTERM", "Send SIGTERM", "y: send SIGTERM"} {
		x, y = locate(t, m, target, rect{})
		var cmd tea.Cmd
		m, cmd = apply(m, click(x, y))
		if cmd != nil || m.confirm == nil || backend.stops != 0 {
			t.Fatalf("clicking %q must not send a signal", target)
		}
	}
	x, y = locate(t, m, "Enter / Esc cancel", footer)
	m, _ = apply(m, click(x, y))
	if m.confirm != nil || backend.stops != 0 {
		t.Fatal("cancel click should close the confirmation without stopping")
	}
}

func TestSearchBarClickAndWheel(t *testing.T) {
	m := sized(t, ready(t, &fakeBackend{}), 120, 30)
	x, y := locate(t, m, "Search projects", rect{})
	m, _ = apply(m, click(x, y))
	if !m.searching {
		t.Fatal("search bar click should start a search")
	}
	m, _ = apply(m, key("d"))
	m, _ = apply(m, key("b"))
	x, y = locate(t, m, "5432", m.layout().table)
	m, _ = apply(m, click(x, y))
	// The only match was already selected, so this click also opens it.
	if m.searching || m.filter != "db" || !m.detail {
		t.Fatalf("clicking a row should keep the query and leave search: searching=%v filter=%q detail=%v", m.searching, m.filter, m.detail)
	}
	m, _ = apply(m, key("esc")) // close details
	m, _ = apply(m, key("esc")) // clear the search
	m, _ = apply(m, key("g"))

	start := m.selectionIndex
	m, _ = apply(m, wheel(tea.MouseWheelDown))
	if m.selectionIndex != start+1 {
		t.Fatal("wheel down should move the selection")
	}
	m, _ = apply(m, key("?"))
	m, _ = apply(m, wheel(tea.MouseWheelDown))
	if m.scroll != min(3, m.maxScroll()) || m.scroll == 0 {
		t.Fatalf("wheel should scroll the panel, scroll=%d", m.scroll)
	}
	m, _ = apply(m, wheel(tea.MouseWheelUp))
	if m.scroll != 0 {
		t.Fatalf("wheel up should scroll back, scroll=%d", m.scroll)
	}
}

func TestMouseModeAndNoMouse(t *testing.T) {
	m := sized(t, ready(t, &fakeBackend{}), 120, 30)
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("mouse should be enabled by default")
	}
	if off := m.WithoutMouse(); off.View().MouseMode != tea.MouseModeNone {
		t.Fatal("--no-mouse must leave the mouse to the terminal")
	}
	for _, name := range []string{"enter", "esc", "tab", "ctrl+u", "o", "?", "/"} {
		if got := keyPress(name).String(); got != name {
			t.Fatalf("keyPress(%q) reads as %q", name, got)
		}
	}
}

func TestGroupingIsCachedBetweenRenders(t *testing.T) {
	m := sized(t, ready(t, &fakeBackend{}), 140, 32)
	builds := m.cache.builds
	for range 5 {
		m.View()
		m.current()
	}
	m, _ = apply(m, key("j"))
	m, _ = apply(m, wheel(tea.MouseWheelDown))
	if m.cache.builds != builds {
		t.Fatalf("renders and navigation regrouped %d times", m.cache.builds-builds)
	}
	m, _ = apply(m, key("/"))
	m, _ = apply(m, key("a"))
	m, _ = apply(m, key("p"))
	m, _ = apply(m, key("i"))
	if len(m.groups()) != 1 {
		t.Fatalf("search should narrow to api, got %d rows", len(m.groups()))
	}
	m, _ = apply(m, key("esc"))
	before := m.cache.builds
	m.hosts[1].generation, m.hosts[1].inflight = 2, true
	m, _ = apply(m, scanMsg{index: 1, generation: 2, snapshot: domain.Snapshot{CollectedAt: time.Now(), Services: []domain.Service{service("remote", "cache", 44, 6379)}}})
	if m.cache.builds == before {
		t.Fatal("a new scan must regroup")
	}
	if _, y := locate(t, m, "6379", m.layout().table); y == 0 {
		t.Fatal("new observation not rendered")
	}
}

func TestArrowPointerOnlyWithMouse(t *testing.T) {
	sends := func(m Model) bool {
		var found func(tea.Msg) bool
		found = func(msg tea.Msg) bool {
			switch msg := msg.(type) {
			case tea.RawMsg:
				return msg.Msg == ArrowPointer
			case tea.BatchMsg:
				for _, cmd := range msg {
					// Skip scans and ticks, which would block or wait.
					if cmd != nil {
						if raw, ok := cmd().(tea.RawMsg); ok && found(raw) {
							return true
						}
					}
				}
			}
			return false
		}
		return found(m.Init()())
	}
	m := New(context.Background(), nil, &fakeBackend{}, time.Second, false)
	if !sends(m) {
		t.Fatal("mouse mode should request the arrow pointer")
	}
	if sends(m.WithoutMouse()) {
		t.Fatal("--no-mouse must leave the pointer alone")
	}
	if ArrowPointer != "\x1b]22;default\x1b\\" || ResetPointer != "\x1b]22;\x1b\\" {
		t.Fatal("unexpected OSC 22 sequences")
	}
}
