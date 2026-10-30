package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/joeynyc/lhwatch/internal/domain"
)

type fakeForwarder struct {
	mu        sync.Mutex
	opened    []domain.Service
	browsed   []string
	closed    []domain.Forward
	active    []domain.Forward
	openErr   error
	browseErr error
}

func (f *fakeForwarder) Open(_ context.Context, h domain.Host, s domain.Service) (domain.Forward, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, s)
	if f.openErr != nil {
		return domain.Forward{}, f.openErr
	}
	fw := domain.Forward{Host: h.Name, Remote: s.Endpoint(), URL: "http://127.0.0.1:9999/", LocalPort: 9999, Direct: h.IsLocal()}
	if !fw.Direct {
		f.active = append(f.active, fw)
	}
	return fw, nil
}
func (f *fakeForwarder) CloseForward(fw domain.Forward) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, fw)
	f.active = nil
	return nil
}
func (f *fakeForwarder) Active() []domain.Forward {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Forward{}, f.active...)
}
func (f *fakeForwarder) Browse(url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.browsed = append(f.browsed, url)
	return f.browseErr
}

func selectService(t *testing.T, m Model, name string) Model {
	t.Helper()
	for range len(m.groups()) {
		if g, ok := m.current(); ok && g.service.Name == name {
			return m
		}
		m, _ = apply(m, key("j"))
	}
	t.Fatalf("service %s not found", name)
	return m
}

func TestOpenPrefersWebEndpointAndOpensBrowser(t *testing.T) {
	fwd := &fakeForwarder{}
	m := ready(t, &fakeBackend{}).WithForwarder(fwd)
	api := service("local", "web", 11, 2999)
	api.EndpointLabel = "API"
	ui := service("local", "web", 11, 3001)
	ui.EndpointLabel = "Web UI"
	m.hosts[0].snapshot.Services = append(m.hosts[0].snapshot.Services, api, ui)
	m.reconcile() // as a scan carrying the change would
	m = selectService(t, m, "web")
	m, cmd := apply(m, key("o"))
	if cmd == nil || !m.opening {
		t.Fatal("o did not start opening")
	}
	if _, again := apply(m, key("o")); again != nil {
		t.Fatal("second o while opening must not start another open")
	}
	m, _ = apply(m, cmd())
	if len(fwd.opened) != 1 || fwd.opened[0].Port != 3001 {
		t.Fatalf("expected the Web UI endpoint, opened %+v", fwd.opened)
	}
	if len(fwd.browsed) != 1 || !strings.HasPrefix(m.notice, "Opened http://127.0.0.1:9999/") || m.opening {
		t.Fatalf("browser not opened: %v %q", fwd.browsed, m.notice)
	}
}

func TestRemoteTunnelShownInDetailsAndClosable(t *testing.T) {
	fwd := &fakeForwarder{}
	m := ready(t, &fakeBackend{}).WithForwarder(fwd)
	m = selectService(t, m, "db")
	m, _ = apply(m, key("enter"))
	details := strings.Join(m.detailLines(100), "\n")
	if !strings.Contains(details, "through an SSH tunnel") {
		t.Fatalf("remote details should offer a tunnel:\n%s", details)
	}
	m, cmd := apply(m, key("o"))
	if cmd == nil {
		t.Fatal("o in details did not open")
	}
	m, _ = apply(m, cmd())
	if !strings.Contains(m.notice, "→ remote 127.0.0.1:5432") {
		t.Fatalf("notice should name the tunnel: %q", m.notice)
	}
	if details := strings.Join(m.detailLines(100), "\n"); !strings.Contains(details, "tunnel open · O closes it") {
		t.Fatalf("details missing active tunnel:\n%s", details)
	}
	m.errors, m.detail = true, false
	if diag := strings.Join(m.diagnostics(100), "\n"); !strings.Contains(diag, "SSH TUNNELS") || !strings.Contains(diag, "127.0.0.1:9999") {
		t.Fatalf("diagnostics missing tunnel:\n%s", diag)
	}
	m.errors, m.detail = false, true
	m, cmd = apply(m, key("O"))
	if cmd == nil {
		t.Fatal("O did not close the tunnel")
	}
	m, _ = apply(m, cmd())
	if len(fwd.closed) != 1 || !strings.HasPrefix(m.notice, "Closed tunnel") {
		t.Fatalf("tunnel not closed: %+v %q", fwd.closed, m.notice)
	}
	if _, cmd = apply(m, key("O")); cmd != nil {
		t.Fatal("closing with no tunnel should do nothing")
	}
}

func TestOpenWithoutBrowserCopiesURLAndFailuresAreShown(t *testing.T) {
	fwd := &fakeForwarder{browseErr: errors.New("no graphical browser available in this session")}
	m := ready(t, &fakeBackend{}).WithForwarder(fwd)
	m, cmd := apply(m, key("o"))
	m, clip := apply(m, cmd())
	if clip == nil || !strings.Contains(m.notice, "terminal clipboard") {
		t.Fatalf("expected clipboard fallback: %q", m.notice)
	}
	if msg := clip(); msg == nil {
		t.Fatal("clipboard command produced nothing")
	}

	fwd.openErr = errors.New("gpubox: ssh tunnel failed: Permission denied\x1b[2J")
	m, cmd = apply(m, key("o"))
	m, _ = apply(m, cmd())
	if !strings.HasPrefix(m.notice, "Open failed: ") || strings.Contains(m.notice, "\x1b") {
		t.Fatalf("failure not shown safely: %q", m.notice)
	}
}

func TestDemoOpenIsSimulated(t *testing.T) {
	m := New(context.Background(), nil, nil, 0, true).WithForwarder(&fakeForwarder{})
	if _, ok := m.forwarder.(*demoForwarder); !ok {
		t.Fatal("demo must keep its simulated forwarder")
	}
	m, _ = apply(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	for i := range m.hosts {
		m, _ = apply(m, m.scan(i)())
	}
	m, cmd := apply(m, key("o"))
	m, _ = apply(m, cmd())
	if !strings.HasPrefix(m.notice, "Demo · simulated") {
		t.Fatalf("demo open not simulated: %q", m.notice)
	}
}
