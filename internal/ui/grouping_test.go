package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/joeynyc/lhwatch/internal/domain"
)

func TestIdenticalUnverifiedEndpointsRemainSeparate(t *testing.T) {
	m := ready(t, &fakeBackend{})
	s := domain.Service{Host: "local", Name: "unknown", Address: "127.0.0.1", Port: 9000, Family: "IPv4"}
	m.hosts[0].snapshot.Services = []domain.Service{s, s}
	m.hosts[1].snapshot.Services = nil
	m.reconcile()
	groups := m.groups()
	if len(groups) != 2 || groups[0].key() == groups[1].key() {
		t.Fatal("unknown shared sockets require separate rows and selection keys")
	}
	first := m.selected
	m.move(1)
	if m.selected == first {
		t.Fatal("both unknown rows select the same observation")
	}
}

func TestContainerSiblingBlocksOrdinaryRowEvenWhenFiltered(t *testing.T) {
	m := ready(t, &fakeBackend{})
	ordinary := service("local", "proxy", 400, 18001)
	container := ordinary
	container.Port, container.ContainerID = 18000, "container-a"
	m.hosts[0].snapshot.Services = []domain.Service{ordinary, container}
	m.hosts[1].snapshot.Services = nil
	m.filter = "18001"
	m.reconcile()
	m, _ = apply(m, key("x"))
	if m.confirm != nil || !strings.Contains(m.notice, "Docker") {
		t.Fatal("filtered ordinary row offered to stop container's process")
	}
	m.hosts[0].snapshot.Services[1].Identity += ":reused"
	m, _ = apply(m, key("x"))
	if m.confirm == nil {
		t.Fatal("different process incarnation incorrectly blocks stopping")
	}
}

func TestNewContainerEvidenceBlocksFrozenConfirmation(t *testing.T) {
	m := ready(t, &fakeBackend{})
	ordinary := service("local", "proxy", 400, 18001)
	m.hosts[0].snapshot.Services = []domain.Service{ordinary}
	m.hosts[1].snapshot.Services = nil
	m.reconcile()
	m, _ = apply(m, key("x"))
	if m.confirm == nil {
		t.Fatal("ordinary fixture did not open confirmation")
	}
	container := ordinary
	container.Port, container.ContainerID = 18000, "container-a"
	m.hosts[0].snapshot.Services = append(m.hosts[0].snapshot.Services, container)
	m, cmd := apply(m, key("y"))
	if cmd != nil || !strings.Contains(m.notice, "Docker") {
		t.Fatal("new container evidence did not prevent dispatch from old confirmation")
	}
}

func TestGroupingKeepsOwnershipBoundaries(t *testing.T) {
	m := ready(t, &fakeBackend{})
	base := service("local", "api", 22, 8000)
	base.Project = "Same Project"
	otherPort := base
	otherPort.Port = 8001
	otherPID := base
	otherPID.PID = 23
	otherPID.Port = 8002
	otherIdentity := base
	otherIdentity.Identity = "new-incarnation"
	otherIdentity.Port = 8003
	unknownA := domain.Service{Host: "local", Name: "unknown", Port: 9000, Address: "127.0.0.1", Family: "IPv4"}
	unknownB := unknownA
	unknownB.Port = 9001
	m.hosts[0].snapshot.Services = []domain.Service{base, otherPort, otherPID, otherIdentity, unknownA, unknownB}
	remote := base
	remote.Host = "remote"
	m.hosts[1].snapshot.Services = []domain.Service{remote}
	m.reconcile()
	if got := len(m.groups()); got != 6 {
		t.Fatalf("wanted 6 separate services, got %d", got)
	}
	m.filter = "8001"
	m.reconcile()
	if len(m.groups()) != 1 {
		t.Fatal("port search must select its owning service")
	}
	details := ansi.Strip(strings.Join(m.detailLines(100), "\n"))
	if !strings.Contains(details, "127.0.0.1:8000 IPv4") || !strings.Contains(details, "127.0.0.1:8001 IPv4") || strings.Contains(details, "127.0.0.1:8003 IPv4") {
		t.Fatalf("details crossed identity boundaries or dropped filtered endpoints: %s", details)
	}
	m.filter = ""
	m.reconcile()
	selected := m.selected
	m.hosts[0].snapshot.Services = []domain.Service{otherPort, otherPID, otherIdentity, unknownA, unknownB}
	m.reconcile()
	if m.selected != selected {
		t.Fatal("losing one endpoint changed selected service")
	}
}

