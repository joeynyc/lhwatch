//go:build unix

package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joeynyc/lhwatch/internal/domain"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	// t.TempDir on macOS exceeds the socket-path budget.
	dir, err := os.MkdirTemp("/tmp", "lhw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestPrivateControlDirIsCreatedPrivately(t *testing.T) {
	base := shortTempDir(t)
	dir, err := privateControlDir(base)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("control dir mode: %v %v", info.Mode(), err)
	}
	if again, err := privateControlDir(base); err != nil || again != dir {
		t.Fatalf("existing private dir rejected: %q %v", again, err)
	}
}

func TestPrivateControlDirRejectsUnsafeDirectories(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"shared permissions", func(t *testing.T, dir string) {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o777); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, dir string) {
			if err := os.Symlink(t.TempDir(), dir); err != nil {
				t.Fatal(err)
			}
		}},
		{"regular file", func(t *testing.T, dir string) {
			if err := os.WriteFile(dir, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := shortTempDir(t)
			tc.setup(t, filepath.Join(base, "lhwatch-"+strconv.Itoa(os.Getuid())))
			if dir, err := privateControlDir(base); err == nil {
				t.Fatalf("unsafe control dir accepted: %s", dir)
			}
		})
	}
	for _, base := range []string{"/tmp/" + strings.Repeat("x", 80), "/tmp/with space", "/tmp/percent%h"} {
		if dir, err := privateControlDir(base); err == nil {
			t.Fatalf("unsuitable base accepted: %s", dir)
		}
	}
}

// The fake ssh records each invocation, creates the control socket path like
// a master would, and runs the helper for ordinary commands.
const recordingSSH = `
args = sys.argv[1:]
with open(os.environ['LHWATCH_TEST_ARGS'], 'a') as log:
    log.write(json.dumps(args) + '\n')
paths = [a.split('=', 1)[1] for a in args if a.startswith('ControlPath=')]
if '-O' in args:
    sys.exit(0)
if paths and '-o' in args and 'ControlMaster=auto' in args:
    open(paths[0], 'w').close()
parts = args[-1].split(' ')
os.execv(sys.executable, [sys.executable] + parts[1:])
`

func invocations(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, args)
	}
	return calls
}

func option(args []string, name string) string {
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, name+"="); ok {
			return value
		}
	}
	return ""
}

func TestMultiplexedCollectorSharesAndClosesConnections(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls.jsonl")
	t.Setenv("LHWATCH_TEST_ARGS", log)
	fakeSSH(t, recordingSSH)
	c := New(15 * time.Second)
	c.controlDir = shortTempDir(t)
	alpha := domain.Host{Name: "alpha", SSH: "dev@alpha.test"}
	keyed := domain.Host{Name: "alpha key", SSH: "dev@alpha.test", IdentityFile: "/keys/other"}
	for _, host := range []domain.Host{alpha, alpha, keyed} {
		if _, err := c.Scan(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	calls := invocations(t, log)
	if len(calls) != 3 {
		t.Fatalf("expected 3 scans, got %d", len(calls))
	}
	first, second, third := option(calls[0], "ControlPath"), option(calls[1], "ControlPath"), option(calls[2], "ControlPath")
	if first == "" || first != second || filepath.Dir(first) != c.controlDir {
		t.Fatalf("scans of one host must share a control socket: %q %q", first, second)
	}
	if third == first {
		t.Fatal("a different identity file must not reuse another connection")
	}
	for _, want := range []string{"ControlMaster=auto", "ControlPersist=60", "ServerAliveInterval=5"} {
		if !slices.Contains(calls[0], want) {
			t.Fatalf("missing %s in %q", want, calls[0])
		}
	}
	if i := slices.Index(calls[0], "--"); i < 0 || slices.Index(calls[0], "ControlMaster=auto") > i {
		t.Fatalf("multiplexing options must precede the destination: %q", calls[0])
	}

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	exits := map[string]bool{}
	for _, args := range invocations(t, log)[3:] {
		if !slices.Contains(args, "-O") || args[slices.Index(args, "-O")+1] != "exit" || args[len(args)-1] != "dev@alpha.test" {
			t.Fatalf("unexpected close invocation: %q", args)
		}
		exits[option(args, "ControlPath")] = true
	}
	if len(exits) != 2 || !exits[first] || !exits[third] {
		t.Fatalf("Close must end each established connection once: %v", exits)
	}

	// A scan still in flight when the dashboard exits must not start a new master.
	if _, err := c.Scan(context.Background(), alpha); err != nil {
		t.Fatal(err)
	}
	calls = invocations(t, log)
	if option(calls[len(calls)-1], "ControlPath") != "" {
		t.Fatal("closed collector reused connections")
	}
}

func TestPlainCollectorAndLocalScansNeverMultiplex(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls.jsonl")
	t.Setenv("LHWATCH_TEST_ARGS", log)
	fakeSSH(t, recordingSSH)
	if _, err := New(15*time.Second).Scan(context.Background(), domain.Host{Name: "remote", SSH: "host"}); err != nil {
		t.Fatal(err)
	}
	if args := invocations(t, log)[0]; option(args, "ControlPath") != "" {
		t.Fatalf("plain collector multiplexed: %q", args)
	}
	c := New(15 * time.Second)
	c.controlDir = shortTempDir(t)
	if _, err := c.Scan(context.Background(), domain.Host{Name: "local"}); err != nil {
		t.Fatal(err)
	}
	if len(c.masters) != 0 || len(invocations(t, log)) != 1 {
		t.Fatal("local scans must not use SSH or record a master")
	}
	if err := c.Close(); err != nil || len(invocations(t, log)) != 1 {
		t.Fatal("Close must not contact hosts without an established connection")
	}
}
