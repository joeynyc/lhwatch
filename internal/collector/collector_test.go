package collector

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joeynyc/lhwatch/internal/domain"
)

func python(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	return path
}

func fakeSSH(t *testing.T, body string) string {
	t.Helper()
	py := python(t)
	dir := t.TempDir()
	script := "#!" + py + "\nimport os, sys, json, time\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestSSHTransportExecutesEmbeddedHelper(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("LHWATCH_TEST_ARGS", argsFile)
	fakeSSH(t, "open(os.environ['LHWATCH_TEST_ARGS'], 'w').write(json.dumps(sys.argv[1:]))\nparts=sys.argv[-1].split(' ')\nassert parts[:2] == ['python3', '-'] and len(parts)==3\nos.execv(sys.executable,[sys.executable]+parts[1:])")
	host := domain.Host{Name: "test remote", SSH: "user@example.test", Port: 2222, IdentityFile: "/tmp/a key with spaces"}
	snapshot, err := New(15*time.Second).Scan(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Host != host.Name || snapshot.SchemaVersion != 1 {
		t.Fatalf("bad snapshot: %+v", snapshot)
	}
	for _, s := range snapshot.Services {
		if s.Host != host.Name {
			t.Fatalf("unstamped service: %+v", s)
		}
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		t.Fatal(err)
	}
	expected := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "ConnectTimeout=15", "-p", "2222", "-i", host.IdentityFile, "--", host.SSH}
	if len(args) != len(expected)+1 {
		t.Fatalf("unexpected args: %q", args)
	}
	for i, value := range expected {
		if args[i] != value {
			t.Fatalf("arg %d: got %q want %q", i, args[i], value)
		}
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(args[len(args)-1], "python3 - "))
	if err != nil {
		t.Fatal(err)
	}
	var req request
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatal(err)
	}
	if req.Action != "scan" || req.SchemaVersion != 1 || req.TimeoutSeconds != 15 {
		t.Fatalf("bad request: %+v", req)
	}
}

func TestTransportFailures(t *testing.T) {
	host := domain.Host{Name: "remote", SSH: "host"}
	for _, tc := range []struct{ name, body, want string }{
		{"nonzero", "sys.stderr.write('permission denied'); sys.exit(7)", "permission denied"},
		{"python missing", "sys.stderr.write('python3: command not found'); sys.exit(127)", "python3: command not found"},
		{"structured failure", `print('{"schema_version":1,"error":"lsof failed: unavailable"}'); sys.exit(1)`, "lsof failed: unavailable"},
		{"malformed", "print('not json')", "invalid collector response"},
		{"unknown fields", `print('{"schema_version":1,"unexpected":true}')`, "unknown field"},
		{"incomplete", `print('{"schema_version":1,"services":[]}')`, "incomplete"},
		{"null", `print('null')`, "incomplete"},
		{"stderr bound", "sys.stderr.write('x'*10000); sys.exit(1)", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeSSH(t, tc.body)
			_, err := New(time.Second*3).Scan(context.Background(), host)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v want %q", err, tc.want)
			}
			if len(err.Error()) > 4300 {
				t.Fatalf("unbounded error length: %d", len(err.Error()))
			}
		})
	}
}

