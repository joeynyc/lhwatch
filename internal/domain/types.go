// Package domain contains the wire contract shared by the terminal and collectors.
package domain

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"
)

const SchemaVersion = 1

type Host struct {
	Name         string `json:"name"`
	SSH          string `json:"ssh,omitempty"`
	Port         int    `json:"port,omitempty"`
	IdentityFile string `json:"identity_file,omitempty"`
}

func (h Host) IsLocal() bool { return h.SSH == "" }

// Service is one observed listening endpoint. Several endpoints may share a PID.
// Identity is an opaque, host-generated process incarnation token, not a PID.
type Service struct {
	Host                string `json:"host"`
	Project             string `json:"project,omitempty"`
	ProjectPath         string `json:"project_path,omitempty"`
	ProjectSource       string `json:"project_source,omitempty"`
	ContainerID         string `json:"container_id,omitempty"`
	ContainerName       string `json:"container_name,omitempty"`
	ContainerImage      string `json:"container_image,omitempty"`
	VisibilityReason    string `json:"visibility_reason,omitempty"`
	PID                 int    `json:"pid"`
	Name                string `json:"name"`
	Command             string `json:"command,omitempty"`
	Cwd                 string `json:"cwd,omitempty"`
	User                string `json:"user,omitempty"`
	UID                 int    `json:"uid"`
	Address             string `json:"address"`
	Port                int    `json:"port"`
	Family              string `json:"family"`
	EndpointLabel       string `json:"endpoint_label,omitempty"`
	EndpointLabelSource string `json:"endpoint_label_source,omitempty"`
	Started             string `json:"started,omitempty"`
	Identity            string `json:"identity,omitempty"`
	CanStop             bool   `json:"can_stop"`
	StopReason          string `json:"stop_reason,omitempty"`
}

const UnidentifiedServiceName = "Unidentified service"

// DisplayName keeps attribution separate from the process identity used to stop.
func (s Service) DisplayName() string {
	if s.Project != "" {
		return s.Project
	}
	if s.Name != "" && s.Name != "unknown" {
		return s.Name
	}
	return UnidentifiedServiceName
}

func (s Service) Key() string {
	return fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%d\x00%s", s.Host, s.PID, s.Identity, s.Address, s.Port, s.Family)
}

// GroupKey joins only endpoints with evidence of shared ownership. Endpoint
// identity remains separate so grouping never changes a signal target.
func (s Service) GroupKey() string {
	if s.ContainerID != "" {
		return fmt.Sprintf("%s\x00container\x00%s", s.Host, s.ContainerID)
	}
	if s.PID > 0 && s.Identity != "" {
		return fmt.Sprintf("%s\x00process\x00%d\x00%s", s.Host, s.PID, s.Identity)
	}
	return "endpoint\x00" + s.Key()
}

func (s Service) Endpoint() string { return net.JoinHostPort(s.Address, strconv.Itoa(s.Port)) }

type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	Host          string    `json:"host"`
	Platform      string    `json:"platform,omitempty"`
	Hostname      string    `json:"hostname,omitempty"`
	Services      []Service `json:"services"`
	Warnings      []string  `json:"warnings,omitempty"`
	Partial       bool      `json:"partial"`
	CollectedAt   time.Time `json:"collected_at,omitzero"` // omitted when a scan failed
	Error         string    `json:"error,omitempty"`
}

type StopRequest struct {
	PID      int    `json:"pid"`
	Identity string `json:"identity"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Family   string `json:"family"`
}

type StopResult struct {
	SchemaVersion int    `json:"schema_version"`
	Sent          bool   `json:"sent"`
	Message       string `json:"message"`
}

// Backend operations are synchronous; the UI runs each in a Bubble Tea command.
// Implementations must honor context cancellation and be safe for parallel hosts.
type Backend interface {
	Scan(context.Context, Host) (Snapshot, error)
	Stop(context.Context, Host, Service) (StopResult, error)
}

type Report struct {
	SchemaVersion int        `json:"schema_version"`
	CollectedAt   time.Time  `json:"collected_at"`
	Hosts         []Snapshot `json:"hosts"`
}

// Forward is a listener made reachable from this machine. Direct forwards are
// local listeners opened without a tunnel; others are SSH local forwards.
type Forward struct {
	Host      string
	Remote    string // dialable endpoint on the host, such as 127.0.0.1:8000
	URL       string
	LocalPort int
	Direct    bool
}

// Forwarder opens listeners from this machine. Open may block on SSH, so the
// UI runs it in a Bubble Tea command; implementations must be concurrency-safe.
type Forwarder interface {
	Open(context.Context, Host, Service) (Forward, error)
	CloseForward(Forward) error
	Active() []Forward
	Browse(url string) error
}
