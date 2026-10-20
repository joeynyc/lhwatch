package collector

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joeynyc/lhwatch/internal/config"
	"github.com/joeynyc/lhwatch/internal/domain"
)

// Tunnels makes listeners reachable from this machine. Local listeners are
// opened directly. Remote listeners are forwarded through one dedicated SSH
// connection per host that lives until Close, independent of scan reuse.
// Forwards bind only to 127.0.0.1 here.
type Tunnels struct {
	timeout    time.Duration
	controlDir string

	// changeMu serializes opens and closes, which may wait on SSH. mu guards
	// the maps only briefly, so Active never blocks the dashboard's render.
	changeMu sync.Mutex
	mu       sync.Mutex
	closed   bool
	masters  map[string]*tunnelMaster // host name → connection
}

type tunnelMaster struct {
	host     domain.Host
	path     string
	cmd      *exec.Cmd
	done     chan struct{}
	forwards map[string]domain.Forward // remote endpoint → forward
}

func (t *tunnelMaster) alive() bool {
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

var _ domain.Forwarder = (*Tunnels)(nil)

// NewTunnels needs the same private socket directory as connection reuse;
// without one, remote forwarding reports why it is unavailable.
func NewTunnels(timeout time.Duration) *Tunnels {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	t := &Tunnels{timeout: timeout, masters: map[string]*tunnelMaster{}}
	if dir, err := privateControlDir(controlBase()); err == nil {
		t.controlDir = dir
	}
	return t
}

// connectAddress turns a listening address into one a client can dial:
// wildcard listeners are reached through loopback of the same family.
func connectAddress(s domain.Service) (netip.Addr, error) {
	addr, err := netip.ParseAddr(s.Address)
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, fmt.Errorf("cannot open %s: unsupported listening address", s.Endpoint())
	}
	if s.Port < 1 || s.Port > 65535 {
		return netip.Addr{}, fmt.Errorf("cannot open %s: invalid port", s.Endpoint())
	}
	if addr.IsUnspecified() {
		if addr.Is4() {
			return netip.MustParseAddr("127.0.0.1"), nil
		}
		return netip.IPv6Loopback(), nil
	}
	return addr.Unmap(), nil
}

func httpURL(addr netip.Addr, port int) string {
	return "http://" + net.JoinHostPort(addr.String(), strconv.Itoa(port)) + "/"
}

func (t *Tunnels) Open(ctx context.Context, host domain.Host, s domain.Service) (domain.Forward, error) {
	target, err := connectAddress(s)
	if err != nil {
		return domain.Forward{}, err
	}
	remote := net.JoinHostPort(target.String(), strconv.Itoa(s.Port))
	if host.IsLocal() {
		return domain.Forward{Host: host.Name, Remote: remote, URL: httpURL(target, s.Port), Direct: true}, nil
	}
	if err := config.ValidateHost(host); err != nil {
		return domain.Forward{}, err
	}
	if t.controlDir == "" {
		return domain.Forward{}, errors.New("SSH tunnels need a private socket directory; see README")
	}
	// One change at a time keeps port choice and master startup simple;
	// opens are user-initiated and rare.
	t.changeMu.Lock()
	defer t.changeMu.Unlock()
	master, err := t.master(ctx, host)
	if err != nil {
		return domain.Forward{}, err
	}
	t.mu.Lock()
	f, ok := master.forwards[remote]
	t.mu.Unlock()
	if ok {
		return f, nil
	}
	var lastErr error
	for _, port := range localPorts(s.Port) {
		spec := forwardSpec(port, target, s.Port)
		if err := t.control(ctx, master, "forward", spec); err != nil {
			lastErr = err
			continue // the port was taken between the check and the bind
		}
		f := domain.Forward{Host: host.Name, Remote: remote, URL: httpURL(netip.MustParseAddr("127.0.0.1"), port), LocalPort: port}
		t.mu.Lock()
		master.forwards[remote] = f
		t.mu.Unlock()
		return f, nil
	}
	return domain.Forward{}, fmt.Errorf("%s: could not forward %s: %w", host.Name, remote, lastErr)
}

// localPorts prefers the remote port number, so URLs match what the service
// expects, then falls back to a free ephemeral port.
func localPorts(remote int) []int {
	ports := []int{}
	if l, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(remote))); err == nil {
		l.Close()
		ports = append(ports, remote)
	}
	for range 2 {
		if l, err := net.Listen("tcp4", "127.0.0.1:0"); err == nil {
			ports = append(ports, l.Addr().(*net.TCPAddr).Port)
			l.Close()
		}
	}
	return ports
}

func forwardSpec(local int, target netip.Addr, port int) string {
	host := target.String()
	if target.Is6() {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("127.0.0.1:%d:%s:%d", local, host, port)
}

func (t *Tunnels) sshArgs(host domain.Host, path string) []string {
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "PermitLocalCommand=no", "-o", "ControlPath=" + path}
	if host.Port != 0 {
		args = append(args, "-p", strconv.Itoa(host.Port))
	}
	if host.IdentityFile != "" {
		args = append(args, "-i", host.IdentityFile)
	}
	return args
}

