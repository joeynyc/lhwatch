# SSH hosts, connection sharing, and tunnels

How lhwatch reaches remote machines and how it opens remote services in a browser. For the quick version, see the README.

## Connecting

First establish a normal SSH connection so your key and the server's identity are known:

```sh
ssh you@devbox
```

Then monitor it alongside this machine:

```sh
lhwatch --host devbox=you@devbox
lhwatch --host devbox=you@devbox --host gpu=you@gpu-box
```

- Names on the left of `=` are dashboard labels.
- The destination may be an existing SSH alias, including aliases created by tools such as NVIDIA Sync. There is no need to copy keys or configure another login.
- lhwatch uses your existing OpenSSH client, keys, agent, and SSH configuration.
- Authentication is noninteractive and host-key checking is strict. lhwatch never prompts for a password, accepts an unknown server key, installs a daemon, or changes your SSH configuration.
- Keep password-protected keys loaded in your SSH agent.

## Running the dashboard on a Linux host

Install the matching binary on the host, or build one with `make cross`. That target writes `dist/lhwatch-linux-arm64`, `dist/lhwatch-linux-amd64`, and the macOS binaries. Run it from a terminal on that host. Its local view then describes that machine.

Configure other hosts, including a Mac with SSH access enabled, in the same way as above.

## Connection sharing

While the dashboard runs, it reuses one SSH connection per host through OpenSSH connection sharing instead of reconnecting on every refresh.

- Control sockets live in a private per-user directory: `$XDG_RUNTIME_DIR/lhwatch-UID`, or `/tmp/lhwatch-UID` when `XDG_RUNTIME_DIR` is unset, as on macOS.
- Reuse is disabled if that directory is shared, foreign-owned, or a symlink, or if its parent lacks sticky-bit protection and is writable by or owned by another non-root user.
- The connections close when the dashboard exits, or on their own after 60 idle seconds if it is interrupted.
- `--json` snapshots connect independently.

## Opening a service in a browser

Press `o` on a service to open it in your browser.

- A service with several ports opens its **Web UI** endpoint if one is labelled, otherwise another labelled endpoint, otherwise its lowest port.
- Local services open directly.
- URLs use `http://`. lhwatch never guesses HTTPS from a port number.

### Tunnels for remote hosts

For a remote host, lhwatch forwards the port over SSH and opens the forwarded address, so a remote web UI is one key away.

- The forward binds only to `127.0.0.1` on this machine. It uses the same port number when that port is free, or another free port.
- All tunnels to one host share a dedicated SSH connection that ignores port forwards in your SSH configuration.
- Details and diagnostics (`e`) list open tunnels.
- `O` closes the selected service's tunnel. Quitting closes them all.
- Other local users can reach a forwarded port while it is open, as with `ssh -L`.

### Where a browser launches

A browser is launched only where it can appear: `open` on a Mac you are sitting at, or `xdg-open` when Linux has a graphical display.

Over SSH or without a display, lhwatch shows the URL and sends it to your terminal's clipboard (OSC 52, where supported).

The demo simulates tunnels and never opens a browser.
