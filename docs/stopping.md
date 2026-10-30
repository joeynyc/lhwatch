# What stop does

Full semantics of the `x` action. The README has the short version.

A port belongs to a process. Stopping that process can close **all of its listeners**, not just the highlighted endpoint. The confirmation names the machine, process, PID, and its known listening endpoints.

![Stop confirmation](assets/confirm.png)

## The signal

- The action sends **SIGTERM only**.
- "Requested" means the signal was sent. The next scan establishes whether the listener disappeared.
- A process may ignore SIGTERM or be restarted by its supervisor.
- There is no automatic SIGKILL or process-tree termination.
- A click never sends a signal. Confirming a stop always needs `y`; `n`, `q`, `Esc`, or `Enter` cancel.

## Revalidation

Before signaling, the collector checks the process's identity, ownership, and selected listener again on that machine. It refuses unknown, stale, changed, protected, or foreign-user targets.

Stopping fails closed: unknown, unreadable, ambiguous, or incomplete evidence disables it, with a stated reason.

### Docker and containers

- Container groups do not offer process stopping, because a container can contain several processes and have its own lifecycle manager.
- When Docker is reachable, the collector also rechecks published port bindings. It refuses a process that forwards ports for a container, such as a rootless Docker port proxy, or whose container ownership is ambiguous.
- If listing Docker's containers times out, or they cannot all be inspected (more than 64 are running, including paused ones, or the scan time limit is reached), ordinary processes are not stoppable until a complete inventory is available.
- A Docker daemon you cannot access at all does not block stopping.
- On Linux, a process whose container membership cannot be read is refused.

How containers are recognized is described in [naming.md](naming.md).

### Race protection

Linux uses a pidfd when available to bind the signal to that process incarnation. macOS and fallback Linux signaling retain a small check-to-signal race. PID reuse protection is best-effort there.

## Lost responses

If the response is lost, the outcome is shown as unknown and the host is refreshed. A connection error does not prove that no signal was sent.

## Stale hosts

Failed hosts retain their last observations in the TUI as stale and cannot be stopped from that stale view.

## Demo

The demo simulates stop actions. It never contacts or signals real services.

For the design behind these checks, see [ARCHITECTURE.md](../ARCHITECTURE.md).
