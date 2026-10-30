# Continuous integration

GitHub Actions runs `.github/workflows/ci.yml` on pushes to `main`, on pull requests, and on demand (**Actions → CI → Run workflow**, any branch). Other branches are tested through their pull request, so a push never runs twice. A newer push to the same branch cancels the older run.

| Job | Runs on | What it checks |
| --- | --- | --- |
| Test (linux) | `ubuntu-latest` | gofmt, vet, race tests, Python suites, cross builds |
| Test (macos) | `macos-latest` | The same, plus the tmux TUI smoke test (`scripts/smoke.py`) |
| lint | `ubuntu-latest` | staticcheck and govulncheck on the current stable Go |
| Collector (Python 3.9, 3.14) | Python containers | Collector tests on the oldest and newest supported Python |

In summary, CI runs formatting, vet, race tests, and cross-compilation on Linux and macOS, staticcheck and govulncheck on Linux, and the collector tests on Python 3.9 and 3.14.

The test jobs use the Go version from the `go.mod` directive, the oldest supported toolchain. The lint job uses the current stable Go, because vulnerability results follow the toolchain.

The smoke test builds `lhwatch`, drives the real TUI in an isolated tmux session against disposable local listeners, and stops only the fixtures it started. The macOS job installs tmux with Homebrew first. To run it yourself, install `tmux` and run `make build && python3 scripts/smoke.py`.

## Pinned versions

Actions are pinned to commit SHAs, with the release tag in a trailing comment. staticcheck and govulncheck are pinned to released versions. To upgrade one, change the SHA or version and its comment together, and check that staticcheck supports the Go version in `go.mod`.

## Releases

To publish a release, add notes at `docs/releases/vX.Y.Z.md`, then push a matching tag:

```sh
git tag vX.Y.Z && git push origin vX.Y.Z
```

`.github/workflows/release.yml` runs when you push a version tag such as `v0.2.0`. It runs vet and tests, builds the archives and `SHA256SUMS`, checks that the packaged Linux binary reports the tag, and publishes a GitHub release. Release notes come from `docs/releases/<tag>.md` when that file exists; otherwise GitHub generates them. A tag with a suffix, such as `v0.2.0-rc.1`, is published as a prerelease.

## Hardware checks

CI never connects to SSH hosts. `scripts/hardware_smoke.py` checks lhwatch against real machines, so you run it manually against your own hosts, using your lhwatch config and existing SSH access. See [verification.md](verification.md).