func TestEmptySnapshotAndStamping(t *testing.T) {
	fakeSSH(t, `print('{"schema_version":1,"host":"spoofed","platform":"linux","services":[],"partial":false,"collected_at":"2026-10-01T12:00:00Z"}')`)
	snapshot, err := New(time.Second*3).Scan(context.Background(), domain.Host{Name: "expected", SSH: "host"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Host != "expected" || snapshot.Services == nil || len(snapshot.Services) != 0 {
		t.Fatalf("bad empty snapshot: %+v", snapshot)
	}
}

func TestDeadlineAndCancellation(t *testing.T) {
	fakeSSH(t, "time.sleep(60)")
	for _, cancelFirst := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelFirst {
			cancel()
		}
		started := time.Now()
		_, err := New(100*time.Millisecond).Scan(ctx, domain.Host{Name: "remote", SSH: "host"})
		cancel()
		expected := context.DeadlineExceeded
		if cancelFirst {
			expected = context.Canceled
		}
		if !errors.Is(err, expected) {
			t.Fatalf("got %v want %v", err, expected)
		}
		if time.Since(started) > 2*time.Second {
			t.Fatal("cancellation did not terminate transport promptly")
		}
	}
}

func TestMissingLocalPythonAndInvalidHost(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	c := New(time.Second)
	if _, err := c.Scan(context.Background(), domain.Host{Name: "local"}); err == nil {
		t.Fatal("missing python must fail")
	}
	if _, err := c.Scan(context.Background(), domain.Host{Name: "remote", SSH: "-oProxyCommand=evil"}); err == nil {
		t.Fatal("invalid host must fail")
	}
}

func TestContainerStopRefusedBeforeTransport(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "transport-called")
	t.Setenv("LHWATCH_TEST_MARKER", marker)
	fakeSSH(t, "open(os.environ['LHWATCH_TEST_MARKER'], 'w').write('called')\nprint('{\"schema_version\":1,\"sent\":true,\"message\":\"sent\"}')")
	host := domain.Host{Name: "spark", SSH: "spark-alias"}
	service := domain.Service{Host: "spark", PID: 999991, Identity: "verified", Port: 3000, Address: "127.0.0.1", Family: "IPv4", ContainerID: "container", CanStop: true}
	result, err := New(time.Second).Stop(context.Background(), host, service)
	if err == nil || !strings.Contains(err.Error(), "Docker") || result.Sent {
		t.Fatalf("container controls must be refused: %+v %v", result, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("container stop reached transport")
	}
}

func TestCappedBuffer(t *testing.T) {
	b := &cappedBuffer{limit: 5}
	n, err := b.Write([]byte("123456789"))
	if err != nil || n != 9 || b.String() != "12345" || !b.overflow {
		t.Fatalf("bad bound: %+v %d %v", b, n, err)
	}
	_, _ = b.Write([]byte("more"))
	if b.Len() != 5 {
		t.Fatal("buffer grew beyond limit")
	}
}

func disposableListener(t *testing.T, ipv6, ignoreTERM bool) (*exec.Cmd, int, <-chan error) {
	t.Helper()
	code := fmt.Sprintf("import socket,signal,time\ns=socket.socket(socket.AF_INET6 if %v else socket.AF_INET)\ns.bind(('::1' if %v else '127.0.0.1',0))\ns.listen()\nif %v: signal.signal(signal.SIGTERM,lambda *_: None)\nprint(s.getsockname()[1],flush=True)\nwhile True: time.sleep(1)\n", pyBool(ipv6), pyBool(ipv6), pyBool(ignoreTERM))
	cmd := exec.Command(python(t), "-c", code)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		if ipv6 {
			t.Skipf("IPv6 unavailable: %s", stderr.String())
		}
		t.Fatalf("listener startup: %v %s", err, stderr.String())
	}
	port, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	return cmd, port, done
}
func pyBool(value bool) string {
	if value {
		return "True"
	}
	return "False"
}

func TestDisposableListenerDiscoveryAndStop(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("supported hosts only")
	}
	for _, tc := range []struct {
		name         string
		ipv6, ignore bool
	}{{"ipv4", false, false}, {"ipv6", true, false}, {"ignores term", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, port, done := disposableListener(t, tc.ipv6, tc.ignore)
			c := New(15 * time.Second)
			host := domain.Host{Name: "local"}
			snapshot, err := c.Scan(context.Background(), host)
			if err != nil {
				t.Fatal(err)
			}
			var found *domain.Service
			for i := range snapshot.Services {
				s := &snapshot.Services[i]
				if s.PID == cmd.Process.Pid && s.Port == port {
					found = s
					break
				}
			}
			if found == nil {
				t.Fatalf("disposable listener PID %d port %d not discovered", cmd.Process.Pid, port)
			}
			if !found.CanStop || found.Identity == "" || found.Command == "" {
				t.Fatalf("missing identity/metadata: %+v", found)
			}
			stale := *found
			stale.Identity += "stale"
			result, err := c.Stop(context.Background(), host, stale)
			if err != nil || result.Sent {
				t.Fatalf("stale stop: %+v %v", result, err)
			}
			select {
			case err := <-done:
				t.Fatalf("stale request killed disposable: %v", err)
			default:
			}
			wrongEndpoint := *found
			wrongEndpoint.Port = 1
			result, err = c.Stop(context.Background(), host, wrongEndpoint)
			if err != nil || result.Sent {
				t.Fatalf("changed endpoint stop: %+v %v", result, err)
			}
			// The helper derives authorization; a UI-provided flag is irrelevant.
			found.CanStop = false
			result, err = c.Stop(context.Background(), host, *found)
			if err != nil || !result.Sent {
				t.Fatalf("valid stop: %+v %v", result, err)
			}
			if tc.ignore {
				select {
				case err := <-done:
					t.Fatalf("SIGTERM-ignoring listener was killed: %v", err)
				case <-time.After(250 * time.Millisecond):
				}
			} else {
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("SIGTERM listener did not exit")
				}
			}
		})
	}
}
