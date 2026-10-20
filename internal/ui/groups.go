package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joeynyc/lhwatch/internal/domain"
)

// Display grouping, filtering, and selection of observed endpoints.

// serviceGroup is a display grouping only. A stop still targets one exact
// endpoint and process incarnation from its observed endpoints.
type serviceGroup struct {
	service      domain.Service
	endpoints    []domain.Service
	selectionKey string
}

func (g serviceGroup) key() string {
	if g.selectionKey != "" {
		return g.selectionKey
	}
	return g.service.GroupKey()
}

func hasContainerEvidence(s domain.Service) bool {
	return s.ContainerID != "" || s.ContainerName != "" || s.ContainerImage != "" || strings.HasPrefix(s.ProjectSource, "Docker")
}

func (g serviceGroup) container() bool {
	for _, s := range g.endpoints {
		if hasContainerEvidence(s) {
			return true
		}
	}
	return false
}

func (g serviceGroup) unidentified() bool {
	return g.service.DisplayName() == domain.UnidentifiedServiceName
}

func (g serviceGroup) ports() string {
	ports := []string{}
	previous := -1
	for _, s := range g.endpoints {
		if s.Port != previous {
			ports = append(ports, fmt.Sprint(s.Port))
			previous = s.Port
		}
	}
	return strings.Join(ports, ", ")
}

func groupServices(services []domain.Service) []serviceGroup {
	groups := []serviceGroup{}
	indexes := map[string]int{}
	unverified := map[string]int{}
	for _, s := range services {
		key := s.GroupKey()
		if s.ContainerID == "" && (s.PID <= 0 || s.Identity == "") {
			// Equal endpoint tuples do not prove equal owners (SO_REUSEPORT).
			occurrence := unverified[key]
			unverified[key]++
			key = fmt.Sprintf("%s\x00observation:%d", key, occurrence)
		}
		index, exists := indexes[key]
		if !exists {
			index = len(groups)
			indexes[key] = index
			groups = append(groups, serviceGroup{selectionKey: key})
		}
		groups[index].endpoints = append(groups[index].endpoints, s)
	}
	for i := range groups {
		g := &groups[i]
		sort.SliceStable(g.endpoints, func(a, b int) bool {
			x, y := g.endpoints[a], g.endpoints[b]
			if x.Port != y.Port {
				return x.Port < y.Port
			}
			return x.Key() < y.Key()
		})
		g.service = g.endpoints[0]
		// Prefer available attribution while preserving the exact observed endpoint.
		for _, s := range g.endpoints {
			if (g.service.Project == "" && s.Project != "") || (g.unidentified() && s.DisplayName() != domain.UnidentifiedServiceName) {
				g.service = s
			}
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.unidentified() != b.unidentified() {
			return !a.unidentified()
		}
		if a.service.Host != b.service.Host {
			return a.service.Host < b.service.Host
		}
		if a.endpoints[0].Port != b.endpoints[0].Port {
			return a.endpoints[0].Port < b.endpoints[0].Port
		}
		return a.key() < b.key()
	})
	return groups
}

// groupCache keeps grouping and search text between renders. It is rebuilt
// only when observations, the search, or the host filter change. Model copies
// share it; Bubble Tea calls Update and View on one goroutine.
type groupCache struct {
	version    uint64
	filter     string
	hostFilter int
	valid      bool
	hosts      [][]serviceGroup // per host, unfiltered, for counts
	all        []serviceGroup   // host filter applied, before search
	search     [][]string       // lowercase searchable text per endpoint of all
	rows       []serviceGroup   // all, narrowed by the search
	builds     int              // number of rebuilds, for tests
}

// invalidate marks observations as changed; the next read regroups.
func (m *Model) invalidate() { m.dataVersion++ }

func (m Model) ensureGroups() *groupCache {
	c := m.cache
	if c == nil { // a zero Model, as in some tests
		c = &groupCache{}
	}
	if c.valid && c.version == m.dataVersion && c.hostFilter == m.hostFilter && c.filter == m.filter {
		return c
	}
	if !c.valid || c.version != m.dataVersion || c.hostFilter != m.hostFilter {
		c.hosts = make([][]serviceGroup, len(m.hosts))
		var endpoints []domain.Service
		for i, h := range m.hosts {
			c.hosts[i] = groupServices(h.snapshot.Services)
			if m.hostFilter < 0 || i == m.hostFilter {
				endpoints = append(endpoints, h.snapshot.Services...)
			}
		}
		c.all = groupServices(endpoints)
		c.search = make([][]string, len(c.all))
		for i, g := range c.all {
			for _, s := range g.endpoints {
				text := fmt.Sprintf("%s %s %s %s %s %s %d %d %s %s %s %s %s %s", s.Host, s.Name, s.Command, s.Cwd, s.User, s.Address, s.Port, s.PID, s.Family, s.Project, s.ProjectPath, s.ContainerID, s.ContainerName, s.ContainerImage)
				c.search[i] = append(c.search[i], strings.ToLower(clean(text)))
			}
		}
		c.builds++
	}
	c.rows = c.all
	if m.filter != "" {
		// A query matches within one endpoint; a hit keeps the whole group.
		query := strings.ToLower(m.filter)
		c.rows = []serviceGroup{}
		for i, g := range c.all {
			for _, text := range c.search[i] {
				if strings.Contains(text, query) {
					c.rows = append(c.rows, g)
					break
				}
			}
		}
	}
	c.version, c.hostFilter, c.filter, c.valid = m.dataVersion, m.hostFilter, m.filter, true
	return c
}

// groups returns the visible rows: host filter, then search.
func (m Model) groups() []serviceGroup { return m.ensureGroups().rows }

// hostGroups returns host i's unfiltered groups, for counts and the sidebar.
func (m Model) hostGroups(i int) []serviceGroup { return m.ensureGroups().hosts[i] }

// reconcile regroups after observations, search, or host filter change and
// keeps the selection on the same group where it still exists.
func (m *Model) reconcile() {
	m.invalidate()
	defer m.syncListTop()
	rows := m.groups()
	for i, g := range rows {
		if g.key() == m.selected {
			m.selectionIndex = i
			return
		}
	}
	if len(rows) == 0 {
		m.selected = ""
		m.selectionIndex = 0
		return
	}
	m.selectionIndex = min(max(m.selectionIndex, 0), len(rows)-1)
	m.selected = rows[m.selectionIndex].key()
}

func (m Model) current() (serviceGroup, bool) {
	rows := m.groups()
	for _, g := range rows {
		if g.key() == m.selected {
			return g, true
		}
	}
	if len(rows) > 0 {
		return rows[min(m.selectionIndex, len(rows)-1)], true
	}
	return serviceGroup{}, false
}

func (m *Model) move(delta int) {
	rows := m.groups()
	if len(rows) == 0 {
		return
	}
	m.selectionIndex = min(max(m.selectionIndex+delta, 0), len(rows)-1)
	m.selected = rows[m.selectionIndex].key()
	m.syncListTop()
}
