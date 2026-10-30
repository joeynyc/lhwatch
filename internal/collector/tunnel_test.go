//go:build unix

package collector

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joeynyc/lhwatch/internal/domain"
)

// The fake ssh acts as a control master (creates its socket path and waits
// to be killed) or as a -O client (records the request, optionally failing
// forwards to ports listed in LHWATCH_TEST_BUSY).
const tunnelSSH = `
args = sys.argv[1:]
with open(os.environ['LHWATCH_TEST_ARGS'], 'a') as log:
    log.write(json.dumps(args) + '\n')
path = [a.split('=', 1)[1] for a in args if a.startswith('ControlPath=')][0]
if os.environ.get('LHWATCH_TEST_MASTER_FAIL') and '-M' in args:
    sys.stderr.write('Permission denied (publickey).')
    sys.exit(255)
if '-M' in args:
    open(path, 'w').close()
    time.sleep(3600)
op = args[args.index('-O') + 1]
spec = args[args.index('-L') + 1]
if op == 'forward' and spec.split(':')[1] in os.environ.get('LHWATCH_TEST_BUSY', '').split(','):
    sys.stderr.write('mux_client_forward: forwarding request failed: Port forwarding failed')
    sys.exit(255)
`

func tunnelFixture(t *testing.T) (*Tunnels, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "calls.jsonl")
	t.Setenv("LHWATCH_TEST_ARGS", log)
	fakeSSH(t, tunnelSSH)
	tunnels := NewTunnels(5 * time.Second)
	tunnels.controlDir = shortTempDir(t)
	t.Cleanup(func() { tunnels.Close() })
	return tunnels, log
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func calls(t *testing.T, log string, filter func([]string) bool) [][]string {
	t.Helper()
	if _, err := os.Stat(log); os.IsNotExist(err) {
		return nil
	}
	var matched [][]string
	for _, args := range invocations(t, log) {
		if filter(args) {
			matched = append(matched, args)
		}
	}
	return matched
}

func isMaster(args []string) bool { return slices.Contains(args, "-M") }
func isOp(op string) func([]string) bool {
	return func(args []string) bool {
		i := slices.Index(args, "-O")
		return i >= 0 && args[i+1] == op
	}
}
func spec(args []string) string { return args[slices.Index(args, "-L")+1] }

func remoteService(port int) domain.Service {
	return domain.Service{Host: "spark", Address: "0.0.0.0", Port: port, Family: "IPv4"}
}

var spark = domain.Host{Name: "spark", SSH: "you@spark.test"}

