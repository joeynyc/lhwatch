# Agent guide

lhwatch is a Go Bubble Tea TUI that lists listening TCP services on this machine and SSH hosts. It ships an embedded Python collector to each host and can send SIGTERM to a revalidated process. Read [ARCHITECTURE.md](ARCHITECTURE.md) before changing the collector, transport, or stop path; [CONTRIBUTING.md](CONTRIBUTING.md) has the contributor rules.

## Where things are

| Path | What it holds |
| --- | --- |
| `main.go` | Flags, config loading, `--json`, exit codes |
| `internal/config/` | Config file and `--host` parsing and validation |
| `internal/domain/` | Shared types (`Service`, `Host`, `Forward`) |
| `internal/collector/collector.go` | Runs `helper.py` locally or over SSH; strict decoding |
| `internal/collector/helper.py` | Python 3.9+ stdlib collector: discovery, naming, Docker, `can_stop` guards, `stop()` |
| `internal/collector/control_*.go` | Private SSH control-socket directory |
| `internal/collector/tunnel.go` | SSH port forwards for `o` |
| `internal/ui/` | Model, keys, mouse, layout, panels, rendering |
| `scripts/` | tmux smoke tests and hardware/Docker smoke tests |
| `docs/` | CI and verification notes, release notes |

## Verify every change

```sh
gofmt -l .        # must print nothing
make vet
make test         # Go race tests + Python collector and script tests
make cross        # all four release targets still build
python3 scripts/smoke.py   # real TUI in tmux; needs tmux
```

`helper.py` runs on hosts with Python as old as 3.9. Check it with `uv run --no-project --python 3.9 python -m unittest discover -s internal/collector -p 'test_*.py'` when you touch it.

## Rules that matter

- Stopping fails closed. Unknown, unreadable, ambiguous, or incomplete evidence must set `can_stop=False` with a `stop_reason`, both in `scan()` and in the `stop()` recheck. Add a test showing the refusal.
- No guessing from port numbers, image names, or addresses. Names, container ownership, and purposes need direct evidence.
- `helper.py` stays standard-library only and installs nothing on hosts.
- Collector output is untrusted: keep Go decoding strict and sanitize text before display.
- Update the README, the in-app help (`internal/ui/panels.go`), and ARCHITECTURE.md when flags, keys, configuration, or safety behavior change.
- Never put real hostnames, usernames, IPs, or command lines in tests, fixtures, or screenshots.
