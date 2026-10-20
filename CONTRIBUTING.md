# Contributing

Thanks for helping improve lhwatch. Bug reports, fixes, and focused features are welcome.

## Prerequisites

- Go 1.26 or later (see `go.mod`)
- Python 3.9 or later, for the collector and script tests
- `tmux`, for the TUI smoke test (`scripts/smoke.py`)
- Docker, only if you run `scripts/smoke.py --docker`

## Build and test

```sh
make build    # builds bin/lhwatch
make test     # Go race tests plus the Python collector and script tests
make vet
gofmt -l .    # must print nothing
python3 scripts/smoke.py
```

`make demo` runs the dashboard with sample data, which needs no hosts. CI also runs staticcheck and govulncheck; you can run them locally with `go run honnef.co/go/tools/cmd/staticcheck@latest ./...` and `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`.

See [ARCHITECTURE.md](ARCHITECTURE.md) for how the collector, transport, and UI fit together.

## The remote helper

`internal/collector/helper.py` runs on every monitored host. It must stay compatible with Python 3.9 and use only the standard library, because lhwatch installs nothing on the hosts it watches. CI tests it on Python 3.9 and 3.14.

## Pull requests

- Keep each pull request focused on one change.
- Add or update tests for any behavior change. Pull requests without tests for new behavior will be asked for them.
- Run `gofmt`, `make vet`, and `make test` before opening the pull request.
- Update the README when you change flags, configuration, or visible behavior.
- Do not include real hostnames, usernames, IP addresses, or process command lines in tests, fixtures, or screenshots.

For security issues, follow [SECURITY.md](SECURITY.md) instead of opening a public issue.
