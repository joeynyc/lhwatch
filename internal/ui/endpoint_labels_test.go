package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/joeynyc/lhwatch/internal/domain"
)

func endpointDetailsModel(t *testing.T, endpoints ...domain.Service) Model {
	t.Helper()
	m := ready(t, &fakeBackend{})
	m.hosts[0].snapshot.Services = endpoints
	m.hosts[1].snapshot.Services = nil
	m.reconcile()
	return m
}

func endpointSection(t *testing.T, m Model, width int) []string {
	t.Helper()
	lines := m.detailLines(width)
	for i, line := range lines {
		if ansi.Strip(line) != "ALL ENDPOINTS FOR THIS SERVICE" {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if lines[j] == "" {
				return lines[i+1 : j]
			}
		}
	}
	t.Fatal("missing endpoint section")
	return nil
}

func TestEndpointPurposesAppearLeftOfAlignedAddresses(t *testing.T) {
	api := service("local", "web-ui", 11, 8000)
	api.EndpointLabel, api.EndpointLabelSource = "Model API", "Explicit .lhwatch.json port mapping"
	ipv6 := api
	ipv6.Address, ipv6.Family = "::1", "IPv6"
	unknown := api
	unknown.Port, unknown.EndpointLabel, unknown.EndpointLabelSource = 8001, "", ""
	unknown.ContainerImage = "web-ui:latest"
	m := endpointDetailsModel(t, api, ipv6, unknown)
	rows := endpointSection(t, m, 100)
	if len(rows) != 3 {
		t.Fatalf("got %d endpoint lines, want 3", len(rows))
	}
	addressColumn := -1
	for _, row := range rows {
		line := ansi.Strip(row)
		label := "Model API"
		endpoint := "127.0.0.1:8000 IPv4"
		if strings.Contains(line, "[::1]") {
			endpoint = "[::1]:8000 IPv6"
		}
		if strings.Contains(line, ":8001") {
			label, endpoint = "Purpose unknown", "127.0.0.1:8001 IPv4"
		}
		if !strings.HasPrefix(line, label+" ") || !strings.Contains(line, endpoint) {
			t.Fatalf("purpose must precede complete endpoint: %q", line)
		}
		column := strings.Index(line, endpoint)
		if addressColumn >= 0 && column != addressColumn {
			t.Fatalf("endpoint columns differ: %d and %d", addressColumn, column)
		}
		addressColumn = column
	}
	details := ansi.Strip(strings.Join(m.detailLines(100), "\n"))
	if !strings.Contains(details, "PURPOSE SOURCES") || !strings.Contains(details, "8000") || strings.Count(details, api.EndpointLabelSource) != 1 {
		t.Fatalf("purpose evidence should appear once for the dual-stack port:\n%s", details)
	}
}

func TestEndpointPurposeWrappingPreservesReachableAddresses(t *testing.T) {
	api := service("local", "model-server", 11, 65535)
	api.Address, api.Family = "2001:db8:1234:5678:9abc:def0:1234:5678", "IPv6"
	api.EndpointLabel = "Model API"
	unknown := api
	unknown.Address, unknown.Family, unknown.Port, unknown.EndpointLabel = "127.0.0.1", "IPv4", 54321, ""
	for _, size := range [][2]int{{55, 16}, {80, 24}, {120, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := endpointDetailsModel(t, api, unknown)
			m.width, m.height, m.detail = size[0], size[1], true
			rows := endpointSection(t, m, size[0]-8)
			compact := strings.Join(strings.Fields(ansi.Strip(strings.Join(rows, "\n"))), "")
			for _, endpoint := range []domain.Service{api, unknown} {
				if !strings.Contains(compact, endpoint.Endpoint()+endpoint.Family) {
					t.Fatalf("wrapped details lost endpoint %s %s: %q", endpoint.Endpoint(), endpoint.Family, compact)
				}
			}
			seen := strings.Builder{}
			for range len(m.detailLines(size[0]-8)) + 10 {
				view := m.View().Content
				for _, line := range strings.Split(view, "\n") {
					if ansi.StringWidth(line) > size[0] {
						t.Fatalf("rendered view exceeds terminal width: %q", line)
					}
				}
				seen.WriteString(ansi.Strip(view))
				m, _ = apply(m, key("j"))
			}
			for _, row := range rows {
				if ansi.StringWidth(row) > size[0]-8 || !strings.Contains(seen.String(), strings.TrimSpace(ansi.Strip(row))) {
					t.Fatalf("endpoint content is clipped or inaccessible by scrolling: %q", row)
				}
			}
		})
	}
}

