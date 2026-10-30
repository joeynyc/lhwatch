# Architecture

lhwatch is a Go terminal UI that runs one small Python collector on each monitored host, locally or over SSH, and never installs anything there. This page explains how the pieces fit together and which invariants keep stopping safe. The README covers behavior from a user's point of view.

```
 this machine                                          each monitored host
┌──────────────────────────────────────────┐          ┌────────────────────────────┐
│ main.go: flags + config → mode           │          │ python3 - <base64 request> │
│                                          │  stdin:  │  helper.py (embedded)      │
│ ui (Bubble Tea)                          │  script  │   scan:  sockets → owners  │
│   Model ── Cmd ──► domain.Backend ───────┼─────────►│          → names, labels,  │
│     ▲                (collector)         │  stdout: │            Docker, guards  │
│     └──── scanMsg / stopMsg ◄────────────┼──────────┤   stop:  revalidate → TERM │
│   Model ── Cmd ──► domain.Forwarder      │   JSON   └────────────────────────────┘
│                      (collector.Tunnels) ├── ssh -M (tunnel connection) ──► host
└──────────────────────────────────────────┘
```

## Packages

| Path | Role |
| --- | --- |
| `main.go` | Parses flags, loads config, picks the mode (`--json`, `--demo`, or the TUI), and closes SSH connections on exit, including on SIGHUP. |
| `internal/config` | Defaults, then the JSON file, then `--host` flags. `ValidateHost` is the gate that keeps option injection out of every `ssh` argument list. |
| `internal/domain` | The wire contract shared by the UI and collectors: `Service`, `Snapshot`, `StopRequest`/`StopResult`, `Forward`, and the `Backend` and `Forwarder` interfaces. |
| `internal/collector` | `collector.go` runs `helper.py` locally or over SSH and strictly decodes its output. `control_*.go` holds the private socket directory for SSH connection sharing. `tunnel.go` handles browser tunnels; `browse_*.go` opens browsers. |
| `internal/collector/helper.py` | The ephemeral collector: discovery, naming, Docker attribution, endpoint labels, and stop revalidation. |
| `internal/ui` | The Bubble Tea model. `model.go` (state and messages), `keys.go`, `groups.go` (grouping, search, selection, and their cache), `stop.go`, `open.go`, `layout.go` (screen geometry shared by drawing and mouse hit-testing), `mouse.go`, `view.go`, `list.go`, `details.go`, `panels.go`, and `demo.go` (simulated fleet). |
| `scripts/` | tmux-driven smoke tests: local, a Docker SSH fixture, your own SSH hosts, and Docker attribution. [docs/verification.md](docs/verification.md) explains each. |

## A scan, end to end

1. **Schedule.** Each host has its own refresh loop. `Model.refresh` increments that host's *generation* and returns a command. A result whose generation is stale is dropped, so a slow host never overwrites a newer observation, and one host never blocks another.
2. **Transport.** `Collector.invoke` base64-encodes a small JSON request, starts `python3 - <token>` (or `ssh … -- dest "python3 - <token>"`), and writes `helper.py` to stdin. The remote shell only ever sees that constant command and a base64 token. Output is capped at 16 MiB and the whole call is bounded by the host timeout.
3. **Discovery.** On Linux the helper reads `/proc/net/tcp{,6}` for listening sockets, then walks `/proc/*/fd` to find each socket's owning process; process identity is `boot_id:pid:start_ticks`. On macOS it uses `lsof` and `ps`, with identity built from boot time, PID, start time, and command.
4. **Attribution, display only.** Project names come from the nearest `.lhwatch.json`, `package.json`, or project marker above the working directory, or from a macOS app bundle. Docker names come from `docker inspect`. For host-network containers, the helper probes the container itself (`docker exec`) and requires the same socket inode **and** endpoint. Endpoint purposes come from `.lhwatch.json` or explicit launch arguments. Nothing is inferred from a port number.
5. **Guards.** Every service gets `can_stop` and a `stop_reason`. Stopping is refused for PID 1, the collector and its ancestors, other users' processes, missing identity, processes in a container's cgroup (Docker, Podman, containerd, CRI-O, Kubernetes, LXC) or whose cgroup cannot be read, and processes serving a published Docker port or one claimed by more than one container. If Docker lists containers but the inventory is incomplete (over `DOCKER_LIMIT`, a timeout, or a failed inspect), every remaining ordinary process is refused, because an unseen container could be using it as a port proxy.
6. **Decode.** Go decodes the JSON strictly (unknown fields are rejected) and validates every service. The UI then re-stamps `Host` with the configured name, so a collector can never relabel or retarget its rows. All collector text passes through `clean()` before rendering.

## Stopping a process

The UI groups endpoints for display, but a stop always targets one exact endpoint and process incarnation:

1. `x` opens a confirmation listing every endpoint of that process. The target is frozen at that moment; later refreshes cannot retarget it.
2. On `y`, the UI rechecks eligibility against the latest observation. Stale hosts and container groups are refused.
3. The helper's `stop()` rechecks ancestry, then (on Linux) opens a pidfd, rescans the host including Docker published-port bindings, and requires PID, identity, address, port, and family to match, with the same user and `can_stop` still true. Only then does it send **SIGTERM**: through the pidfd when available, otherwise `kill()`, which leaves a small PID-reuse race. There is no SIGKILL.
4. A result means "signal sent", not "process exited"; the next scan shows what happened.

## SSH connections

| Connection | Used for | Lifetime |
| --- | --- | --- |
| Per-command `ssh` | `--json` snapshots | One command |
| Shared scan connection (`ControlMaster=auto`) | Dashboard scans and stops | `Collector.Close` on exit; `ControlPersist=60` if lhwatch is killed |
| Tunnel connection (`ssh -N -M`, `ClearAllForwardings=yes`) | Browser forwards added with `-O forward` | `Tunnels.Close` on exit, or when its last forward closes |

Control sockets live in `$XDG_RUNTIME_DIR/lhwatch-UID` or `/tmp/lhwatch-UID`. If that directory is shared, owned by someone else, or a symlink, connection sharing is disabled, because a planted socket could relay sessions. Socket names hash destination, port, and identity file, so different credentials never share a connection. All connections use `BatchMode=yes` and `StrictHostKeyChecking=yes` and never modify SSH configuration.

## Invariants worth preserving

- Grouping never changes a signal target. `Service.Key()` identifies an endpoint; `GroupKey()` only groups display rows.
- No guessing. Names, container ownership, and purposes require direct evidence. Ambiguity leaves a field empty and explains why.
- Stopping fails closed. Anything unknown, unreadable, ambiguous, or incomplete makes a process unstoppable with a `stop_reason`; it never makes one stoppable.
- Collector output is untrusted. Decode it strictly, cap it, and sanitize it before display.
- Hosts are independent. A failing host keeps its last observation, marked stale, and disables its stop controls.
- A click never sends a signal. Mouse clicks become the same key presses the keyboard sends, except that confirming a stop is keyboard-only.
- Drawing and clicking share geometry. `layout.go` computes where every box, row, and footer hint is; never hard-code positions elsewhere.
- Grouping is cached, not recomputed per frame. Anything that changes observations must call `reconcile()` (or `invalidate()`), as scan results do.
- Rendering never blocks. Every backend, SSH, or browser operation runs in a Bubble Tea command; `Tunnels.Active` takes only a short lock.

## Testing

`make test` runs Go race tests (with fake `ssh` scripts for transport, multiplexing, and tunnels) and the Python suites for the collector. The scripts in `scripts/` drive the real TUI in tmux; see the README and [docs/ci.md](docs/ci.md) for which run in CI.
