// Package collector runs the same ephemeral Python helper locally and over SSH.
package collector

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joeynyc/lhwatch/internal/config"
	"github.com/joeynyc/lhwatch/internal/domain"
)

//go:embed helper.py
var helper string

const outputLimit = 16 * 1024 * 1024

// controlPersist bounds how long a shared SSH connection outlives its last
// use, including after an interrupted dashboard that never reached Close.
const controlPersist = "60"

type Collector struct {
	timeout time.Duration
	// controlDir is a private directory for SSH control sockets; empty
	// disables connection reuse.
	controlDir string

	mu      sync.Mutex
	closed  bool
	masters map[string]domain.Host // control path → host, ended by Close
}

func New(timeout time.Duration) *Collector {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	return &Collector{timeout: timeout}
}

// NewMultiplexed reuses one OpenSSH connection per remote host across scans
// and stops, avoiding a new handshake on every refresh. Without a private
// socket directory it behaves exactly like New. Call Close when finished.
func NewMultiplexed(timeout time.Duration) *Collector {
	c := New(timeout)
	if dir, err := privateControlDir(controlBase()); err == nil {
		c.controlDir = dir
	}
	return c
}

// controlName is a short, per-destination socket name. Identity and port are
// included so hosts with different credentials never share a connection.
func controlName(host domain.Host) string {
	sum := sha256.Sum256([]byte(host.SSH + "\x00" + strconv.Itoa(host.Port) + "\x00" + host.IdentityFile))
	return hex.EncodeToString(sum[:8])
}

func (c *Collector) controlPath(host domain.Host) string {
	return filepath.Join(c.controlDir, controlName(host))
}

// multiplexArgs returns connection-sharing options, recording the socket so
// Close can end it. Options are passed per command; SSH config is untouched.
func (c *Collector) multiplexArgs(host domain.Host) []string {
	if c.controlDir == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	path := c.controlPath(host)
	if c.masters == nil {
		c.masters = map[string]domain.Host{}
	}
	c.masters[path] = host
	// Keepalives let a master on a dead link exit, so later scans reconnect.
	return []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + path, "-o", "ControlPersist=" + controlPersist, "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=2"}
}

// Close asks each shared SSH connection to exit and disables further reuse.
// ControlPersist still ends any connection this request cannot reach.
func (c *Collector) Close() error {
	c.mu.Lock()
	masters := c.masters
	c.masters, c.closed = nil, true
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for path, host := range masters {
		if _, err := os.Stat(path); err != nil {
			continue // no connection was established
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ControlPath="+path, "-O", "exit", "--", host.SSH)
			cmd.WaitDelay = time.Second
			_ = cmd.Run()
		}()
	}
	wg.Wait()
	return nil
}

var _ domain.Backend = (*Collector)(nil)

type request struct {
	SchemaVersion  int                 `json:"schema_version"`
	Action         string              `json:"action"`
	Target         *domain.StopRequest `json:"target,omitempty"`
	TimeoutSeconds float64             `json:"timeout_seconds,omitempty"`
}

// cappedBuffer keeps draining output after the limit, without allocating more.
// exec cancellation therefore remains responsible for killing a noisy child.
type cappedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	count := len(p)
	room := b.limit - b.Len()
	if len(p) > room {
		p = p[:room]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return count, nil
}

