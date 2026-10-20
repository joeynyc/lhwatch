package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/joeynyc/lhwatch/internal/collector"
	"github.com/joeynyc/lhwatch/internal/config"
	"github.com/joeynyc/lhwatch/internal/domain"
	"github.com/joeynyc/lhwatch/internal/ui"
)

// version is set for release builds with -ldflags "-X main.version=v1.2.3"
// (make build VERSION=v1.2.3). Otherwise the Go toolchain's embedded module
// version identifies the tag or commit, including uncommitted changes.
var version = ""

func versionString() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type hostFlags []string

func (h *hostFlags) String() string     { return fmt.Sprint([]string(*h)) }
func (h *hostFlags) Set(v string) error { *h = append(*h, v); return nil }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lhwatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var hosts hostFlags
	path := fs.String("config", config.DefaultPath(), "JSON configuration path")
	fs.Var(&hosts, "host", "add SSH host: name=user@hostname (repeatable)")
	jsonOutput := fs.Bool("json", false, "print one read-only fleet snapshot and exit")
	demo := fs.Bool("demo", false, "explore a simulated fleet; never connects to real hosts")
	noMouse := fs.Bool("no-mouse", false, "leave the mouse to the terminal, so plain drags select text")
	showVersion := fs.Bool("version", false, "print version")
	refresh := fs.Int("refresh", 0, "refresh interval in seconds (default 3, or config)")
	timeout := fs.Int("timeout", 0, "per-host timeout in seconds (default 8, or config)")
	fs.Usage = func() {
		fmt.Fprint(stderr, "lhwatch — your localhost, everywhere.\n\nUsage: lhwatch [options]\n\nA live view of TCP listeners on this machine and your SSH hosts.\nStop a selected process with x, then confirm in the dashboard.\n")
		fs.PrintDefaults()
		fmt.Fprintln(stderr, "\nExamples:\n  lhwatch\n  lhwatch --host devbox=you@devbox.example\n  lhwatch --demo\n  lhwatch --json\n\nRequires Python 3.9+ on each host; macOS also uses lsof and ps.")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "lhwatch: unexpected positional argument; use --help")
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, "lhwatch", versionString())
		return 0
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	var cfg config.Config
	var err error
	if *demo {
		cfg, err = config.Load("", false, nil)
		cfg.Hosts = ui.DemoHosts()
	} else {
		cfg, err = config.Load(*path, visited["config"], hosts)
	}
	if err != nil {
		fmt.Fprintln(stderr, "lhwatch:", err)
		return 2
	}
	if visited["refresh"] {
		cfg.RefreshSeconds = *refresh
	}
	if visited["timeout"] {
		cfg.TimeoutSeconds = *timeout
	}
	if cfg.RefreshSeconds < 1 || cfg.RefreshSeconds > 3600 || cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 60 {
		fmt.Fprintln(stderr, "lhwatch: --refresh must be 1–3600 seconds and --timeout must be 1–60 seconds")
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	var backend domain.Backend = collector.New(cfg.Timeout())
	if *demo {
		backend = ui.NewDemoBackend()
	}
	if *jsonOutput {
		report := collect(ctx, cfg.Hosts, backend)
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintln(stderr, "lhwatch: write JSON:", err)
			return 1
		}
		for _, host := range report.Hosts {
			if host.Error != "" {
				return 1
			}
		}
		return 0
	}
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		fmt.Fprintln(stderr, "lhwatch: an interactive terminal is required; use --json for a snapshot or --demo --json for sample data")
		return 2
	}
	if !*demo {
		// The dashboard rescans every few seconds; reuse one SSH connection per
		// host and end them on exit. One-shot JSON keeps independent connections.
		multiplexed := collector.NewMultiplexed(cfg.Timeout())
		defer multiplexed.Close()
		backend = multiplexed
	}
	model := ui.New(ctx, cfg.Hosts, backend, cfg.Interval(), *demo)
	if *noMouse {
		model = model.WithoutMouse()
	}
	if !*demo {
		// Tunnels opened with o live until the dashboard exits.
		tunnels := collector.NewTunnels(cfg.Timeout())
		defer tunnels.Close()
		model = model.WithForwarder(tunnels)
	}
	_, err = tea.NewProgram(model, tea.WithContext(ctx), tea.WithOutput(stdout)).Run()
	if !*noMouse {
		fmt.Fprint(stdout, ui.ResetPointer) // the dashboard set an arrow pointer
	}
	if err != nil && !errors.Is(err, tea.ErrProgramKilled) && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "lhwatch:", err)
		return 1
	}
	return 0
}

func collect(ctx context.Context, hosts []domain.Host, backend domain.Backend) domain.Report {
	report := domain.Report{SchemaVersion: domain.SchemaVersion, CollectedAt: time.Now().UTC(), Hosts: make([]domain.Snapshot, len(hosts))}
	var wg sync.WaitGroup
	for i, host := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, err := backend.Scan(ctx, host)
			snapshot.Host = host.Name
			snapshot.SchemaVersion = domain.SchemaVersion
			if snapshot.Services == nil {
				snapshot.Services = []domain.Service{}
			}
			if err != nil {
				snapshot.Error = err.Error()
			}
			report.Hosts[i] = snapshot
		}()
	}
	wg.Wait()
	return report
}
