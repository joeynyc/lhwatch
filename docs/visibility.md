# Visibility and limits

What lhwatch can and cannot see.

- This release discovers **listening TCP sockets**, including loopback, wildcard, IPv4, and IPv6. It does not call a listening socket "healthy." UDP, application logs, and AI/GPU metrics are outside this first version.
- Collection runs with the logged-in user's permissions. macOS may omit other users' sockets. Linux may show a listener without enough information to identify its process. Unknown ownership disables stopping.
- A host scan sees its current network namespace. Ports published by containers may appear, but isolated container listeners and Docker Desktop's internal VM are not universally visible. The app does not claim to discover every container service.
- One service row can contain several addresses or ports. Grouping requires a shared process incarnation or container ID on the same host. Identical project names alone never combine processes. Unidentified endpoints remain separate.
- Failed hosts retain their last observations in the TUI as stale and cannot be stopped from that stale view. JSON reports the failed scan explicitly rather than silently returning success.
- Partial visibility is represented by `partial` and warnings in JSON output, not a false claim of full visibility.

See [naming.md](naming.md) for container attribution limits and [stopping.md](stopping.md) for how missing evidence affects stopping.
