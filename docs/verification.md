# Verification

lhwatch has three layers of checks: unit suites that run anywhere, a tmux smoke test that drives the real executable, and two opt-in scripts that run against SSH hosts you configure. Every live check creates its own disposable listeners or containers and only ever signals or removes those. Your existing services are observed, never stopped.

[ci.md](ci.md) describes which of these run in continuous integration.

## Make targets

```sh
make test
make vet
make build
make cross
make release VERSION=v0.1.0
python3 scripts/smoke.py
python3 scripts/smoke.py --docker
```

`make build` writes `bin/lhwatch`. `make cross` builds the four release targets into `dist/`. `make release VERSION=vX.Y.Z` writes the release archives and checksums to `dist/release`. See [ci.md](ci.md) for the release procedure.

## Requirements

| Check | Needs |
| --- | --- |
| `make test`, `make vet`, `make cross` | Go 1.26 or newer, Python 3.9 or newer |
| `scripts/smoke.py` | The above, plus `tmux` |
| `scripts/smoke.py --docker` | The above, plus a local Docker daemon |
| `scripts/hardware_smoke.py` | `tmux`, a built `bin/lhwatch`, and an lhwatch config with Linux SSH hosts that accept noninteractive login |
| `scripts/docker_smoke.py` | `tmux`, a built `bin/lhwatch`, a configured Linux SSH host whose SSH user can run Docker, and an image with `python3` already cached on that host |

## Unit suites

```sh
make test     # Go race tests and all Python suites
make vet      # go vet ./...
make cross    # builds for macOS and Linux on ARM64 and AMD64
```

`make test` runs:

- `go test -race ./...` across the CLI, config, collector, and UI packages. SSH transport, connection sharing, and browser tunnels are tested with fake `ssh` scripts, so no network access is needed.
- The collector tests in `internal/collector`: socket discovery and process identity on macOS and Linux, project naming, Docker attribution, endpoint labels, and stop refusals for other users, PID 1, ancestors, and containers.
- The harness tests in `scripts/`, which check that the smoke scripts only confirm a stop for their exact fixture and that the SSH fixture cleans up after itself. Tests that read `/proc` are skipped on macOS.

To check the declared minimum Go version, run `GOTOOLCHAIN=go1.26.0 go test ./...`.

## Terminal smoke test

```sh
python3 scripts/smoke.py
python3 scripts/smoke.py --docker
```

The suite requires `tmux`. It builds `bin/lhwatch`, then drives the actual TUI against disposable listeners in tmux on an isolated tmux socket. It covers search, cancel, confirmed stop, and disappearance. Without options it runs on this machine only:

- Starts a disposable listener in a temporary project with a `.lhwatch.json` name and endpoint label, and checks that `--json` reports its name, label, and `can_stop`.
- In the TUI, searches for the project, checks that the selected row and details match the listener's host, PID, port, and naming source, opens the keyboard guide, cancels a stop, then confirms one and waits for the listener to exit and disappear.
- Starts a disposable IPv6 loopback listener and checks that it is discovered. This is skipped when IPv6 loopback is unavailable.
- Adds an unreachable SSH host and checks that `--json` exits 1, reports the SSH error, and keeps the local results.
- Runs `--demo`, resizes it to 80 × 24, searches for something that matches nothing, clears the search, and quits.

Before pressing `y`, the script reads the confirmation and checks its host, PID, and endpoint against the fixture. On any mismatch it presses Escape and fails. When a screen check times out, the last screen is saved to `.local/smoke-failure.txt`.

`--docker` adds a Linux host reached over real SSH. The script builds `scripts/e2e/Dockerfile` as `lhwatch-e2e:local`, starts it with SSH on a random loopback port, and connects as an unprivileged user with a freshly generated key and a private `known_hosts` file. It then:

- Starts a listener inside the container whose `.lhwatch.json` is too deeply nested to decode, and checks that naming falls back to its `package.json` without breaking the scan or the local results.
- Builds a native Linux binary for the container's architecture and checks that it discovers the same listener.
- Stops that listener through the TUI over SSH and checks that it disappears while the local listener remains.

The container, key, listeners, and tmux server are removed when the script exits. The image stays cached for later runs. The script does not stop existing workloads or change your SSH trust store.

## Your own SSH hosts

`scripts/hardware_smoke.py` checks lhwatch end to end against the Linux SSH hosts in a config you provide:

```sh
make build
python3 scripts/hardware_smoke.py --config path/to/config.json
```

Pass `--binary` to test a different lhwatch executable. The script:

1. Copies your config to a temporary file with a one-second refresh. Your config, SSH settings, and `known_hosts` are not modified.
2. Takes one snapshot and stops if any configured host fails to scan.
3. For each host with an `ssh` entry, runs two fixtures over SSH: one with a custom `.lhwatch.json` name and endpoint label, and one whose `.lhwatch.json` is malformed so naming must fall back to `package.json`.
4. For each fixture, checks the reported identity, project name, folder, naming source, and endpoint label, then searches for it in the TUI, cancels a stop, confirms a stop, and checks that the fixture exited on SIGTERM, cleaned up, and disappeared from the next snapshot.

Each fixture is a short Python listener in a temporary directory on the remote host. It listens on `127.0.0.1` with a random port, removes its directory when it exits, and exits on its own when its SSH session closes or after three minutes. The script never sends a signal to a remote PID during cleanup. The only remote process it stops is the fixture, and only through the TUI's normal confirmation.

SSH runs with `BatchMode=yes` and `StrictHostKeyChecking=yes`, so each host must already accept your key and be in your `known_hosts`. Fixtures read `/proc`, so the hosts must run Linux. Any Linux ARM64 or AMD64 host works, a DGX Spark included.

## Docker attribution on your own host

`scripts/docker_smoke.py` checks Docker naming and grouping on one configured Linux SSH host:

```sh
make build
python3 scripts/docker_smoke.py --config path/to/config.json --host <name> --image <cached-image>
```

`--host` is the host's `name` in your config. `--image` must already be on that host and contain `python3`. The script passes `--pull never`, so it never downloads an image. Pass `--binary` to test a different lhwatch executable.

Over SSH, the script starts one container on the host network, with a unique name, an ownership label for this run, and Compose project and service labels. The container is read-only, drops all capabilities, and listens on two IPv4 endpoints and one IPv6 endpoint. The script then checks:

- `--json` attributes exactly those endpoints to the container, with the Compose name, and marks none of them stoppable.
- The TUI shows one grouped row with both ports, the details show every endpoint, and `x` reports that the container must be stopped with Docker, without opening a confirmation. The container is still running afterwards.

On exit, the script removes the container only after its ownership label matches this run, then checks that its endpoints are gone. It uses the host's local Docker socket regardless of the active Docker context.

## Scope and limits

- Listener visibility follows OS permissions and the current network namespace. Unknown or unobservable metadata never grants stop permission.
- A listening port is not an application health check.
- A successful stop means SIGTERM was sent, not that the process exited. A process may ignore it or be restarted by a supervisor.
- Linux pidfds bind the signal to one process incarnation when supported. macOS and older Linux kernels fall back to `kill()`, which leaves a small check-to-signal race, as [stopping.md](stopping.md) explains.
- SSH collection has a client-side deadline. If a stop response is lost, the outcome is reported as unknown and the host is refreshed.
- Visibility limits are described in [visibility.md](visibility.md).
- Host-network container attribution needs Python 3 inside the container and readable descriptors in its PID namespace. Published Docker ports without an observed OS listener are not shown.
- The demo and screenshots use simulated processes and placeholder hostnames.
