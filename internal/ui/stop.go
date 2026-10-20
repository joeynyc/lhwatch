package ui

import "sort"

// Stop eligibility and the stop confirmation.

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
