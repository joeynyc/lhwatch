package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingOptionalConfigIsLocal(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.json"), false, nil)
	if err != nil || len(c.Hosts) != 1 || !c.Hosts[0].IsLocal() {
		t.Fatalf("local default: %+v, %v", c, err)
	}
}

func TestConfigAndFlags(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"refresh_seconds":4,"hosts":[{"name":"lab","ssh":"user@gpubox"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, true, []string{"second=user@other"})
	if err != nil {
		t.Fatal(err)
	}
	if c.RefreshSeconds != 4 || len(c.Hosts) != 3 || c.Hosts[0].Name != "local" || c.Hosts[2].SSH != "user@other" {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestExplicitLocalNotDuplicated(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"hosts":[{"name":"my mac"}]}`), 0600)
	c, err := Load(p, true, nil)
	if err != nil || len(c.Hosts) != 1 || c.Hosts[0].Name != "my mac" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, body := range []string{
		`null`, `{"hosts":`, `{"hostz":[]}`, `{} {}`, `{"refresh_seconds":0}`,
		`{"hosts":[{"name":"same","ssh":"gpubox"},{"name":"same","ssh":"other"}]}`,
		`{"hosts":[{"name":"one"},{"name":"two"}]}`,
		`{"hosts":[{"name":"bad","ssh":"-oProxyCommand=oops"}]}`,
		`{"hosts":[{"name":"bad","ssh":"user@host; echo bad"}]}`,
		`{"hosts":[{"name":"bad","ssh":"host","port":65536}]}`,
		`{"hosts":[{"name":"bad\u001b[2J","ssh":"host"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.json")
			os.WriteFile(p, []byte(body), 0600)
			if _, err := Load(p, true, nil); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestHostFlagRejectsDuplicatesAndUnsafeDestination(t *testing.T) {
	for _, flags := range [][]string{{"lab=a", "lab=b"}, {"local=user@gpubox"}, {"bad=-host"}, {"bad=user@host$(id)"}} {
		if _, err := Load("", false, flags); err == nil {
			t.Fatalf("accepted %v", flags)
		}
	}
}

func TestExplicitMissingConfigErrors(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.json"), true, nil)
	if err == nil || !strings.Contains(err.Error(), "config") {
		t.Fatalf("%v", err)
	}
}

func TestDestinationPortNeedsPortField(t *testing.T) {
	for _, dest := range []string{"gpubox:2222", "you@gpubox:2222", "you@gpubox:"} {
		_, err := Load("", false, []string{"lab=" + dest})
		if err == nil || !strings.Contains(err.Error(), `set "port"`) {
			t.Fatalf("%s: want port hint, got %v", dest, err)
		}
	}
	for _, dest := range []string{"you@fe80::1", "you@gpubox.local"} {
		if _, err := Load("", false, []string{"lab=" + dest}); err != nil {
			t.Fatalf("%s rejected: %v", dest, err)
		}
	}
}
