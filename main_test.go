package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/joeynyc/lhwatch/internal/domain"
)

func TestDemoJSONNeverNeedsRealHosts(t *testing.T) {
	var out, errout bytes.Buffer
	code := run([]string{"--demo", "--json", "--host", "never=not-a-real-host.invalid"}, &out, &errout)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errout.String())
	}
	var report domain.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || len(report.Hosts) < 2 {
		t.Fatalf("invalid report: %+v", report)
	}
	for _, h := range report.Hosts {
		if h.Host == "never" || h.Error != "" {
			t.Fatalf("real config reached demo: %+v", h)
		}
	}
}

func TestBadCLIInput(t *testing.T) {
	for _, args := range [][]string{{"--bogus"}, {"unexpected"}, {"--config", "/nonexistent/lhwatch/config.json", "--json"}, {"--refresh", "0", "--json"}, {"--host", "bad", "--json"}} {
		var out, errout bytes.Buffer
		if code := run(args, &out, &errout); code != 2 {
			t.Fatalf("%v: got %d: %s", args, code, errout.String())
		}
		if out.Len() != 0 || errout.Len() == 0 {
			t.Fatalf("diagnostics must be on stderr: %v", args)
		}
	}
}

func TestVersionAndHelp(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"--help"}} {
		var out, errout bytes.Buffer
		if code := run(args, &out, &errout); code != 0 || out.Len()+errout.Len() == 0 {
			t.Fatalf("%v: %d", args, code)
		}
	}
}

type failingBackend struct{ domain.Backend }

func (failingBackend) Scan(context.Context, domain.Host) (domain.Snapshot, error) {
	return domain.Snapshot{}, errors.New("unreachable")
}

func TestFailedHostJSONOmitsPlaceholderFields(t *testing.T) {
	report := collect(context.Background(), []domain.Host{{Name: "down", SSH: "down.invalid"}}, failingBackend{})
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Hosts []map[string]any `json:"hosts"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	host := decoded.Hosts[0]
	if host["error"] != "unreachable" || host["host"] != "down" {
		t.Fatalf("failed host lost its identity or error: %v", host)
	}
	if services, ok := host["services"].([]any); !ok || len(services) != 0 {
		t.Fatalf("failed host must report empty services: %v", host["services"])
	}
	for _, field := range []string{"collected_at", "platform"} {
		if _, present := host[field]; present {
			t.Fatalf("failed host reported placeholder %s: %v", field, host[field])
		}
	}
}

func TestReleaseVersionOverride(t *testing.T) {
	defer func(previous string) { version = previous }(version)
	version = "v1.2.3"
	var out, errout bytes.Buffer
	if code := run([]string{"--version"}, &out, &errout); code != 0 || out.String() != "lhwatch v1.2.3\n" {
		t.Fatalf("%d %q", code, out.String())
	}
	version = ""
	out.Reset()
	run([]string{"--version"}, &out, &errout)
	if !strings.HasPrefix(out.String(), "lhwatch ") || strings.TrimSpace(strings.TrimPrefix(out.String(), "lhwatch ")) == "" {
		t.Fatalf("missing fallback version: %q", out.String())
	}
}
