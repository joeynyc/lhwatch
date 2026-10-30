# Configuration and command line

## Config file

By default, lhwatch reads `$XDG_CONFIG_HOME/lhwatch/config.json`, or `~/.config/lhwatch/config.json` if that variable is unset. A missing default file is fine. An explicit `--config` path must exist and be valid.

```json
{
  "refresh_seconds": 3,
  "timeout_seconds": 8,
  "hosts": [
    { "name": "laptop" },
    { "name": "devbox", "ssh": "you@devbox" },
    { "name": "gpu", "ssh": "you@gpu-box", "port": 2222, "identity_file": "~/.ssh/id_ed25519" }
  ]
}
```

## Hosts

- A host without `ssh` represents the machine running the TUI. If omitted, one named `local` is added. Configure at most one local entry.
- SSH entries can also set `port` (1–65535) and `identity_file`. `~/` is expanded in the identity path.
- Names must be unique, 1–32 characters, and start with a letter or digit. They may contain letters, digits, spaces, `.`, `_`, and `-`.
- Unknown keys are rejected, and the file must be a single JSON object of at most 1 MiB.

## Flags and precedence

```sh
lhwatch --config lhwatch.example.json
lhwatch --refresh 2 --timeout 5
lhwatch --version
```

- Configuration order is defaults, file, then flags.
- Repeatable `--host name=destination` entries **add** hosts. Duplicate names are errors rather than silent replacements.
- Refresh is 1–3600 seconds. Each host timeout is 1–60 seconds.
- `--no-mouse` leaves mouse handling to the terminal. See [interface.md](interface.md).
- Personal configurations can be saved as `*.local.json`, which Git ignores.

See [ssh.md](ssh.md) for how hosts are reached.