func TestContainerGroupingCannotOfferProcessStop(t *testing.T) {
	backend := &fakeBackend{}
	m := ready(t, backend)
	first := service("local", "docker-proxy", 44, 8080)
	first.ContainerID, first.ContainerName, first.Project = "container-a", "my-api", "My API"
	second := first
	second.Port, second.PID, second.Identity = 9090, 45, "different-proxy"
	m.hosts[0].snapshot.Services = []domain.Service{first, second}
	m.hosts[1].snapshot.Services = nil
	m.reconcile()
	if len(m.groups()) != 1 {
		t.Fatal("container listeners should have one service row")
	}
	m, _ = apply(m, key("x"))
	if m.confirm != nil || !strings.Contains(m.notice, "Docker") || backend.stops != 0 {
		t.Fatalf("container offered a process stop: %s", m.notice)
	}
	details := ansi.Strip(strings.Join(m.detailLines(100), "\n"))
	if !strings.Contains(details, "Use Docker to stop this container") {
		t.Fatal("details must explain container controls")
	}
}

func TestUnidentifiedServiceExplainsMissingInformationEarly(t *testing.T) {
	m := ready(t, &fakeBackend{})
	m.hosts[0].snapshot.Services = []domain.Service{{Host: "local", Name: "unknown", Port: 9000, Family: "IPv4", Address: "127.0.0.1", VisibilityReason: "Process details require additional permissions.", StopReason: "Process ownership is unavailable."}}
	m.hosts[1].snapshot.Services = nil
	m.hosts[0].snapshot.Partial = true
	m.reconcile()
	details := ansi.Strip(strings.Join(m.detailLines(100), "\n"))
	if strings.Contains(details, "unknown / 0") || !strings.Contains(details, "PROCESS / PID\nunavailable") {
		t.Fatal("PID zero must not look like a real process")
	}
	if !strings.Contains(details, "Process details require additional permissions.") || !strings.Contains(details, "Process ownership is unavailable.") {
		t.Fatal("missing explanation for identity or stop availability")
	}
	if i := strings.Index(details, "STOP PROCESS"); i < 0 || i > strings.Index(details, "COMMAND") {
		t.Fatal("stop availability is buried below raw process details")
	}
	if strings.Contains(ansi.Strip(m.status(m.hosts[0])), "partial") || !strings.Contains(ansi.Strip(m.status(m.hosts[0])), "limited details") {
		t.Fatal("partial is unexplained jargon")
	}
}

func TestIdentifiedServicesPrecedeUnidentifiedAcrossHosts(t *testing.T) {
	m := ready(t, &fakeBackend{})
	m.hosts[0].snapshot.Services = []domain.Service{{Host: "local", Name: "unknown", Port: 20}}
	m.reconcile()
	current, ok := m.current()
	if !ok || current.service.Host != "remote" {
		t.Fatal("unidentified services still dominate initial view")
	}
}

func TestContainerEvidenceAlwaysPreventsProcessStop(t *testing.T) {
	for _, kind := range []string{"id", "name", "image", "source"} {
		t.Run(kind, func(t *testing.T) {
			m := ready(t, &fakeBackend{})
			s := &m.hosts[0].snapshot.Services[0]
			switch kind {
			case "id":
				s.ContainerID = "container-id"
			case "name":
				s.ContainerName = "container-name"
			case "image":
				s.ContainerImage = "container-image"
			case "source":
				s.ProjectSource = "Docker Compose service"
			}
			m.reconcile()
			m, _ = apply(m, key("x"))
			if m.confirm != nil || !strings.Contains(m.notice, "Use Docker") {
				t.Fatal("container evidence did not block process stop")
			}
		})
	}
}

func TestContainerSearchAndFilteredEndpoints(t *testing.T) {
	m := ready(t, &fakeBackend{})
	a := service("local", "unknown", 0, 8000)
	a.Identity = ""
	a.ContainerID, a.ContainerName, a.ContainerImage, a.Project = "container-a", "qwen-service", "vllm:demo", "My AI Server"
	a.CanStop = false
	b := a
	b.Address, b.Family = "::1", "IPv6"
	c := a
	c.Port = 8001
	m.hosts[0].snapshot.Services = []domain.Service{a, b, c}
	m.hosts[1].snapshot.Services = nil
	for _, query := range []string{"container-a", "qwen-service", "vllm:demo", "8001", "::1"} {
		m.filter = query
		m.reconcile()
		rows := m.groups()
		if len(rows) != 1 || len(rows[0].endpoints) != 3 || rows[0].ports() != "8000, 8001" {
			t.Fatalf("query %q failed to retain all service endpoints", query)
		}
	}
	m.hostFilter = 1
	m.reconcile()
	if len(m.groups()) != 0 {
		t.Fatal("host filter leaked container")
	}
}

