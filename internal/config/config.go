// Package config loads portable host settings. It never modifies SSH configuration.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joeynyc/lhwatch/internal/domain"
)

type Config struct {
	RefreshSeconds int           `json:"refresh_seconds"`
	TimeoutSeconds int           `json:"timeout_seconds"`
	Hosts          []domain.Host `json:"hosts"`
}

func (c Config) Interval() time.Duration { return time.Duration(c.RefreshSeconds) * time.Second }
func (c Config) Timeout() time.Duration  { return time.Duration(c.TimeoutSeconds) * time.Second }

func DefaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "lhwatch", "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "lhwatch", "config.json")
}

// Load merges defaults, an optional file, then additional --host name=destination
// entries. Host names must be unique; flags never silently replace a file entry.
func Load(path string, explicit bool, extraHosts []string) (Config, error) {
	c := Config{RefreshSeconds: 3, TimeoutSeconds: 8}
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			if explicit || !os.IsNotExist(err) {
				return c, fmt.Errorf("read config: %w", err)
			}
		} else {
			defer f.Close()
			d := json.NewDecoder(io.LimitReader(f, 1024*1024))
			d.DisallowUnknownFields()
			decoded := &c
			if err := d.Decode(&decoded); err != nil {
				return c, fmt.Errorf("parse config: %w", err)
			}
			if decoded == nil {
				return c, fmt.Errorf("config must be a JSON object")
			}
			if err := d.Decode(new(any)); err != io.EOF {
				return c, fmt.Errorf("config must contain one JSON object")
			}
		}
	} else if explicit {
		return c, fmt.Errorf("config path is empty")
	}
	if c.RefreshSeconds < 1 || c.RefreshSeconds > 3600 {
		return c, fmt.Errorf("refresh_seconds must be between 1 and 3600")
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 60 {
		return c, fmt.Errorf("timeout_seconds must be between 1 and 60")
	}
	localCount := 0
	for _, h := range c.Hosts {
		if h.IsLocal() {
			localCount++
		}
	}
	if localCount == 0 {
		c.Hosts = append([]domain.Host{{Name: "local"}}, c.Hosts...)
	}
	if localCount > 1 {
		return c, fmt.Errorf("configure only one local host (entry without ssh)")
	}
	for _, value := range extraHosts {
		name, destination, ok := strings.Cut(value, "=")
		if !ok || destination == "" {
			return c, fmt.Errorf("--host must be name=user@host or name=ssh-alias")
		}
		c.Hosts = append(c.Hosts, domain.Host{Name: name, SSH: destination})
	}
	seen := make(map[string]bool)
	for i := range c.Hosts {
		h := &c.Hosts[i]
		if err := ValidateHost(*h); err != nil {
			return c, err
		}
		if seen[h.Name] {
			return c, fmt.Errorf("duplicate host name %q", h.Name)
		}
		seen[h.Name] = true
		if strings.HasPrefix(h.IdentityFile, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return c, err
			}
			h.IdentityFile = filepath.Join(home, h.IdentityFile[2:])
		}
	}
	return c, nil
}

var hostName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,31}$`)
var destination = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]*@)?[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

// ValidateHost prevents option injection before an SSH destination reaches exec.
// Custom ports and identities are separate fields; complex routing belongs in SSH config.
func ValidateHost(h domain.Host) error {
	if !hostName.MatchString(h.Name) {
		return fmt.Errorf("host name %q must be 1–32 letters, numbers, spaces, dots, underscores or hyphens", h.Name)
	}
	if !h.IsLocal() && !destination.MatchString(h.SSH) {
		return fmt.Errorf("host %q: invalid ssh destination; use an SSH alias or user@hostname", h.Name)
	}
	// IPv6 literals contain several colons; one colon is a host:port attempt,
	// which ssh would treat as part of the hostname.
	if address := h.SSH[strings.LastIndex(h.SSH, "@")+1:]; !h.IsLocal() && strings.Count(address, ":") == 1 {
		return fmt.Errorf("host %q: ssh destination cannot include a port; set \"port\" in the config file or use an SSH config alias", h.Name)
	}
	if h.Port < 0 || h.Port > 65535 {
		return fmt.Errorf("host %q: port must be between 1 and 65535 (or omitted)", h.Name)
	}
	if h.IsLocal() && (h.Port != 0 || h.IdentityFile != "") {
		return fmt.Errorf("host %q: port and identity_file require ssh", h.Name)
	}
	if strings.ContainsAny(h.IdentityFile, "\x00\r\n") {
		return fmt.Errorf("host %q: invalid identity_file", h.Name)
	}
	return nil
}
