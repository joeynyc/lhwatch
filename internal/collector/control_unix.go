//go:build unix

package collector

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

// Unix socket paths are limited to about 104 bytes, and OpenSSH appends a
// temporary suffix while creating one, so the directory must stay short.
const maxControlDir = 64

// SSH expands % tokens and splits options on whitespace; allow neither.
var plainPath = regexp.MustCompile(`^[A-Za-z0-9/._-]+$`)

// controlBase prefers the per-user runtime directory. macOS has none and its
// TMPDIR is too long for socket paths, so the fallback is /tmp.
func controlBase() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(dir) {
		return dir
	}
	return "/tmp"
}

// privateControlDir creates or verifies a directory only this user can use.
// A socket planted by someone else would let them relay our SSH sessions,
// so a directory that is shared, foreign, or a symlink disables reuse.
func privateControlDir(base string) (string, error) {
	dir := filepath.Join(base, fmt.Sprintf("lhwatch-%d", os.Getuid()))
	if !plainPath.MatchString(dir) || len(dir) > maxControlDir {
		return "", fmt.Errorf("unsuitable SSH control directory %q", dir)
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(owner.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("SSH control directory %s is not private to this user", dir)
	}
	return dir, nil
}
