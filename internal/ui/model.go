// Package ui implements the localhost radar terminal dashboard.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/joeynyc/lhwatch/internal/domain"
)

type hostState struct {
	host       domain.Host
	snapshot   domain.Snapshot
	err        string
	observed   time.Time
	inflight   bool
	generation uint64
}

type scanMsg struct {
	index      int
	generation uint64
	snapshot   domain.Snapshot
	err        error
}

type refreshMsg struct {
	index      int
	generation uint64
}

type clockMsg time.Time

type stopMsg struct {
	result domain.StopResult
	err    error
	host   string
	pid    int
}

type openMsg struct {
	key       string
	forward   domain.Forward
	err       error
	browseErr error
}

type closeForwardMsg struct {
	forward domain.Forward
	err     error
}

type confirmation struct {
	host      domain.Host
	service   domain.Service
	endpoints []string
}

// Model is the dashboard's Bubble Tea model. All backend work runs in commands.
type Model struct {
	ctx                  context.Context
	backend              domain.Backend
	forwarder            domain.Forwarder
	forwards             map[string]domain.Forward // forwardKey → last opened
	opening              bool
	hosts                []hostState
	interval             time.Duration
	demo                 bool
	width, height        int
	now                  time.Time
	filter               string
	searching            bool
	hostFilter           int // -1 means all hosts
	selected             string
	selectionIndex       int
	detail, help, errors bool
	scroll               int
	listTop              int // first visible table group, kept across renders
	noMouse              bool
	cache                *groupCache
	dataVersion          uint64 // bumped whenever observations may have changed
	confirm              *confirmation
	stopping             bool
	notice               string
}

func New(ctx context.Context, hosts []domain.Host, backend domain.Backend, interval time.Duration, demo bool) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = 3 * time.Second
	}
	var forwarder domain.Forwarder
	if demo {
		hosts = DemoHosts()
		backend = NewDemoBackend()
		forwarder = newDemoForwarder()
	}
	m := Model{ctx: ctx, backend: backend, forwarder: forwarder, forwards: map[string]domain.Forward{}, interval: interval, demo: demo, hostFilter: -1, now: time.Now(), cache: &groupCache{}}
	for _, h := range hosts {
		m.hosts = append(m.hosts, hostState{host: h, inflight: true, generation: 1})
	}
	return m
}

// WithForwarder enables opening services in a browser. Demo mode keeps its
// simulated forwarder.
// WithoutMouse leaves the mouse to the terminal, so plain drags select text.
func (m Model) WithoutMouse() Model {
	m.noMouse = true
	return m
}

func (m Model) WithForwarder(f domain.Forwarder) Model {
	if !m.demo {
		m.forwarder = f
	}
	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick()}
	if !m.noMouse {
		cmds = append(cmds, tea.Raw(ArrowPointer))
	}
	for i := range m.hosts {
		cmds = append(cmds, m.scan(i))
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return clockMsg(t) }) }

func (m Model) scan(i int) tea.Cmd {
	h, gen, backend, ctx := m.hosts[i].host, m.hosts[i].generation, m.backend, m.ctx
	return func() tea.Msg {
		if backend == nil {
			return scanMsg{index: i, generation: gen, err: fmt.Errorf("collector unavailable")}
		}
		s, err := backend.Scan(ctx, h)
		return scanMsg{index: i, generation: gen, snapshot: s, err: err}
	}
}

func (m *Model) refresh(i int) tea.Cmd {
	if m.hosts[i].inflight {
		return nil
	}
	m.hosts[i].inflight = true
	m.hosts[i].generation++
	return m.scan(i)
}

func (m Model) stale(h hostState) bool {
	return h.err != "" || h.observed.IsZero() || m.now.Sub(h.observed) > max(3*m.interval, 10*time.Second)
}

func (m Model) usableViewport() bool { return m.width >= 55 && m.height >= 16 }

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if !m.usableViewport() {
			m.confirm = nil
			m.scroll = 0
		}
		m.scroll = min(m.scroll, m.maxScroll())
		m.syncListTop()
	case clockMsg:
		m.now = time.Time(msg)
		return m, tick()
	case scanMsg:
		if msg.index < 0 || msg.index >= len(m.hosts) {
			return m, nil
		}
		h := &m.hosts[msg.index]
		if msg.generation != h.generation || !h.inflight {
			return m, nil
		}
		h.inflight = false
		if msg.err != nil {
			h.err = clean(msg.err.Error())
		} else if msg.snapshot.Error != "" {
			h.err = clean(msg.snapshot.Error)
		} else {
			h.snapshot = msg.snapshot
			h.err = ""
			// Freshness follows local receipt time; remote clocks may be skewed.
			h.observed = time.Now()
			// Routing uses the configured host, never a collector-supplied label.
			for i := range h.snapshot.Services {
				h.snapshot.Services[i].Host = h.host.Name
			}
		}
		m.now = time.Now()
		m.reconcile()
		i, gen := msg.index, h.generation
		return m, tea.Tick(m.interval, func(time.Time) tea.Msg { return refreshMsg{i, gen} })
	case refreshMsg:
		if msg.index >= 0 && msg.index < len(m.hosts) && m.hosts[msg.index].generation == msg.generation {
			return m, m.refresh(msg.index)
		}
	case openMsg:
		m.opening = false
		if msg.err != nil {
			m.notice = "Open failed: " + clean(msg.err.Error())
			return m, nil
		}
		m.forwards[msg.key] = msg.forward
		switch {
		case m.demo:
			m.notice = "Demo · simulated " + describeForward(msg.forward) + " · no browser opened"
		case msg.browseErr == nil:
			m.notice = "Opened " + describeForward(msg.forward)
		default:
			// No usable browser here (for example over SSH); hand the URL to the
			// terminal's clipboard where it supports OSC 52.
			m.notice = "Ready at " + describeForward(msg.forward) + " · " + clean(msg.browseErr.Error()) + "; URL sent to the terminal clipboard"
			return m, tea.SetClipboard(msg.forward.URL)
		}
	case closeForwardMsg:
		if msg.err != nil {
			m.notice = "Tunnel close failed: " + clean(msg.err.Error())
		} else {
			m.notice = "Closed tunnel " + describeForward(msg.forward)
		}
	case stopMsg:
		m.stopping = false
		if msg.err != nil {
			m.notice = fmt.Sprintf("Stop outcome unknown · %s · PID %d: %s. Refreshing observations.", clean(msg.host), msg.pid, clean(msg.err.Error()))
		} else if msg.result.Sent {
			m.notice = fmt.Sprintf("SIGTERM requested · %s · PID %d. Awaiting a fresh observation.", clean(msg.host), msg.pid)
		} else {
			m.notice = "No signal sent: " + clean(msg.result.Message)
		}
		var cmds []tea.Cmd
		for i := range m.hosts {
			if m.hosts[i].host.Name == msg.host {
				cmds = append(cmds, m.refresh(i))
			}
		}
		return m, tea.Batch(cmds...)
	case tea.MouseClickMsg:
		return m.updateClick(tea.Mouse(msg))
	case tea.MouseWheelMsg:
		return m.updateWheel(tea.Mouse(msg))
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

// clean keeps collector-controlled text inert, including OSC, C1, and bidi controls.
func clean(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) || r == 0x2028 || r == 0x2029 {
			return -1
		}
		return r
	}, s)
}