func TestEndpointPurposeMetadataIsInert(t *testing.T) {
	api := service("local", "python", 11, 8000)
	api.EndpointLabel = "\x1b]52;c;c2VjcmV0\a\x1b[31mModel\x1b[0m\nAPI\u202e\u2066\x00"
	api.EndpointLabelSource = "\x1b]0;hostile title\aExplicit\tmetadata\u009b\u2028\u2029"
	unknown := api
	unknown.Port, unknown.EndpointLabel, unknown.EndpointLabelSource = 8001, "\x1b[31m\x1b[0m\u202e\t", ""
	m := endpointDetailsModel(t, api, unknown)
	raw := strings.Join(m.detailLines(72), "\n")
	plain := ansi.Strip(raw)
	if strings.Contains(raw, "\x1b]") || !strings.Contains(plain, "Model API") || !strings.Contains(plain, "Explicit metadata") || !strings.Contains(plain, "Purpose unknown") {
		t.Fatalf("metadata was not safely displayed: %q", raw)
	}
	for _, r := range plain {
		if r != '\n' && (unicode.IsControl(r) || unicode.In(r, unicode.Cf) || r == 0x2028 || r == 0x2029) {
			t.Fatalf("unsafe metadata rune U+%04X", r)
		}
	}
}

func TestLongEndpointPurposeAndSourceWrapWithoutClipping(t *testing.T) {
	api := service("local", "python", 11, 8000)
	api.EndpointLabel = "模型 API for internal evaluation and batch jobs"
	api.EndpointLabelSource = "Explicit port mapping in /workspace/a-project-with-a-long-directory-name/.lhwatch.json"
	m := endpointDetailsModel(t, api)
	rows := endpointSection(t, m, 47)
	labelText := strings.Builder{}
	for _, row := range rows {
		if ansi.StringWidth(row) > 47 {
			t.Fatalf("long label overflows endpoint section: %q", row)
		}
		text := strings.ReplaceAll(ansi.Strip(row), api.Endpoint()+" "+api.Family, "")
		labelText.WriteString(text)
	}
	compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
	if compact(labelText.String()) != compact(api.EndpointLabel) {
		t.Fatalf("long label lost content: %q", labelText.String())
	}
	details := ansi.Strip(strings.Join(m.detailLines(47), "\n"))
	if !strings.Contains(compact(details), compact(api.EndpointLabelSource)) {
		t.Fatal("long purpose source was clipped")
	}
}

func TestDemoContainsExplicitPurposeEvidenceAndUnknownAuxiliaryPort(t *testing.T) {
	m := New(context.Background(), DemoHosts(), nil, 0, true)
	for i := range m.hosts {
		m, _ = apply(m, m.scan(i)())
	}
	m.filter = "AI Lab"
	m.reconcile()
	group, ok := m.current()
	if !ok || len(group.endpoints) != 4 {
		t.Fatal("missing dual-stack model demo")
	}
	for _, endpoint := range group.endpoints {
		switch endpoint.Port {
		case 8000:
			if endpoint.EndpointLabel != "Model API" || !strings.HasPrefix(endpoint.EndpointLabelSource, "Demo: simulated ") {
				t.Fatalf("model demo lacks explicit simulated evidence: %+v", endpoint)
			}
		case 8001:
			if endpoint.EndpointLabel != "" || endpoint.EndpointLabelSource != "" {
				t.Fatalf("auxiliary endpoint purpose must remain unknown: %+v", endpoint)
			}
		default:
			t.Fatalf("unexpected demo port: %d", endpoint.Port)
		}
	}
}
