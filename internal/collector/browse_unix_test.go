//go:build unix

package collector

import (
	"errors"
	"runtime"
	"testing"
)

// Each case refuses before any opener could run, so no browser ever starts.
func TestBrowseRefusesWhereNoBrowserCanAppear(t *testing.T) {
	cases := map[string]map[string]string{
		"no opener on PATH": {"PATH": t.TempDir(), "SSH_CONNECTION": "", "SSH_TTY": "", "DISPLAY": ":0", "WAYLAND_DISPLAY": ""},
	}
	if runtime.GOOS == "darwin" {
		cases["over SSH"] = map[string]string{"SSH_CONNECTION": "192.0.2.1 50000 192.0.2.2 22", "SSH_TTY": ""}
	} else {
		cases["no display"] = map[string]string{"DISPLAY": "", "WAYLAND_DISPLAY": ""}
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			if err := browse("http://127.0.0.1:9/"); !errors.Is(err, errNoBrowser) {
				t.Fatalf("browse = %v, want errNoBrowser", err)
			}
		})
	}
}
