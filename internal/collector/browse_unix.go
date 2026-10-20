//go:build unix

package collector

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

var errNoBrowser = errors.New("no graphical browser available in this session")

// browse opens url only where a graphical browser can appear to this user.
func browse(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		// Over SSH, open would use the Mac's desktop, out of the user's view.
		if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
			return errNoBrowser
		}
		name = "open"
	} else if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		// Without a display, xdg-open may start a text browser on this terminal.
		return errNoBrowser
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return errNoBrowser
	}
	// Standard streams default to /dev/null, and a new session keeps the
	// opener away from the dashboard's controlling terminal.
	cmd := exec.Command(path, url)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
