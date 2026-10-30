# Names, containers, and endpoint purposes

How lhwatch decides what to call a service, how it attributes processes to containers, and where endpoint purpose labels come from. Every name and label is derived from direct evidence. lhwatch never guesses from a port number, image name, or address, and no project code is executed.

## What the list shows

The main list leads with the **project or application name**, with its process, folder, or Docker context underneath.

- Endpoints that belong to one verified process or container share a row.
- Multiple ports appear together. `3000 +4` means four more ports are available in details.
- Search matches the whole service, so searching for one port still lets you inspect all its endpoints.
- Identified services appear before unidentified listeners.
- PID and full command remain in details.

## Project names

On each monitored host, lhwatch walks upward from the process working directory (up to 16 levels). At the nearest matching directory it uses, in order:

1. A custom name from `.lhwatch.json`.
2. The `name` in `package.json`.
3. The folder containing `.git`, `go.mod`, `pyproject.toml`, or `Cargo.toml`.

Search rules:

- Dependency folders inside `node_modules` or virtual environments are skipped, including when inferring a project folder without metadata.
- The search stops before shared system directories and standard Homebrew prefixes, so Homebrew's own repository does not become a service's project name.
- Invalid, oversized, or excessively nested metadata is ignored without losing the host's listeners.

If no project marker is found, lhwatch can recognize a macOS app bundle from the process's executable path, or show a working-directory name explicitly marked **folder inferred**. App paths that appear only in command arguments do not count. Otherwise it keeps the process name and says **project unidentified**.

Details show the naming source and full folder, so ambiguous names are traceable. Ports are never used to guess project names.

### Custom names

Create `.lhwatch.json` in the project folder **on the machine running that server**:

```json
{"name": "My Store Website"}
```

- The name appears on the next refresh.
- In a monorepo, the nearest package or project marker wins. Place the custom name in that package directory.
- Missing permissions or unavailable working directories can prevent attribution.
- Names are display metadata. Stopping still revalidates the actual process and listener.

## Containers

Processes inside Docker, Podman, containerd, CRI-O, Kubernetes, and LXC containers are recognized from their cgroups and cannot be stopped from lhwatch.

When Docker is available through a local Unix socket, lhwatch also adds container names and Compose project and service labels.

- **Host-network containers.** For Linux containers using host networking, lhwatch matches actual socket ownership inside the container to the host listener. That inspection needs Python 3 and readable process descriptors inside the container, and it uses the container's existing permissions.
- **Shared PID namespaces.** Containers sharing a PID namespace cannot be attributed this way.
- **Incomplete inventory.** If the container inventory is incomplete, host-network names are unavailable.
- **Published ports.** Published TCP ports can be named from an exact, unique Docker binding.

Docker naming is optional and time-limited. Missing Docker access leaves ordinary listener discovery available.

Other labels you may see:

- **Unidentified service**: a root or system service without sufficient naming evidence. A nearby explanation says why.
- **Limited details**: the host was observed, but some information could not be read.

Container rows explain that their stop controls belong in Docker. This release only stops verified ordinary processes. See [stopping.md](stopping.md).

## Endpoint purposes

Open a service's details to see purpose labels to the left of each address. **Model API**, **Web UI**, or another explicit label describes that endpoint. **Purpose unknown** means lhwatch has no evidence for its role. The **PURPOSE SOURCES** section explains where each label came from.

![Service details with endpoint purposes](assets/endpoints.png)

### Declare purposes

On macOS or Linux, declare endpoint purposes in the project's `.lhwatch.json` on the machine running the server:

```json
{
  "name": "My Store Website",
  "endpoints": {
    "3000": "Web UI",
    "8000": "API"
  }
}
```

- Mappings apply to exact TCP port numbers for that discovered project.
- They take precedence over automatic labels and appear on the next refresh.
- Use the nearest project directory, as described under [Project names](#project-names).

### Automatic labels

On Linux, lhwatch recognizes explicit launch ports in supported SGLang, vLLM, and Python `http.server` commands. Verified host-network Docker containers can use the same evidence, including NVIDIA's entrypoint wrapper.

- Labels describe the declared launch purpose, not a health check.
- Ambiguous commands, unreadable arguments, auxiliary ports, and published Docker port translations remain unknown.
- lhwatch never guesses a purpose from a familiar port number, image name, or address, and it does not send requests to your servers.
- Detected container processes do not borrow purpose mappings from identically named folders on the host. Their automatic labels use launch evidence.
