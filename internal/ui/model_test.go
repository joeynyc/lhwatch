package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/joeynyc/lhwatch/internal/domain"
)

type fakeBackend struct {
	mu           sync.Mutex
	scans, stops int
	stopped      domain.Service
	host         domain.Host
	scan         func(context.Context, domain.Host) (domain.Snapshot, error)
}

func (f *fakeBackend) Scan(ctx context.Context, h domain.Host) (domain.Snapshot, error) {
	f.mu.Lock()
	f.scans++
	f.mu.Unlock()
	if f.scan != nil {
		return f.scan(ctx, h)
	}
	return domain.Snapshot{}, nil
}
func (f *fakeBackend) Stop(ctx context.Context, h domain.Host, s domain.Service) (domain.StopResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	f.stopped = s
	f.host = h
	return domain.StopResult{Sent: true}, nil
}
func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}
func apply(m Model, msg tea.Msg) (Model, tea.Cmd) { n, cmd := m.Update(msg); return n.(Model), cmd }
func service(host, name string, pid, port int) domain.Service {
	return domain.Service{Host: host, Name: name, PID: pid, Port: port, Address: "127.0.0.1", Family: "IPv4", Identity: name + "-incarnation", CanStop: true, Cwd: "/workspace/" + name}
}
func ready(t *testing.T, b domain.Backend) Model {
	t.Helper()
	m := New(context.Background(), []domain.Host{{Name: "local"}, {Name: "remote", SSH: "dev@example.test"}}, b, time.Second, false)
	m, _ = apply(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = apply(m, scanMsg{index: 0, generation: 1, snapshot: domain.Snapshot{CollectedAt: time.Now(), Services: []domain.Service{service("local", "web", 11, 3000), service("local", "api", 22, 8000)}}})
	m, _ = apply(m, scanMsg{index: 1, generation: 1, snapshot: domain.Snapshot{CollectedAt: time.Now(), Services: []domain.Service{service("remote", "db", 33, 5432)}}})
	return m
}
func TestSearchSelectionAcrossRefresh(t *testing.T) {
	m := ready(t, &fakeBackend{})
	m, _ = apply(m, key("j"))
	selected := m.selected
	m, _ = apply(m, key("r"))
	m, _ = apply(m, scanMsg{index: 0, generation: 2, snapshot: domain.Snapshot{CollectedAt: time.Now(), Services: []domain.Service{service("local", "api", 22, 8000), service("local", "extra", 44, 2000), service("local", "web", 11, 3000)}}})
	if m.selected != selected {
		t.Fatal("refresh changed selected identity")
	}
	m, _ = apply(m, key("/"))
	for _, r := range "api" {
		m, _ = apply(m, key(string(r)))
	}
	if len(m.groups()) != 1 || m.selected != selected {
		t.Fatal("filter lost matching selection")
	}
	m, _ = apply(m, key("q"))
	if !m.searching || m.filter != "apiq" {
		t.Fatal("search interpreted shortcut")
	}
	m, _ = apply(m, key("esc"))
	if m.filter != "" || m.searching {
		t.Fatal("escape must cancel search")
	}
	m, _ = apply(m, key("tab"))
	for _, s := range m.groups() {
		if s.service.Host != "local" {
			t.Fatal("host filter leaked another host")
		}
	}
}
func TestHostFailureRetainsObservationAndIgnoresOldGeneration(t *testing.T) {
	m := ready(t, &fakeBackend{})
	m, _ = apply(m, key("r"))
	m, _ = apply(m, scanMsg{index: 1, generation: 2, err: errors.New("connection refused")})
	if len(m.hosts[1].snapshot.Services) != 1 || !m.stale(m.hosts[1]) {
		t.Fatal("failure erased stale observation")
	}
	m, _ = apply(m, scanMsg{index: 0, generation: 2, snapshot: domain.Snapshot{CollectedAt: time.Now(), Services: []domain.Service{service("local", "new", 55, 9000)}}})
	if m.hosts[0].snapshot.Services[0].Name != "new" {
		t.Fatal("good host did not update independently")
	}
	m, _ = apply(m, scanMsg{index: 0, generation: 1, err: errors.New("old error")})
	if m.hosts[0].err != "" {
		t.Fatal("old generation overwritten current state")
	}
}
func TestHangingHostDoesNotBlockOtherHostAndNoDuplicateScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked := make(chan struct{})
	backend := &fakeBackend{scan: func(ctx context.Context, h domain.Host) (domain.Snapshot, error) {
		if h.Name == "slow" {
			close(blocked)
			<-ctx.Done()
			return domain.Snapshot{}, ctx.Err()
		}
		return domain.Snapshot{Services: []domain.Service{service(h.Name, "web", 1, 3000)}}, nil
	}}
	m := New(ctx, []domain.Host{{Name: "slow"}, {Name: "fast"}}, backend, time.Second, false)
	done := make(chan tea.Msg, 1)
	go func() { done <- m.scan(0)() }()
	<-blocked
	if m.refresh(0) != nil {
		t.Fatal("started duplicate scan for busy host")
	}
	m, _ = apply(m, m.scan(1)())
	if len(m.hosts[1].snapshot.Services) != 1 || !m.hosts[0].inflight {
		t.Fatal("hanging host blocked independent update")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("root context not passed to scan")
	}
}
func TestStopCancelAndFrozenTargetConfirmedOnce(t *testing.T) {
	backend := &fakeBackend{}
	m := ready(t, backend)
	extra := service("local", "web", 11, 3001)
	m.hosts[0].snapshot.Services = append(m.hosts[0].snapshot.Services, extra)
	m.filter = "3000"
	m.reconcile()
	m, _ = apply(m, key("x"))
	if m.confirm == nil || len(m.confirm.endpoints) != 2 {
		t.Fatal("stop confirmation omitted endpoints outside filter")
	}
	m, _ = apply(m, key("enter"))
	if m.confirm != nil || backend.stops != 0 {
		t.Fatal("default action must cancel")
	}
	m, _ = apply(m, key("x"))
	frozen := m.confirm.service
	m, _ = apply(m, key("r")) // modal must not interpret dashboard shortcuts
	m.hosts[0].generation++
	m.hosts[0].inflight = true
	m, _ = apply(m, scanMsg{index: 0, generation: m.hosts[0].generation, snapshot: domain.Snapshot{CollectedAt: time.Now(), Services: []domain.Service{service("local", "replacement", 99, 3000)}}})
	var cmd tea.Cmd
	m, cmd = apply(m, key("y"))
	if cmd == nil {
		t.Fatal("confirmation did not dispatch stop")
	}
	m, repeat := apply(m, key("y"))
	if repeat != nil {
		t.Fatal("repeated confirmation dispatched a second stop")
	}
	m, _ = apply(m, cmd())
	if backend.stops != 1 || backend.stopped.Key() != frozen.Key() || backend.host.Name != "local" {
		t.Fatal("stop retargeted after refresh")
	}
	if !strings.Contains(m.notice, "SIGTERM requested") || strings.Contains(m.notice, "stopped") {
		t.Fatal("UI claims exit without observation")
	}
}
func TestUnsafeStopDisabled(t *testing.T) {
	for _, kind := range []string{"stale", "unavailable", "identity", "permission"} {
		t.Run(kind, func(t *testing.T) {
			m := ready(t, &fakeBackend{})
			switch kind {
			case "stale":
				m.hosts[0].observed = time.Now().Add(-time.Hour)
			case "unavailable":
				m.hosts[0].err = "offline"
			case "identity":
				m.hosts[0].snapshot.Services[0].Identity = ""
			case "permission":
				m.hosts[0].snapshot.Services[0].CanStop = false
			}
			m.reconcile()
			m, _ = apply(m, key("x"))
			if m.confirm != nil {
				t.Fatal("unsafe stop offered")
			}
		})
	}
}
func TestDemoNeverUsesRealBackend(t *testing.T) {
	real := &fakeBackend{}
	m := New(context.Background(), DemoHosts(), real, time.Second, true)
	if !strings.Contains(ansi.Strip(m.View().Content), "Initializing") {
		t.Fatal("expected initial size fallback")
	}
	m, _ = apply(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = apply(m, m.scan(0)())
	m, _ = apply(m, key("x"))
	m, cmd := apply(m, key("y"))
	if cmd == nil {
		t.Fatal("demo stop unavailable")
	}
	apply(m, cmd())
	if real.scans != 0 || real.stops != 0 {
		t.Fatal("demo called real backend")
	}
}
func TestViewsFitAndExternalTextIsInert(t *testing.T) {
	m := ready(t, &fakeBackend{})
	evil := "evil\x1b]52;c;YXR0YWNr\a\x1b[31mRED\x1b[0m\n\t\x00\u202e\u2066\u009b text"
	m.hosts[0].snapshot.Services[0].Name = evil
	m.hosts[0].snapshot.Services[0].Command = strings.Repeat(evil, 30)
	m.hosts[0].snapshot.Services[0].Cwd = evil
	m.hosts[0].err = evil + strings.Repeat("long diagnostics ", 60)
	m.notice = evil
	m.reconcile()
	for _, size := range [][2]int{{150, 42}, {120, 30}, {80, 24}, {55, 16}, {30, 8}, {1, 1}} {
		m.width, m.height = size[0], size[1]
		for _, mode := range []string{"dashboard", "details", "errors", "help"} {
			m.detail, m.errors, m.help = mode == "details", mode == "errors", mode == "help"
			raw := m.View().Content
			plain := ansi.Strip(raw)
			if strings.Contains(raw, "\x1b]52") {
				t.Fatal("OSC injection rendered")
			}
			for _, r := range plain {
				if r != '\n' && (unicode.IsControl(r) || unicode.In(r, unicode.Cf)) {
					t.Fatalf("unsafe rune U+%04X", r)
				}
			}
			lines := strings.Split(raw, "\n")
			if len(lines) > m.height {
				t.Fatalf("%v %s overflows height", size, mode)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > m.width {
					t.Fatalf("%v %s overflows width: %d", size, mode, ansi.StringWidth(line))
				}
			}
		}
	}
}
func TestDiagnosticsCanScrollToFullError(t *testing.T) {
	m := ready(t, &fakeBackend{})
	m.width, m.height = 80, 24
	m.hosts[0].err = strings.Repeat("reason ", 400) + "FINAL_ERROR_DETAIL"
	m, _ = apply(m, key("e"))
	found := false
	for range 500 {
		if strings.Contains(ansi.Strip(m.View().Content), "FINAL_ERROR_DETAIL") {
			found = true
			break
		}
		m, _ = apply(m, key("j"))
	}
	if !found {
		t.Fatal("long error is inaccessible")
	}
}

func TestRemoteClockSkewDoesNotControlFreshness(t *testing.T) {
	for _, skew := range []time.Duration{-time.Hour, time.Hour} {
		t.Run(skew.String(), func(t *testing.T) {
			m := New(context.Background(), []domain.Host{{Name: "remote", SSH: "dev@example.test"}}, &fakeBackend{}, time.Second, false)
			m, _ = apply(m, tea.WindowSizeMsg{Width: 120, Height: 30})
			remoteTime := time.Now().Add(skew)
			before := time.Now()
			m, _ = apply(m, scanMsg{index: 0, generation: 1, snapshot: domain.Snapshot{CollectedAt: remoteTime, Services: []domain.Service{service("remote", "web", 11, 3000)}}})
			if m.hosts[0].observed.Before(before) || m.hosts[0].observed.After(time.Now()) || m.stale(m.hosts[0]) {
				t.Fatal("freshness depended on the remote clock")
			}
			if !m.hosts[0].snapshot.CollectedAt.Equal(remoteTime) {
				t.Fatal("remote collection timestamp was lost")
			}
			m, _ = apply(m, key("x"))
			if m.confirm == nil {
				t.Fatal("fresh remote service could not enter stop confirmation")
			}
			m, _ = apply(m, key("esc"))
			m, _ = apply(m, clockMsg(m.hosts[0].observed.Add(11*time.Second)))
			m, _ = apply(m, key("x"))
			if !m.stale(m.hosts[0]) || m.confirm != nil {
				t.Fatal("old remote observation did not expire on local time")
			}
		})
	}
}

func TestStopRequiresVisibleConfirmation(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{{Width: 54, Height: 30}, {Width: 100, Height: 15}, {}} {
		for _, alreadyOpen := range []bool{false, true} {
			backend := &fakeBackend{}
			m := ready(t, backend)
			m, _ = apply(m, tea.WindowSizeMsg{Width: 100, Height: 30})
			if alreadyOpen {
				m, _ = apply(m, key("x"))
				if m.confirm == nil {
					t.Fatal("normal viewport did not open confirmation")
				}
			}
			m, _ = apply(m, size)
			if !alreadyOpen {
				m, _ = apply(m, key("x"))
			}
			var cmd tea.Cmd
			m, cmd = apply(m, key("y"))
			if cmd != nil {
				cmd()
			}
			if m.confirm != nil || backend.stops != 0 || m.stopping {
				t.Fatalf("hidden confirmation could stop a process: size=%+v alreadyOpen=%v", size, alreadyOpen)
			}
		}
	}
}

func TestStopTransportFailureReportsUnknownOutcome(t *testing.T) {
	m := ready(t, &fakeBackend{})
	m.stopping = true
	m, cmd := apply(m, stopMsg{host: "local", pid: 11, err: errors.New("SSH response lost after dispatch")})
	if !strings.Contains(m.notice, "outcome unknown") || !strings.Contains(m.notice, "local") || !strings.Contains(m.notice, "PID 11") || strings.Contains(m.notice, "refused") {
		t.Fatalf("notice incorrectly established signal outcome: %s", m.notice)
	}
	if m.stopping || !m.hosts[0].inflight || cmd == nil {
		t.Fatal("uncertain stop did not trigger observation refresh")
	}
}

func TestProjectNameLeadsViewAndSearchWithoutChangingTarget(t *testing.T) {
	m := ready(t, &fakeBackend{})
	s := &m.hosts[0].snapshot.Services[0]
	originalKey := s.Key()
	s.Project = "My Store Website"
	s.ProjectPath = "/workspace/storefront"
	s.ProjectSource = "Custom name (.lhwatch.json)"
	m.filter = "my store"
	m.reconcile()
	rows := m.groups()
	if len(rows) != 1 || rows[0].service.Key() != originalKey {
		t.Fatal("project name search changed process identity or failed to find the project")
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "My Store Website") || !strings.Contains(view, "PROJECT / SERVICE") || !strings.Contains(view, "Custom name (.lhwatch.json)") {
		t.Fatal("project identity and its evidence were not visible")
	}
	m, _ = apply(m, key("x"))
	if m.confirm == nil || m.confirm.service.Key() != originalKey {
		t.Fatal("friendly name changed stop targeting")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "My Store Website") {
		t.Fatal("confirmation omitted the friendly name")
	}
}
func TestDetailsOpenStopConfirmationAndCancelReturns(t *testing.T) {
	backend := &fakeBackend{}
	m := ready(t, backend)
	m, _ = apply(m, key("enter"))
	if !m.detail || !strings.Contains(strings.Join(m.detailLines(80), "\n"), "x opens confirmation") {
		t.Fatal("details did not advertise stop")
	}
	m, _ = apply(m, key("x"))
	if m.confirm == nil {
		t.Fatal("x in details did not open the stop confirmation")
	}
	m, _ = apply(m, key("esc"))
	if m.confirm != nil || !m.detail {
		t.Fatal("cancelling the confirmation should return to details")
	}
	m, _ = apply(m, key("x"))
	m, cmd := apply(m, key("y"))
	if cmd == nil || m.detail {
		t.Fatal("confirmed stop from details should dispatch and return to the list")
	}
	m, _ = apply(m, cmd())
	if backend.stops != 1 {
		t.Fatalf("expected one stop, got %d", backend.stops)
	}
}
func TestDetailsStopUnavailableKeepsDetailsOpen(t *testing.T) {
	m := ready(t, &fakeBackend{})
	m.hosts[0].snapshot.Services[0].CanStop = false
	m.hosts[0].snapshot.Services[1].CanStop = false
	m.reconcile() // as a scan carrying the change would
	m, _ = apply(m, key("enter"))
	m, _ = apply(m, key("x"))
	if m.confirm != nil || !m.detail || !strings.HasPrefix(m.notice, "Stop unavailable") {
		t.Fatalf("unavailable stop from details: confirm=%v detail=%v notice=%q", m.confirm != nil, m.detail, m.notice)
	}
}
func TestPanelScrollIsClampedToContent(t *testing.T) {
	m := ready(t, &fakeBackend{})
	m, _ = apply(m, key("?"))
	limit := m.maxScroll()
	if limit <= 0 {
		t.Fatalf("help should overflow a 30-line terminal, max scroll %d", limit)
	}
	for range limit + 20 {
		m, _ = apply(m, key("j"))
	}
	if m.scroll != limit {
		t.Fatalf("scroll %d exceeded content limit %d", m.scroll, limit)
	}
	m, _ = apply(m, key("k"))
	if m.scroll != limit-1 {
		t.Fatalf("one up press should move immediately after overscroll, got %d", m.scroll)
	}
	m, _ = apply(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.scroll != limit {
		t.Fatalf("page down overscrolled to %d", m.scroll)
	}
	m, _ = apply(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	m, _ = apply(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.scroll != 0 {
		t.Fatalf("page up should stop at the top, got %d", m.scroll)
	}
}