func TestLocalServicesOpenDirectlyWithoutSSH(t *testing.T) {
	tunnels, log := tunnelFixture(t)
	for _, tc := range []struct{ address, url string }{
		{"0.0.0.0", "http://127.0.0.1:3000/"},
		{"::", "http://[::1]:3000/"},
		{"127.0.0.1", "http://127.0.0.1:3000/"},
		{"::ffff:127.0.0.1", "http://127.0.0.1:3000/"},
		{"192.168.1.20", "http://192.168.1.20:3000/"},
	} {
		f, err := tunnels.Open(context.Background(), domain.Host{Name: "local"}, domain.Service{Host: "local", Address: tc.address, Port: 3000})
		if err != nil || f.URL != tc.url || !f.Direct {
			t.Fatalf("%s: %+v %v", tc.address, f, err)
		}
	}
	if got := calls(t, log, func([]string) bool { return true }); len(got) != 0 {
		t.Fatalf("local open ran ssh: %q", got)
	}
	for _, bad := range []domain.Service{{Address: "not-an-ip", Port: 80}, {Address: "fe80::1%en0", Port: 80}, {Address: "127.0.0.1", Port: 0}} {
		if _, err := tunnels.Open(context.Background(), domain.Host{Name: "local"}, bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestRemoteTunnelSharesOneConnectionAndPrefersTheSamePort(t *testing.T) {
	tunnels, log := tunnelFixture(t)
	port, other := freePort(t), freePort(t)
	first, err := tunnels.Open(context.Background(), spark, remoteService(port))
	if err != nil {
		t.Fatal(err)
	}
	if first.Direct || first.LocalPort != port || first.URL != "http://127.0.0.1:"+strconv.Itoa(port)+"/" || first.Remote != "127.0.0.1:"+strconv.Itoa(port) {
		t.Fatalf("unexpected forward: %+v", first)
	}
	again, err := tunnels.Open(context.Background(), spark, remoteService(port))
	if err != nil || again != first {
		t.Fatalf("reopen should reuse the forward: %+v %v", again, err)
	}
	if _, err := tunnels.Open(context.Background(), spark, domain.Service{Host: "spark", Address: "::", Port: other, Family: "IPv6"}); err != nil {
		t.Fatal(err)
	}
	masters := calls(t, log, isMaster)
	if len(masters) != 1 {
		t.Fatalf("expected one tunnel connection, got %d", len(masters))
	}
	for _, want := range []string{"-N", "ClearAllForwardings=yes", "ControlPersist=no", "BatchMode=yes", "StrictHostKeyChecking=yes"} {
		if !slices.Contains(masters[0], want) {
			t.Fatalf("master missing %s: %q", want, masters[0])
		}
	}
	if masters[0][len(masters[0])-2] != "--" || masters[0][len(masters[0])-1] != spark.SSH {
		t.Fatalf("destination must follow --: %q", masters[0])
	}
	forwards := calls(t, log, isOp("forward"))
	if len(forwards) != 2 || spec(forwards[0]) != "127.0.0.1:"+strconv.Itoa(port)+":127.0.0.1:"+strconv.Itoa(port) || spec(forwards[1]) != "127.0.0.1:"+strconv.Itoa(other)+":[::1]:"+strconv.Itoa(other) {
		t.Fatalf("unexpected forwards: %q", forwards)
	}
	if active := tunnels.Active(); len(active) != 2 {
		t.Fatalf("expected two active tunnels: %+v", active)
	}
}

func TestTunnelManagersKeepSeparateSockets(t *testing.T) {
	first, _ := tunnelFixture(t)
	second := NewTunnels(5 * time.Second)
	second.controlDir = first.controlDir
	t.Cleanup(func() { second.Close() })
	for _, tunnels := range []*Tunnels{first, second} {
		if _, err := tunnels.Open(context.Background(), spark, remoteService(freePort(t))); err != nil {
			t.Fatal(err)
		}
	}
	a, b := first.masters[spark.Name].path, second.masters[spark.Name].path
	if a == b {
		t.Fatalf("managers share a socket: %s", a)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b); err != nil {
		t.Fatalf("closing first manager removed second socket: %v", err)
	}
}

func TestStaleTunnelSocketsAreRemovedAndLiveOnesKept(t *testing.T) {
	dir := shortTempDir(t)
	listen := func(name string) *net.UnixListener {
		l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, name), Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	stale := listen("t0123456789abcdef")
	stale.SetUnlinkOnClose(false)
	stale.Close()
	live := listen("tfedcba9876543210")
	defer live.Close()
	other := listen("c0123456789abcdef")
	other.SetUnlinkOnClose(false)
	other.Close()
	removeStaleTunnelSockets(dir)
	for name, want := range map[string]bool{"t0123456789abcdef": false, "tfedcba9876543210": true, "c0123456789abcdef": true} {
		if _, err := os.Lstat(filepath.Join(dir, name)); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", name, err == nil, want)
		}
	}
}

func TestBusyLocalPortFallsBackToAnotherPort(t *testing.T) {
	tunnels, log := tunnelFixture(t)
	busy, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	f, err := tunnels.Open(context.Background(), spark, remoteService(port))
	if err != nil || f.LocalPort == port || f.LocalPort == 0 {
		t.Fatalf("expected a different local port: %+v %v", f, err)
	}

	// A port taken after the check is retried rather than reported as open.
	race := freePort(t)
	t.Setenv("LHWATCH_TEST_BUSY", strconv.Itoa(race))
	f, err = tunnels.Open(context.Background(), spark, remoteService(race))
	if err != nil || f.LocalPort == race {
		t.Fatalf("expected retry on another port: %+v %v", f, err)
	}
	attempts := calls(t, log, isOp("forward"))
	if len(attempts) != 3 || !strings.HasPrefix(spec(attempts[1]), "127.0.0.1:"+strconv.Itoa(race)+":") {
		t.Fatalf("unexpected attempts: %q", attempts)
	}
}

func TestClosingTunnelsCancelsForwardsAndEndsConnections(t *testing.T) {
	tunnels, log := tunnelFixture(t)
	a, b := freePort(t), freePort(t)
	fa, err := tunnels.Open(context.Background(), spark, remoteService(a))
	if err != nil {
		t.Fatal(err)
	}
	fb, err := tunnels.Open(context.Background(), spark, remoteService(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := tunnels.CloseForward(fa); err != nil {
		t.Fatal(err)
	}
	if cancels := calls(t, log, isOp("cancel")); len(cancels) != 1 || spec(cancels[0]) != "127.0.0.1:"+strconv.Itoa(a)+":127.0.0.1:"+strconv.Itoa(a) {
		t.Fatalf("expected one cancel: %q", cancels)
	}
	master := tunnels.masters["spark"]
	if err := tunnels.CloseForward(fb); err != nil {
		t.Fatal(err)
	}
	if master.alive() || len(tunnels.Active()) != 0 {
		t.Fatal("closing the last tunnel must end its connection")
	}
	if _, err := os.Stat(master.path); !os.IsNotExist(err) {
		t.Fatal("control socket left behind")
	}
	if err := tunnels.CloseForward(fb); err != nil {
		t.Fatal("closing twice should be harmless")
	}

	// A dead connection is replaced on the next open.
	if _, err := tunnels.Open(context.Background(), spark, remoteService(a)); err != nil {
		t.Fatal(err)
	}
	tunnels.masters["spark"].cmd.Process.Kill()
	<-tunnels.masters["spark"].done
	if len(tunnels.Active()) != 0 {
		t.Fatal("dead connection still listed")
	}
	if _, err := tunnels.Open(context.Background(), spark, remoteService(a)); err != nil {
		t.Fatal(err)
	}
	if got := len(calls(t, log, isMaster)); got != 3 {
		t.Fatalf("expected a fresh connection, got %d masters", got)
	}

	live := tunnels.masters["spark"]
	tunnels.Close()
	if live.alive() {
		t.Fatal("Close left a tunnel connection running")
	}
	if _, err := tunnels.Open(context.Background(), spark, remoteService(a)); err == nil {
		t.Fatal("open after Close succeeded")
	}
}

func TestTunnelFailuresAreReported(t *testing.T) {
	tunnels, _ := tunnelFixture(t)
	t.Setenv("LHWATCH_TEST_MASTER_FAIL", "1")
	_, err := tunnels.Open(context.Background(), spark, remoteService(freePort(t)))
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("expected ssh error detail, got %v", err)
	}
	if len(tunnels.Active()) != 0 {
		t.Fatal("failed connection listed as active")
	}

	unavailable := NewTunnels(time.Second)
	unavailable.controlDir = ""
	if _, err := unavailable.Open(context.Background(), spark, remoteService(8000)); err == nil {
		t.Fatal("remote tunnel without a private socket directory")
	}
	if _, err := unavailable.Open(context.Background(), domain.Host{Name: "bad", SSH: "-oProxyCommand=x"}, remoteService(8000)); err == nil {
		t.Fatal("unsafe destination accepted")
	}
}