// master returns a live connection for host, starting one if needed. The
// caller holds t.changeMu.
func (t *Tunnels) master(ctx context.Context, host domain.Host) (*tunnelMaster, error) {
	t.mu.Lock()
	m, ok := t.masters[host.Name]
	closed := t.closed
	if ok && !(m.alive() && m.host == host) {
		delete(t.masters, host.Name)
	}
	t.mu.Unlock()
	if closed {
		return nil, errors.New("tunnels are closed")
	}
	if ok {
		if m.alive() && m.host == host {
			return m, nil
		}
		t.stop(m)
	}
	// Distinct from the scan socket so the two connections never interfere.
	path := filepath.Join(t.controlDir, "t"+controlName(host))
	_ = os.Remove(path) // a stale socket from a killed session
	seconds := int(t.timeout.Seconds() + 0.999)
	// ClearAllForwardings drops forwards from SSH config, so only requested
	// ports are opened; runtime -O forward requests are unaffected.
	args := append([]string{"-N", "-M", "-o", "ControlPersist=no", "-o", "ClearAllForwardings=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(seconds), "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"}, t.sshArgs(host, path)...)
	args = append(args, "--", host.SSH)
	cmd := exec.Command("ssh", args...)
	stderr := &cappedBuffer{limit: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: start ssh: %w", host.Name, err)
	}
	m = &tunnelMaster{host: host, path: path, cmd: cmd, done: make(chan struct{}), forwards: map[string]domain.Forward{}}
	go func() { _ = cmd.Wait(); close(m.done) }()
	// The control socket appears once the connection is authenticated.
	deadline := time.NewTimer(t.timeout)
	defer deadline.Stop()
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			t.mu.Lock()
			closed := t.closed // Close ran while this connection was starting
			if !closed {
				t.masters[host.Name] = m
			}
			t.mu.Unlock()
			if closed {
				t.stop(m)
				return nil, errors.New("tunnels are closed")
			}
			return m, nil
		}
		select {
		case <-m.done:
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = "connection closed"
			}
			return nil, fmt.Errorf("%s: ssh tunnel failed: %s", host.Name, detail)
		case <-ctx.Done():
			t.stop(m)
			return nil, ctx.Err()
		case <-deadline.C:
			t.stop(m)
			return nil, fmt.Errorf("%s: ssh tunnel timed out", host.Name)
		case <-poll.C:
		}
	}
}

func (t *Tunnels) control(ctx context.Context, m *tunnelMaster, operation, spec string) error {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	args := append(t.sshArgs(m.host, m.path), "-O", operation, "-L", spec, "--", m.host.SSH)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	stderr := &cappedBuffer{limit: 4096}
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

func (t *Tunnels) stop(m *tunnelMaster) {
	if m.alive() && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
	}
	select {
	case <-m.done:
	case <-time.After(2 * time.Second):
	}
	_ = os.Remove(m.path)
}

// CloseForward removes one remote forward, ending the host connection when
// none remain. Direct local URLs have nothing to close.
func (t *Tunnels) CloseForward(f domain.Forward) error {
	if f.Direct {
		return nil
	}
	t.changeMu.Lock()
	defer t.changeMu.Unlock()
	t.mu.Lock()
	m, ok := t.masters[f.Host]
	if ok {
		_, ok = m.forwards[f.Remote]
	}
	if !ok {
		t.mu.Unlock()
		return nil
	}
	delete(m.forwards, f.Remote)
	last := len(m.forwards) == 0
	if last {
		delete(t.masters, f.Host)
	}
	t.mu.Unlock()
	if last {
		t.stop(m)
		return nil
	}
	host, portText, _ := net.SplitHostPort(f.Remote)
	port, _ := strconv.Atoi(portText)
	return t.control(context.Background(), m, "cancel", forwardSpec(f.LocalPort, netip.MustParseAddr(host), port))
}

// Active lists forwards on live connections, ordered by host and endpoint.
func (t *Tunnels) Active() []domain.Forward {
	t.mu.Lock()
	defer t.mu.Unlock()
	forwards := []domain.Forward{}
	for name, m := range t.masters {
		if !m.alive() {
			delete(t.masters, name)
			continue
		}
		for _, f := range m.forwards {
			forwards = append(forwards, f)
		}
	}
	sort.Slice(forwards, func(i, j int) bool {
		if forwards[i].Host != forwards[j].Host {
			return forwards[i].Host < forwards[j].Host
		}
		return forwards[i].Remote < forwards[j].Remote
	})
	return forwards
}

// Close ends every tunnel connection without waiting for an open in
// progress; that open stops its own connection. Later opens are refused.
func (t *Tunnels) Close() error {
	t.mu.Lock()
	masters := t.masters
	t.masters, t.closed = map[string]*tunnelMaster{}, true
	t.mu.Unlock()
	for _, m := range masters {
		t.stop(m)
	}
	return nil
}

func (t *Tunnels) Browse(url string) error { return browse(url) }
