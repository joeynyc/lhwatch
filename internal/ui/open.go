package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/joeynyc/lhwatch/internal/domain"
)

// Opening services in a browser and managing their SSH tunnels.

func forwardKey(s domain.Service) string { return s.Host + "\x00" + s.Endpoint() }

// openTarget picks the endpoint a browser most likely wants: an explicit web
// label, then any labeled purpose, then the lowest port, preferring IPv4.
func openTarget(g serviceGroup) domain.Service {
	best, bestScore := g.endpoints[0], -1
	for _, s := range g.endpoints {
		score := 0
		if label := strings.ToLower(strings.TrimSpace(s.EndpointLabel)); strings.Contains(label, "web") {
			score = 4
		} else if label != "" {
			score = 2
		}
		if s.Family == "IPv4" {
			score++
		}
		if score > bestScore {
			best, bestScore = s, score
		}
	}
	return best
}

// activeForward reports the live tunnel or direct URL last opened for g.
func (m Model) activeForward(g serviceGroup) (domain.Forward, bool) {
	if m.forwarder == nil || len(g.endpoints) == 0 {
		return domain.Forward{}, false
	}
	f, ok := m.forwards[forwardKey(openTarget(g))]
	if !ok || f.Direct {
		return f, ok
	}
	for _, active := range m.forwarder.Active() {
		if active.Host == f.Host && active.Remote == f.Remote {
			return active, true
		}
	}
	return domain.Forward{}, false
}

func (m *Model) openSelected() tea.Cmd {
	if m.forwarder == nil || m.opening {
		return nil
	}
	g, ok := m.current()
	if !ok {
		return nil
	}
	target := openTarget(g)
	for _, h := range m.hosts {
		if h.host.Name != target.Host {
			continue
		}
		host := h.host
		m.opening = true
		if host.IsLocal() {
			m.notice = "Opening " + clean(target.Endpoint()) + "…"
		} else {
			m.notice = fmt.Sprintf("Opening an SSH tunnel to %s · %s…", clean(host.Name), clean(target.Endpoint()))
		}
		forwarder, ctx := m.forwarder, m.ctx
		return func() tea.Msg {
			f, err := forwarder.Open(ctx, host, target)
			msg := openMsg{key: forwardKey(target), forward: f, err: err}
			if err == nil {
				msg.browseErr = forwarder.Browse(f.URL)
			}
			return msg
		}
	}
	return nil
}

func (m *Model) closeSelectedForward() tea.Cmd {
	g, ok := m.current()
	if !ok {
		return nil
	}
	f, ok := m.activeForward(g)
	if !ok || f.Direct {
		m.notice = "No tunnel is open for this service."
		return nil
	}
	delete(m.forwards, forwardKey(openTarget(g)))
	m.notice = "Closing tunnel " + clean(f.URL) + "…"
	forwarder := m.forwarder
	return func() tea.Msg { return closeForwardMsg{forward: f, err: forwarder.CloseForward(f)} }
}

func describeForward(f domain.Forward) string {
	if f.Direct {
		return clean(f.URL)
	}
	return fmt.Sprintf("%s → %s %s", clean(f.URL), clean(f.Host), clean(f.Remote))
}
