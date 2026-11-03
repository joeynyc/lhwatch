package ui

import (
	"sort"

	"github.com/joeynyc/lhwatch/internal/domain"
)

// Stop eligibility and the stop confirmation.

// latestTarget finds a frozen stop target in the host's latest observation,
// with every endpoint of that process incarnation, so a confirmation is judged
// on current evidence. It reports false when the exact endpoint is gone.
func (m Model) latestTarget(target domain.Service) (serviceGroup, bool) {
	for _, h := range m.hosts {
		if h.host.Name != target.Host {
			continue
		}
		g := serviceGroup{}
		for _, s := range h.snapshot.Services {
			if s.Key() == target.Key() {
				g.service = s
			}
			if s.PID == target.PID && s.Identity == target.Identity {
				g.endpoints = append(g.endpoints, s)
			}
		}
		return g, g.service.Key() == target.Key()
	}
	return serviceGroup{}, false
}

func (m Model) stopUnavailable(g serviceGroup) string {
	if g.container() {
		return "Use Docker to stop this container."
	}
	for _, h := range m.hosts {
		if h.host.Name != g.service.Host {
			continue
		}
		if m.stale(h) {
			return "This host needs a fresh observation. Press r to refresh."
		}
		for _, target := range g.endpoints {
			if target.PID <= 0 || target.Identity == "" {
				continue
			}
			for _, sibling := range h.snapshot.Services {
				if sibling.PID == target.PID && sibling.Identity == target.Identity && hasContainerEvidence(sibling) {
					return "This process also serves a Docker container. Use Docker to stop it."
				}
			}
		}
	}
	for _, s := range g.endpoints {
		if !s.CanStop || s.Identity == "" || s.PID <= 0 {
			if s.StopReason != "" {
				return clean(s.StopReason)
			}
			return "Process identity or ownership could not be verified."
		}
	}
	return ""
}

func (m *Model) openStop() {
	if !m.usableViewport() {
		return
	}
	g, ok := m.current()
	if !ok {
		return
	}
	if reason := m.stopUnavailable(g); reason != "" {
		m.notice = "Stop unavailable: " + reason
		return
	}
	s := g.service
	for _, h := range m.hosts {
		if h.host.Name == s.Host {
			c := &confirmation{host: h.host, service: s}
			for _, other := range g.endpoints {
				c.endpoints = append(c.endpoints, clean(other.Endpoint())+"  "+clean(other.Family))
			}
			sort.Strings(c.endpoints)
			m.confirm = c
			m.scroll = 0
			return
		}
	}
}