func TestUnverifiedProcessesDoNotGroupOrOfferStop(t *testing.T) {
	m := ready(t, &fakeBackend{})
	a := service("local", "python", 7, 8000)
	a.Identity = ""
	b := a
	b.Port = 8001
	m.hosts[0].snapshot.Services = []domain.Service{a, b}
	m.hosts[1].snapshot.Services = nil
	m.reconcile()
	if len(m.groups()) != 2 {
		t.Fatal("PID without incarnation evidence was grouped")
	}
	m, _ = apply(m, key("x"))
	if m.confirm != nil {
		t.Fatal("unverified process offered stop")
	}
}

func TestGroupedConfirmationDoesNotIncludeReusedPID(t *testing.T) {
	m := ready(t, &fakeBackend{})
	base := service("local", "web", 11, 3000)
	different := base
	different.Identity = "new-incarnation"
	different.Port = 3001
	m.hosts[0].snapshot.Services = []domain.Service{base, different}
	m.hosts[1].snapshot.Services = nil
	m.reconcile()
	m, _ = apply(m, key("x"))
	if m.confirm == nil || len(m.confirm.endpoints) != 1 || m.confirm.service.Key() != base.Key() {
		t.Fatal("confirmation crossed process incarnation boundary")
	}
}

func TestGroupedDemoResponsiveAndCounts(t *testing.T) {
	m := New(context.Background(), DemoHosts(), nil, 0, true)
	for i := range m.hosts {
		m, _ = apply(m, m.scan(i)())
	}
	m.filter = "AI Lab"
	m.reconcile()
	if len(m.groups()) != 1 || len(m.groups()[0].endpoints) != 4 {
		t.Fatal("demo lacks multiport dual-stack container")
	}
	for _, size := range [][2]int{{144, 42}, {120, 30}, {80, 24}, {55, 16}} {
		m.width, m.height = size[0], size[1]
		for _, detail := range []bool{false, true} {
			m.detail = detail
			view := m.View().Content
			if len(strings.Split(view, "\n")) > size[1] {
				t.Fatal("grouped UI exceeds height")
			}
			for _, line := range strings.Split(view, "\n") {
				if ansi.StringWidth(line) > size[0] {
					t.Fatal("grouped UI exceeds width")
				}
			}
			if !detail && !strings.Contains(ansi.Strip(view), "1 service · 4 endpoints") {
				t.Fatalf("service counts missing at %v", size)
			}
		}
	}
}

func TestPortColumnNeverClipsAPortNumber(t *testing.T) {
	m := ready(t, &fakeBackend{})
	a := service("local", "web", 11, 12345)
	b := a
	b.Port = 56789
	m.hosts[0].snapshot.Services = []domain.Service{a, b}
	m.hosts[1].snapshot.Services = nil
	m.reconcile()
	two := ansi.Strip(strings.Join(m.table(82, 32), "\n"))
	if !strings.Contains(two, "12345, 56789") {
		t.Fatal("two five-digit ports are clipped")
	}
	c := a
	c.Port = 30000
	m.hosts[0].snapshot.Services = append(m.hosts[0].snapshot.Services, c)
	m.reconcile()
	for _, width := range []int{82, 51} {
		view := ansi.Strip(strings.Join(m.table(width, 16), "\n"))
		if !strings.Contains(view, "12345 +2") {
			t.Fatalf("ports need complete first port and overflow count: %s", view)
		}
	}
	details := ansi.Strip(strings.Join(m.detailLines(100), "\n"))
	for _, endpoint := range []string{"127.0.0.1:12345 IPv4", "127.0.0.1:30000 IPv4", "127.0.0.1:56789 IPv4"} {
		if !strings.Contains(details, endpoint) {
			t.Fatal("detail view lost full endpoint")
		}
	}
}

func TestPreviewWrapsProjectAndPorts(t *testing.T) {
	m := ready(t, &fakeBackend{})
	a := service("local", "web", 11, 12345)
	a.Project = "My Website Development Preview Server"
	b := a
	b.Port = 56789
	c := a
	c.Port = 30000
	m.hosts[0].snapshot.Services = []domain.Service{a, b, c}
	m.hosts[1].snapshot.Services = nil
	m.reconcile()
	preview := m.detailPreview(22, 40)
	for _, line := range preview {
		if ansi.StringWidth(line) > 22 {
			t.Fatalf("preview line will be truncated: %s", line)
		}
	}
	text := strings.Join(strings.Fields(ansi.Strip(strings.Join(preview, " "))), " ")
	if !strings.Contains(text, a.Project) || !strings.Contains(text, "12345, 30000, 56789") {
		t.Fatalf("preview lost project or ports: %s", text)
	}
}