func (c *Collector) invoke(ctx context.Context, host domain.Host, req request) ([]byte, error) {
	if err := config.ValidateHost(host); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req.TimeoutSeconds = c.timeout.Seconds()
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	token := base64.StdEncoding.EncodeToString(payload)
	name, args := "python3", []string{"-", token}
	if !host.IsLocal() {
		seconds := int(math.Ceil(c.timeout.Seconds()))
		args = []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "ConnectTimeout=" + strconv.Itoa(seconds)}
		if host.Port != 0 {
			args = append(args, "-p", strconv.Itoa(host.Port))
		}
		if host.IdentityFile != "" {
			args = append(args, "-i", host.IdentityFile)
		}
		args = append(args, c.multiplexArgs(host)...)
		// The remote shell only sees this constant command and a base64 token,
		// never arbitrary request data or interpolated host configuration.
		args = append(args, "--", host.SSH, "python3 - "+token)
		name = "ssh"
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(helper)
	stdout := &cappedBuffer{limit: outputLimit}
	stderr := &cappedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s: %w", host.Name, ctx.Err())
	}
	if stdout.overflow {
		return nil, fmt.Errorf("%s: collector output exceeded %d bytes", host.Name, outputLimit)
	}
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(stdout.Bytes(), &failure) == nil && failure.Error != "" {
			detail = failure.Error
		}
		if len(detail) > 4096 {
			detail = detail[:4096]
		}
		if detail != "" {
			return nil, fmt.Errorf("%s: %s failed: %w: %s", host.Name, name, err, detail)
		}
		return nil, fmt.Errorf("%s: %s failed: %w", host.Name, name, err)
	}
	return stdout.Bytes(), nil
}

func decode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid collector response: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("collector response must contain one JSON object")
	}
	return nil
}

func (c *Collector) Scan(ctx context.Context, host domain.Host) (domain.Snapshot, error) {
	snapshot := domain.Snapshot{}
	data, err := c.invoke(ctx, host, request{SchemaVersion: domain.SchemaVersion, Action: "scan"})
	if err != nil {
		return snapshot, err
	}
	if err = decode(data, &snapshot); err != nil {
		return domain.Snapshot{}, err
	}
	if snapshot.Error != "" {
		return domain.Snapshot{}, fmt.Errorf("%s: %s", host.Name, snapshot.Error)
	}
	if snapshot.SchemaVersion != domain.SchemaVersion || snapshot.CollectedAt.IsZero() || snapshot.Services == nil || (snapshot.Platform != "linux" && snapshot.Platform != "darwin") {
		return domain.Snapshot{}, fmt.Errorf("%s: incomplete or unsupported collector snapshot", host.Name)
	}
	snapshot.Host = host.Name
	for i := range snapshot.Services {
		s := &snapshot.Services[i]
		if s.PID < 0 || s.UID < -1 || s.Port < 1 || s.Port > 65535 || s.Address == "" || (s.Family != "IPv4" && s.Family != "IPv6") || (s.CanStop && (s.PID <= 1 || s.Identity == "" || s.UID < 0)) {
			return domain.Snapshot{}, fmt.Errorf("%s: invalid service in collector response", host.Name)
		}
		s.Host = host.Name
	}
	return snapshot, nil
}

func (c *Collector) Stop(ctx context.Context, host domain.Host, service domain.Service) (domain.StopResult, error) {
	if service.ContainerID != "" {
		return domain.StopResult{}, fmt.Errorf("use Docker to stop this container; container controls are unavailable in LHWatch")
	}
	target := &domain.StopRequest{PID: service.PID, Identity: service.Identity, Address: service.Address, Port: service.Port, Family: service.Family}
	if service.Host != host.Name {
		return domain.StopResult{}, fmt.Errorf("service host does not match selected host")
	}
	if target.PID <= 1 || target.Identity == "" || target.Port < 1 || target.Port > 65535 || target.Address == "" || (target.Family != "IPv4" && target.Family != "IPv6") {
		return domain.StopResult{}, fmt.Errorf("refused: missing process identity or invalid endpoint")
	}
	data, err := c.invoke(ctx, host, request{SchemaVersion: domain.SchemaVersion, Action: "stop", Target: target})
	if err != nil {
		return domain.StopResult{}, err
	}
	var result domain.StopResult
	if err := decode(data, &result); err != nil {
		return domain.StopResult{}, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	if result.SchemaVersion != domain.SchemaVersion || result.Message == "" || fields["sent"] == nil || string(fields["sent"]) == "null" {
		return domain.StopResult{}, fmt.Errorf("%s: incomplete or unsupported stop result", host.Name)
	}
	return result, nil
}
