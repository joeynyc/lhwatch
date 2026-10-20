package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/joeynyc/lhwatch/internal/domain"
)

// Service details: the full panel and the wide-layout preview.

func wrapField(label, value string, w int) []string {
	if value == "" {
		value = "unavailable"
	}
	return append([]string{dim(label)}, strings.Split(ansi.Wrap(clean(value), max(1, w), ""), "\n")...)
}

func processDescription(s domain.Service) string {
	if s.PID <= 0 {
		return "unavailable"
	}
	name := s.Name
	if name == "" || name == "unknown" {
		name = "name unavailable"
	}
	return fmt.Sprintf("%s / %d", name, s.PID)
}

func nameSource(g serviceGroup) string {
	if g.service.ProjectSource != "" {
		return g.service.ProjectSource
	}
	if g.unidentified() {
		return "Project and process name unavailable"
	}
	return "Process name; project unidentified"
}

func visibilityReason(g serviceGroup) string {
	if g.service.VisibilityReason != "" {
		return g.service.VisibilityReason
	}
	if g.service.PID <= 0 {
		return "The listener is visible, but its owning process is unavailable."
	}
	if g.service.Project == "" {
		return "No project metadata found for this process."
	}
	return ""
}

// Keep purposes in a left column and wrap both columns independently so even
// long IPv6 endpoints remain complete in the scrollable details panel.
func endpointLines(endpoints []domain.Service, w int) []string {
	labels := make([]string, len(endpoints))
	labelWidth := 0
	for i, endpoint := range endpoints {
		labels[i] = strings.TrimSpace(clean(endpoint.EndpointLabel))
		if labels[i] == "" {
			labels[i] = dim("Purpose unknown")
		} else {
			labels[i] = accent(labels[i])
		}
		labelWidth = max(labelWidth, ansi.StringWidth(labels[i]))
	}
	labelWidth = min(labelWidth, max(1, w/3))
	addressWidth := max(1, w-labelWidth-2)
	lines := []string{}
	for i, endpoint := range endpoints {
		left := strings.Split(ansi.Wrap(labels[i], labelWidth, ""), "\n")
		address := paint(clean(endpoint.Endpoint()), cyan) + " " + dim(clean(endpoint.Family))
		right := strings.Split(ansi.Wrap(address, addressWidth, ""), "\n")
		for row := 0; row < max(len(left), len(right)); row++ {
			purpose, value := "", ""
			if row < len(left) {
				purpose = left[row]
			}
			if row < len(right) {
				value = right[row]
			}
			lines = append(lines, fit(purpose, labelWidth)+"  "+value)
		}
	}
	return lines
}

func (m Model) detailLines(w int) []string {
	g, ok := m.current()
	if !ok {
		return []string{dim("Select a listening service"), "", dim("Service details appear here.")}
	}
	s := g.service
	result := wrapField("PROJECT / SERVICE", s.DisplayName(), w)
	result = append(result, "")
	reason := m.stopUnavailable(g)
	if reason == "" {
		reason = "x opens confirmation for this process. SIGTERM only."
	}
	target := openTarget(g)
	open := "o opens " + target.Endpoint() + " in a browser"
	if s.Host != "" && !m.hostIsLocal(s.Host) {
		open = "o opens " + target.Endpoint() + " through an SSH tunnel"
	}
	if f, ok := m.activeForward(g); ok && !f.Direct {
		open = describeForward(f) + " · tunnel open · O closes it"
	}
	if m.forwarder == nil {
		open = ""
	}
	fields := [][2]string{{"HOST", s.Host}, {"STOP PROCESS", reason}}
	if open != "" {
		fields = append(fields, [2]string{"OPEN", open})
	}
	for _, field := range fields {
		result = append(result, wrapField(field[0], field[1], w)...)
		result = append(result, "")
	}
	if reason := visibilityReason(g); reason != "" {
		result = append(result, wrapField("DETAILS AVAILABLE", reason, w)...)
		result = append(result, "")
	}
	result = append(result, dim("ALL ENDPOINTS FOR THIS SERVICE"))
	result = append(result, endpointLines(g.endpoints, w)...)
	result = append(result, "")
	sources := []string{}
	seenSources := map[string]bool{}
	for _, ep := range g.endpoints {
		if strings.TrimSpace(clean(ep.EndpointLabel)) == "" || strings.TrimSpace(clean(ep.EndpointLabelSource)) == "" {
			continue
		}
		source := fmt.Sprintf("%d · %s", ep.Port, strings.TrimSpace(clean(ep.EndpointLabelSource)))
		if !seenSources[source] {
			sources = append(sources, strings.Split(ansi.Wrap(dim(source), max(1, w), ""), "\n")...)
			seenSources[source] = true
		}
	}
	if len(sources) > 0 {
		result = append(result, dim("PURPOSE SOURCES"))
		result = append(result, sources...)
		result = append(result, "")
	}
	processes := []string{}
	seenProcesses := map[string]bool{}
	for _, endpoint := range g.endpoints {
		description := processDescription(endpoint)
		if !seenProcesses[description] {
			processes = append(processes, description)
			seenProcesses[description] = true
		}
	}
	fields = [][2]string{{"PROCESS / PID", strings.Join(processes, "; ")}, {"NAME SOURCE", nameSource(g)}}
	if g.container() {
		fields = append(fields, [2]string{"CONTAINER", s.ContainerName}, [2]string{"IMAGE", s.ContainerImage}, [2]string{"CONTAINER ID", s.ContainerID})
	}
	fields = append(fields, [][2]string{{"PROJECT FOLDER", s.ProjectPath}, {"USER", s.User}, {"WORKING DIRECTORY", s.Cwd}, {"COMMAND", s.Command}, {"STARTED", s.Started}}...)
	for _, field := range fields {
		result = append(result, wrapField(field[0], field[1], w)...)
		result = append(result, "")
	}
	for _, host := range m.hosts {
		if host.host.Name == s.Host {
			result = append(result, dim("OBSERVATION"), m.status(host), dim(age(host.observed, m.now)))
		}
	}
	return result
}

func (m Model) hostIsLocal(name string) bool {
	for _, h := range m.hosts {
		if h.host.Name == name {
			return h.host.IsLocal()
		}
	}
	return false
}

func (m Model) detailPreview(w, h int) []string {
	g, ok := m.current()
	if !ok {
		return []string{dim("Select a listening service")}
	}
	s := g.service
	lines := strings.Split(ansi.Wrap(accent(clean(s.DisplayName())), max(1, w), ""), "\n")
	lines = append(lines, strings.Split(ansi.Wrap(paint(clean(s.Host)+" · "+g.ports(), cyan), max(1, w), ""), "\n")...)
	lines = append(lines, "")
	lines = append(lines, wrapField("NAME SOURCE", nameSource(g), w)...)
	lines = append(lines, "")
	reason := m.stopUnavailable(g)
	if reason == "" {
		reason = "x · confirm process stop"
	}
	lines = append(lines, wrapField("STOP PROCESS", reason, w)...)
	if reason := visibilityReason(g); reason != "" {
		lines = append(lines, "")
		lines = append(lines, wrapField("DETAILS AVAILABLE", reason, w)...)
	}
	if len(lines) > h-2 {
		lines = lines[:max(0, h-2)]
	}
	for len(lines) < h-1 {
		lines = append(lines, "")
	}
	return append(lines, dim("Enter → all endpoints"))
}
