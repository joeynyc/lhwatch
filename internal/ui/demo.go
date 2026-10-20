package ui

import (
	"context"
	"fmt"
	"path"
	"sync"
	"time"

	"github.com/joeynyc/lhwatch/internal/domain"
)

func DemoHosts() []domain.Host {
	return []domain.Host{{Name: "local"}, {Name: "devbox", SSH: "dev@devbox.example"}, {Name: "lab", SSH: "dev@lab.example"}}
}

type demoBackend struct {
	mu      sync.Mutex
	stopped map[string]bool
}

// NewDemoBackend is entirely in memory: it never scans hosts or sends signals.
func NewDemoBackend() domain.Backend { return &demoBackend{stopped: map[string]bool{}} }
func (d *demoBackend) Scan(ctx context.Context, h domain.Host) (domain.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.Snapshot{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	type entry struct {
		name         string
		pid, port    int
		cwd, command string
		label        string
	}
	entries := []entry{
		{"node", 4128, 3000, "/workspace/storefront", "node node_modules/.bin/vite --host 127.0.0.1", "Web UI"},
		{"python", 5902, 8000, "/workspace/catalog-api", "python -m uvicorn app:app --reload", "API"},
		{"postgres", 1874, 5432, "/workspace/data", "postgres -D /workspace/data", ""},
		{"redis-server", 2091, 6379, "/workspace/cache", "redis-server --bind 127.0.0.1", ""},
	}
	if h.Name == "devbox" {
		entries = []entry{
			{"node", 7241, 3001, "/srv/preview", "node server.mjs", "Web UI"},
			{"go", 8226, 8080, "/srv/search-api", "./search-api --listen 127.0.0.1:8080", "API"},
			{"python", 9230, 8888, "/srv/notebooks", "jupyter lab --no-browser", "Web UI"},
		}
	}
	if h.Name == "lab" {
		entries = []entry{
			{"ollama", 3154, 11434, "/srv/models", "ollama serve", "Model API"},
			{"node", 4120, 5173, "/srv/playground", "node node_modules/.bin/vite", "Web UI"},
		}
	}
	s := domain.Snapshot{SchemaVersion: domain.SchemaVersion, Host: h.Name, Hostname: h.Name, Platform: "linux", CollectedAt: time.Now()}
	if h.Name == "local" {
		s.Platform = "darwin"
	}
	for _, e := range entries {
		if d.stopped[fmt.Sprintf("%s/%d", h.Name, e.pid)] {
			continue
		}
		service := domain.Service{Host: h.Name, PID: e.pid, Name: e.name, Command: e.command, Cwd: e.cwd, User: "developer", UID: 1000, Address: "127.0.0.1", Port: e.port, Family: "IPv4", Started: "2026-10-01 09:14:22", Identity: fmt.Sprintf("demo-%s-%d", h.Name, e.pid), CanStop: true}
		service.Project = path.Base(e.cwd)
		service.ProjectPath = e.cwd
		service.ProjectSource = "Project folder (.git)"
		service.EndpointLabel = e.label
		if e.label != "" {
			service.EndpointLabelSource = "Demo: simulated .lhwatch.json port mapping"
		}
		s.Services = append(s.Services, service)
		if e.name == "postgres" {
			service.Address = "::1"
			service.Family = "IPv6"
			s.Services = append(s.Services, service)
		}
	}
	if h.Name == "lab" {
		container := domain.Service{Host: h.Name, Project: "AI Lab / Qwen API", ProjectSource: "Docker Compose service", ContainerID: "demo-qwen-container", ContainerName: "qwen-api", ContainerImage: "vllm/vllm-openai:demo", Name: "unknown", UID: -1, CanStop: false, StopReason: "Use Docker to stop this container.", VisibilityReason: "Container identified; the host process is not visible to this user."}
		for _, port := range []int{8000, 8001} {
			container.EndpointLabel, container.EndpointLabelSource = "", ""
			if port == 8000 {
				container.EndpointLabel = "Model API"
				container.EndpointLabelSource = "Demo: simulated vLLM launch port"
			}
			for _, ep := range [][2]string{{"0.0.0.0", "IPv4"}, {"::", "IPv6"}} {
				container.Port, container.Address, container.Family = port, ep[0], ep[1]
				s.Services = append(s.Services, container)
			}
		}
		s.Services = append(s.Services, domain.Service{Host: h.Name, Name: "unknown", UID: -1, Address: "0.0.0.0", Port: 9090, Family: "IPv4", VisibilityReason: "Process details are hidden by host permissions.", StopReason: "Process ownership could not be verified."})
	}
	if h.Name == "lab" {
		s.Partial = true
		s.Warnings = []string{"Demo: some process details are hidden by host permissions. Container names and endpoint observations remain available."}
	}
	return s, nil
}

// demoForwarder simulates tunnels: it never connects, binds a port, or opens
// a browser.
type demoForwarder struct {
	mu     sync.Mutex
	active map[string]domain.Forward
}

func newDemoForwarder() *demoForwarder { return &demoForwarder{active: map[string]domain.Forward{}} }
func (d *demoForwarder) Open(ctx context.Context, h domain.Host, s domain.Service) (domain.Forward, error) {
	if err := ctx.Err(); err != nil {
		return domain.Forward{}, err
	}
	f := domain.Forward{Host: h.Name, Remote: s.Endpoint(), URL: fmt.Sprintf("http://127.0.0.1:%d/", s.Port), LocalPort: s.Port, Direct: h.IsLocal()}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !f.Direct {
		d.active[h.Name+"\x00"+f.Remote] = f
	}
	return f, nil
}
func (d *demoForwarder) CloseForward(f domain.Forward) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.active, f.Host+"\x00"+f.Remote)
	return nil
}
func (d *demoForwarder) Active() []domain.Forward {
	d.mu.Lock()
	defer d.mu.Unlock()
	forwards := []domain.Forward{}
	for _, f := range d.active {
		forwards = append(forwards, f)
	}
	return forwards
}
func (d *demoForwarder) Browse(string) error { return fmt.Errorf("demo mode never opens a browser") }

func (d *demoBackend) Stop(ctx context.Context, h domain.Host, s domain.Service) (domain.StopResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.StopResult{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.Identity != fmt.Sprintf("demo-%s-%d", h.Name, s.PID) {
		return domain.StopResult{}, fmt.Errorf("demo process identity changed")
	}
	d.stopped[fmt.Sprintf("%s/%d", h.Name, s.PID)] = true
	return domain.StopResult{SchemaVersion: domain.SchemaVersion, Sent: true, Message: "Simulated SIGTERM requested"}, nil
}
