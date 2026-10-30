"""Ephemeral stdlib collector, compatible with Python 3.9+. No network probes."""
import base64
import datetime
import json
import os
import platform
import pwd
import re
import signal
import shutil
import socket
import stat
import subprocess
import sys
import time

SCHEMA = 1
PROC = '/proc'
PROJECT_BOUNDARIES = {'/', '/Users', '/home', '/usr', '/usr/local', '/opt',
                      '/opt/homebrew', '/home/linuxbrew/.linuxbrew',
                      '/tmp', '/private/tmp', '/var', '/private/var', '/var/tmp',
                      '/private/var/tmp'}


def read_text(path):
    with open(path, encoding='utf-8', errors='replace') as f:
        return f.read()


def run(args):
    return subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          text=True, errors='replace', env=dict(os.environ, LC_ALL='C'))


def username(uid):
    try:
        return pwd.getpwuid(uid).pw_name
    except KeyError:
        return str(uid) if uid >= 0 else ''


def parse_linux_stat(text):
    # comm can itself contain spaces and closing parentheses.
    left, right = text.index('('), text.rindex(')')
    fields = text[right + 1:].split()
    return {'pid': int(text[:left]), 'name': text[left+1:right],
            'ppid': int(fields[1]), 'start_ticks': fields[19]}


def linux_address(encoded, family):
    address, port = encoded.split(':')
    raw = bytes.fromhex(address)
    # procfs emits native-endian 32-bit words, including for IPv6.
    if sys.byteorder == 'little':
        raw = b''.join(raw[i:i+4][::-1] for i in range(0, len(raw), 4))
    return socket.inet_ntop(socket.AF_INET if family == 'IPv4' else socket.AF_INET6, raw), int(port, 16)


def linux_sockets(root):
    sockets = {}
    for filename, family in [('tcp', 'IPv4'), ('tcp6', 'IPv6')]:
        try:
            lines = read_text(os.path.join(root, 'net', filename)).splitlines()[1:]
        except FileNotFoundError:
            if filename == 'tcp6':
                continue
            raise
        for line in lines:
            fields = line.split()
            if len(fields) < 10 or fields[3] != '0A':
                continue
            address, port = linux_address(fields[1], family)
            sockets.setdefault(fields[9], []).append(dict(address=address, port=port, family=family, uid=int(fields[7])))
    return sockets


# Container runtimes name cgroups after a 64-hex container ID: Docker
# (/docker/ID, docker-ID.scope), Podman (libpod-ID.scope), containerd and
# CRI-O (cri-containerd-ID.scope, crio-ID.scope), and Kubernetes cgroupfs
# (/kubepods/.../ID). LXC and Incus use named lxc.payload.NAME cgroups.
CONTAINER_CGROUP = re.compile(r'[/-]([0-9a-f]{64})(?:\.scope)?(?=[/\n]|$)|/lxc\.payload\.([^/\n]+)', re.M)


def container_ids(cgroup):
    return {m.group(1) or 'lxc:' + m.group(2) for m in CONTAINER_CGROUP.finditer(cgroup)}


def other_container_cgroup(process_path, root=PROC):
    """Protect host-visible container processes without disabling native-in-container use.

    Returns None when membership cannot be read, so callers refuse to stop it.
    """
    try:
        target = read_text(os.path.join(process_path, 'cgroup'))
        own = read_text(os.path.join(root, 'self', 'cgroup'))
        return bool(container_ids(target) - container_ids(own))
    except OSError:
        return None


def linux_scan(root=PROC):
    sockets = linux_sockets(root)
    boot = ''
    warnings = []
    try:
        boot = read_text(os.path.join(root, 'sys/kernel/random/boot_id')).strip()
    except OSError:
        warnings.append('Boot identity unavailable; stopping is disabled.')
    owners = {}
    denied = False
    for entry in os.scandir(root):
        if not entry.name.isdigit():
            continue
        pid = int(entry.name)
        try:
            before = parse_linux_stat(read_text(os.path.join(entry.path, 'stat')))
            uid = os.stat(entry.path).st_uid
            matches = set()
            with os.scandir(os.path.join(entry.path, 'fd')) as descriptors:
                for fd in descriptors:
                    try:
                        link = os.readlink(fd.path)
                    except OSError:
                        continue
                    if link.startswith('socket:[') and link.endswith(']') and link[8:-1] in sockets:
                        matches.add(link[8:-1])
            if not matches:
                continue
            command, cwd, executable, argv = '', '', '', []
            try:
                argv = read_text(os.path.join(entry.path, 'cmdline')).rstrip('\x00').split('\x00')
                command = ' '.join(argv)
            except OSError:
                pass
            try:
                cwd = os.readlink(os.path.join(entry.path, 'cwd'))
            except OSError:
                pass
            try:
                executable = os.readlink(os.path.join(entry.path, 'exe'))
            except OSError:
                pass
            after = parse_linux_stat(read_text(os.path.join(entry.path, 'stat')))
            if before['start_ticks'] != after['start_ticks']:
                continue
            info = dict(pid=pid, name=before['name'], command=command, cwd=cwd, uid=uid, _executable=executable, _argv=argv,
                        _container_process=other_container_cgroup(entry.path, root),
                        started=before['start_ticks'], identity=('linux:%s:%s:%s' % (boot, pid, before['start_ticks'])) if boot else '')
            for inode in matches:
                owners.setdefault(inode, []).append(info)
        except PermissionError:
            denied = True
        except (OSError, ValueError, IndexError):
            # A process can exit between any two procfs reads.
            continue
    services = []
    for inode, endpoints in sockets.items():
        for endpoint in endpoints:
            for owner in owners.get(inode, [dict(pid=0, name='unknown', identity='')]):
                services.append(dict(endpoint, **{k: v for k, v in owner.items() if k != 'uid'},
                                     uid=owner.get('uid', endpoint['uid']), _socket_inode=inode))
    if denied or any(s['pid'] == 0 for s in services):
        warnings.append('Some process ownership is unavailable; unowned listeners cannot be stopped.')
    return services, warnings


def parse_lsof(text):
    services = []
    process, descriptor = {}, {}

    def flush():
        if descriptor.get('family') in ('IPv4', 'IPv6') and descriptor.get('endpoint'):
            endpoint = descriptor['endpoint']
            try:
                address, port = endpoint.rsplit(':', 1)
                address = address.strip('[]')
                address = ('0.0.0.0' if descriptor['family'] == 'IPv4' else '::') if address == '*' else address
                services.append(dict(process, address=address, port=int(port), family=descriptor['family']))
            except ValueError:
                pass

    for line in text.splitlines():
        if not line:
            continue
        tag, value = line[0], line[1:]
        if tag == 'p':
            flush()
            process, descriptor = dict(pid=int(value), uid=-1, name='unknown'), {}
        elif tag == 'f':
            flush()
            descriptor = {}
        elif tag == 'c':
            process['name'] = value
        elif tag == 'u':
            process['uid'] = int(value)
        elif tag == 't':
            descriptor['family'] = value
        elif tag == 'n':
            descriptor['endpoint'] = value
    flush()
    return services


def mac_processes():
    result = run(['ps', '-axo', 'pid=,ppid=,uid=,lstart=,command='])
    if result.returncode:
        raise RuntimeError('ps failed: ' + result.stderr[:500])
    processes = {}
    for line in result.stdout.splitlines():
        fields = line.split(None, 8)
        if len(fields) < 8:
            continue
        try:
            pid, ppid, uid = map(int, fields[:3])
        except ValueError:
            continue
        processes[pid] = dict(ppid=ppid, uid=uid, started=' '.join(fields[3:8]), command=fields[8] if len(fields) > 8 else '')
    return processes


def mac_executables():
    # comm is the executable path, separate from user-controlled arguments.
    # Splitting only once preserves spaces in application names.
    try:
        result = run(['ps', '-axo', 'pid=,comm='])
    except OSError:
        return {}
    if result.returncode:
        return {}
    executables = {}
    for line in result.stdout.splitlines():
        fields = line.split(None, 1)
        if len(fields) == 2 and fields[0].isdigit():
            executables[int(fields[0])] = fields[1]
    return executables


def mac_scan():
    result = run(['lsof', '-nP', '-iTCP', '-sTCP:LISTEN', '-FpcuftnT'])
    if result.returncode and not (result.returncode == 1 and not result.stdout.strip() and not result.stderr.strip()):
        raise RuntimeError('lsof failed: ' + (result.stderr.strip() or 'exit %d' % result.returncode)[:500])
    services = parse_lsof(result.stdout)
    warnings = []
    if result.stderr.strip():
        warnings.append('lsof: ' + result.stderr.strip()[:500])
    try:
        processes = mac_processes()
    except (OSError, RuntimeError) as exc:
        processes = {}
        warnings.append(str(exc))
    executables = mac_executables()
    boot = ''
    try:
        boot_result = run(['sysctl', '-n', 'kern.boottime'])
        if boot_result.returncode == 0:
            boot = boot_result.stdout.strip()
    except OSError:
        pass
    cwds = {}
    pids = sorted({s['pid'] for s in services})
    # Chunk to avoid exceeding argv limits on large hosts.
    for offset in range(0, len(pids), 200):
        try:
            cwd_result = run(['lsof', '-a', '-p', ','.join(map(str, pids[offset:offset+200])), '-d', 'cwd', '-Fn'])
            pid = None
            for line in cwd_result.stdout.splitlines():
                if line.startswith('p'):
                    pid = int(line[1:])
                elif line.startswith('n') and pid is not None:
                    cwds[pid] = line[1:]
        except (OSError, ValueError):
            pass
    for service in services:
        info = processes.get(service['pid'])
        if info and info['uid'] == service['uid']:
            service.update({k: v for k, v in info.items() if k != 'ppid'})
            service['_executable'] = executables.get(service['pid'], '')
            # lstart is second resolution; full command helps distinguish rapid reuse.
            service['identity'] = 'darwin:%s:%s:%s:%s' % (boot, service['pid'], info['started'], info['command'])
        else:
            service['identity'] = ''
        service['cwd'] = cwds.get(service['pid'], '')
    return services, warnings


def protected_pids():
    protected = {0, 1, os.getpid()}
    pid = os.getppid()
    processes = None
    if sys.platform == 'darwin':
        try:
            processes = mac_processes()
        except (OSError, RuntimeError):
            # Inability to identify ancestors must refuse stop, not weaken the guard.
            raise RuntimeError('Cannot verify ancestor process safety')
    while pid > 1 and pid not in protected:
        protected.add(pid)
        if processes is not None:
            if pid not in processes:
                raise RuntimeError('Cannot verify ancestor process safety')
            pid = processes[pid]['ppid']
        else:
            pid = parse_linux_stat(read_text('%s/%d/stat' % (PROC, pid)))['ppid']
    return protected


def project_json(path):
    """Read small regular metadata files only; never execute project code."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK)
        with os.fdopen(fd, 'rb') as f:
            if not stat.S_ISREG(os.fstat(f.fileno()).st_mode):
                return {}
            data = f.read(131073)
        if len(data) > 131072:
            return {}
        value = json.loads(data)
        return value if isinstance(value, dict) else {}
    except (OSError, ValueError, UnicodeError, RecursionError):
        return {}


def cached_project_json(path, cache):
    if path not in cache:
        cache[path] = project_json(path)
    return cache[path]


def project_info(cwd, command='', executable='', metadata=None):
    """Evidence-based naming on the monitored host; no port-based guesses."""
    def info(name, path, source):
        return dict(project=name, project_path=path, project_source=source)

    metadata = {} if metadata is None else metadata
    boundaries = PROJECT_BOUNDARIES | {os.path.expanduser('~')}
    project_directory = ''
    if cwd and os.path.isabs(cwd):
        directory = os.path.normpath(cwd)
        # Dependency packages are not the user's project.
        for component in ('node_modules', '.venv', 'venv'):
            marker = os.sep + component + os.sep
            if marker in directory + os.sep:
                directory = (directory + os.sep).split(marker, 1)[0] or os.sep
        project_directory = directory
        # Stop before reading shared-prefix metadata (e.g. Homebrew's .git).
        for _ in range(16):
            if directory in boundaries:
                break
            for filename, source in (('.lhwatch.json', 'Custom name (.lhwatch.json)'),
                                     ('package.json', 'Package name (package.json)')):
                name = cached_project_json(os.path.join(directory, filename), metadata).get('name')
                if isinstance(name, str) and name.strip() and len(name) <= 120:
                    return info(name.strip(), directory, source)
            for marker in ('.git', 'go.mod', 'pyproject.toml', 'Cargo.toml'):
                if os.path.exists(os.path.join(directory, marker)):
                    return info(os.path.basename(directory), directory, 'Project folder (' + marker + ')')
            directory = os.path.dirname(directory)
    # Arguments can reference unrelated apps; only executable evidence counts.
    bundle = re.search(r'/([^/]+)\.app/Contents/', executable) if os.path.isabs(executable) else None
    if bundle:
        return info(bundle.group(1), '', 'Application bundle (executable)')
    if project_directory:
        directory = project_directory
        generic = {'bin', 'sbin', 'lib', 'libexec', 'tmp', 'private', 'var', 'run', 'usr', 'opt', 'Users', 'home'}
        if directory not in boundaries and os.path.basename(directory) not in generic | {''}:
            return info(os.path.basename(directory), directory, 'Working directory (inferred)')
    return info('', '', '')


def project_endpoint_label(project_path, port, metadata):
    if project_path:
        settings = cached_project_json(os.path.join(project_path, '.lhwatch.json'), metadata)
        endpoints = settings.get('endpoints')
        label = endpoints.get(str(port)) if isinstance(endpoints, dict) else None
        if isinstance(label, str) and len(label) <= 120 and label.strip() and label.strip().isprintable():
            return dict(endpoint_label=label.strip(), endpoint_label_source='.lhwatch.json')
    return {}


def launch_endpoint_label(argv, address, port, family):
    """Recognize explicit server bindings from real argv, never flattened text."""
    if not isinstance(argv, list) or not argv or not all(isinstance(arg, str) for arg in argv):
        return {}
    # This NGC entrypoint passes the command through; arbitrary wrappers do not.
    if argv[0] == '/opt/nvidia/nvidia_entrypoint.sh':
        argv = argv[1:]
    if not argv:
        return {}
    executable = os.path.basename(argv[0])
    server, args = '', []
    if executable in ('sglang', 'vllm') and argv[1:2] == ['serve']:
        server, args = executable, argv[2:]
    elif re.fullmatch(r'python(?:3(?:\.\d+)?)?', executable) and len(argv) >= 3:
        if argv[1] == '-m':
            server = {'sglang.launch_server': 'sglang', 'vllm.entrypoints.openai.api_server': 'vllm',
                      'http.server': 'http.server'}.get(argv[2], '')
            args = argv[3:]
        elif os.path.basename(argv[1]) in ('sglang', 'vllm') and argv[2] == 'serve':
            server, args = os.path.basename(argv[1]), argv[3:]
    if not server or any(arg.split('=', 1)[0] in ('--', '--headless', '--uds', '--help', '-h', '--version') for arg in args):
        return {}

    def option(names):
        values = []
        for index, arg in enumerate(args):
            key, separator, value = arg.partition('=')
            if key in names:
                if not separator:
                    value = args[index + 1] if index + 1 < len(args) else ''
                if not value or value.startswith('-'):
                    raise ValueError('Missing option value')
                values.append(value)
        if len(values) > 1:
            raise ValueError('Ambiguous repeated option')
        return values[0] if values else None

    try:
        if server == 'http.server':
            # Only known stdlib options can consume tokens beside the port.
            positional, remaining = [], iter(args)
            for arg in remaining:
                key, separator, value = arg.partition('=')
                if key in ('--bind', '-b', '--directory', '-d', '--protocol', '-p'):
                    if not separator and next(remaining, None) is None:
                        return {}
                elif arg == '--cgi':
                    continue
                elif arg.startswith('-'):
                    return {}
                else:
                    positional.append(arg)
            if len(positional) != 1:
                return {}
            configured_port, bind = positional[0], option(('--bind', '-b'))
            label, source = 'HTTP server', 'Python http.server'
        else:
            configured_port, bind = option(('--port',)), option(('--host',))
            # Worker-only modes do not expose the configured frontend port.
            if option(('--node-rank',)) not in (None, '0'):
                return {}
            count = option(('--api-server-count', '-asc'))
            if count is not None and (not re.fullmatch('[0-9]+', count) or int(count) == 0):
                return {}
            label, source = 'Model API', 'SGLang' if server == 'sglang' else 'vLLM'
        if not configured_port or not re.fullmatch('[0-9]{1,5}', configured_port) or not 0 < int(configured_port) < 65536 or int(configured_port) != port:
            return {}
        if bind is not None:
            af = socket.AF_INET if family == 'IPv4' else socket.AF_INET6
            if socket.inet_pton(af, bind) != socket.inet_pton(af, address):
                return {}
        return dict(endpoint_label=label, endpoint_label_source=source + ' explicit launch port')
    except (OSError, ValueError):
        return {}


# Inspect selected fields only, never the full Config or application environment.
# Path/Args are private launch evidence and must not enter the service JSON.
DOCKER_FORMAT = '''{"id":{{json .Id}},"name":{{json .Name}},"image":{{json .Config.Image}},"running":{{json .State.Running}},"network_mode":{{json .HostConfig.NetworkMode}},"pid_mode":{{json .HostConfig.PidMode}},"compose_project":{{json (index .Config.Labels "com.docker.compose.project")}},"compose_service":{{json (index .Config.Labels "com.docker.compose.service")}},"ports":{{json .NetworkSettings.Ports}},"path":{{json .Path}},"args":{{json .Args}}}'''
DOCKER_BUDGET = 1.8
DOCKER_LIMIT = 64

# Host networking shares /proc/net, so that table alone proves nothing. Only
# socket inodes actually held by processes in the container's private PID
# namespace are returned. Both inode and endpoint must also match the host.
# -I -S -B prevents user/site startup imports and bytecode writes.
DOCKER_PROBE = r'''
import json, os, socket, sys, time

def collect(root='/proc', budget=0.65):
    deadline = time.monotonic() + budget
    sockets, owned, partial = {}, set(), False
    for name, family in [('tcp', 'IPv4'), ('tcp6', 'IPv6')]:
        try:
            with open(os.path.join(root, 'net', name)) as stream:
                for line in stream:
                    if time.monotonic() > deadline:
                        return dict(sockets=[], partial=True)
                    fields = line.split()
                    if len(fields) < 10 or fields[3] != '0A':
                        continue
                    address, port = fields[1].split(':')
                    raw = bytes.fromhex(address)
                    if sys.byteorder == 'little':
                        raw = b''.join(raw[i:i+4][::-1] for i in range(0, len(raw), 4))
                    sockets[fields[9]] = dict(inode=fields[9], address=socket.inet_ntop(socket.AF_INET if family == 'IPv4' else socket.AF_INET6, raw), port=int(port, 16), family=family)
                    if len(sockets) > 4096 or time.monotonic() > deadline:
                        return dict(sockets=[], partial=True)
        except FileNotFoundError:
            if name != 'tcp6':
                return dict(sockets=[], partial=True)
        except OSError:
            return dict(sockets=[], partial=True)
    with os.scandir(root) as processes:
        for process in processes:
            if time.monotonic() > deadline:
                partial = True
                break
            if not process.name.isdigit():
                continue
            try:
                with os.scandir(os.path.join(process.path, 'fd')) as descriptors:
                    for descriptor in descriptors:
                        if time.monotonic() > deadline:
                            partial = True
                            break
                        try:
                            link = os.readlink(descriptor.path)
                            if link.startswith('socket:[') and link.endswith(']') and link[8:-1] in sockets:
                                owned.add(link[8:-1])
                        except OSError:
                            pass
            except PermissionError:
                partial = True
            except OSError:
                pass
    return dict(sockets=[sockets[inode] for inode in sorted(owned)], partial=partial)

if __name__ == '__main__':
    print(json.dumps(collect()))
'''


def docker_endpoint():
    """Choose a local Unix socket, never an ambient remote Docker context."""
    if not shutil.which('docker'):
        return '', ''
    configured = os.environ.get('DOCKER_HOST', '')
    if configured and not configured.startswith('unix:///'):
        return '', 'Docker details unavailable: configured Docker endpoint is not a local Unix socket'
    candidates = ([configured[7:]] if configured else [
        '/var/run/docker.sock',
        os.path.join(os.environ.get('XDG_RUNTIME_DIR', '/run/user/%d' % os.getuid()), 'docker.sock'),
        os.path.expanduser('~/.docker/run/docker.sock'),
    ])
    for path in candidates:
        try:
            if stat.S_ISSOCK(os.stat(path).st_mode):
                return 'unix://' + path, ''
        except OSError:
            continue
    return '', ('Docker details unavailable: configured local socket is unavailable' if configured else '')


def docker_command(endpoint, args, deadline):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise subprocess.TimeoutExpired(['docker'], DOCKER_BUDGET)
    environment = dict(os.environ, LC_ALL='C')
    for key in ('DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_TLS_VERIFY', 'DOCKER_CERT_PATH'):
        environment.pop(key, None)
    result = subprocess.run(['docker', '--host', endpoint] + args, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, text=True, errors='replace',
                            env=environment, timeout=remaining)
    if result.returncode:
        # CLI stderr can contain paths or arbitrary daemon output. A plain reason
        # conveys the actionable limitation without copying that data into rows.
        raise RuntimeError('Docker access or container inspection unavailable')
    if len(result.stdout) > 1048576:
        raise RuntimeError('Docker metadata exceeded the scan limit')
    return result.stdout


def docker_enrich(services, budget=DOCKER_BUDGET, host_network=True):
    """Optional display-only attribution; ordinary OS discovery always survives.

    host_network=False skips per-container exec probes and uses only published
    bindings, which is enough to protect processes that proxy container ports.
    """
    endpoint, reason = docker_endpoint()
    if not endpoint:
        return [reason] if reason else []
    if budget <= 0:
        refuse_unverified_proxies(services)
        return ['Docker details skipped: scan time budget exhausted']
    deadline = time.monotonic() + min(budget, DOCKER_BUDGET)
    warnings, candidates = [], {}
    # A reachable daemon whose listing succeeded but whose published bindings
    # were not all inspected cannot rule out a same-user port proxy.
    listed = complete = False

    def claim(key, container):
        candidates.setdefault(key, {})[container['id']] = container

    def endpoint_key(address, port, family):
        return (address, int(port), family)

    def problem(message):
        if message not in warnings:
            warnings.append(message)

    try:
        # Unfiltered ps includes paused and restarting containers, whose port
        # proxies still run. A hung daemon may also own such proxies.
        try:
            ids = docker_command(endpoint, ['ps', '-q', '--no-trunc'], deadline).split()
        except subprocess.TimeoutExpired:
            listed = True
            raise
        ids = [cid for cid in ids if re.fullmatch('[0-9a-f]{64}', cid)]
        listed = True
        truncated = len(ids) > DOCKER_LIMIT
        if truncated:
            problem('Docker details incomplete: container scan limit reached')
        ids = ids[:DOCKER_LIMIT]
        if not ids:
            return warnings
        output = docker_command(endpoint, ['inspect', '--format', DOCKER_FORMAT] + ids, deadline)
        containers = []
        for line in output.splitlines():
            value = json.loads(line)
            if isinstance(value, dict) and value.get('id') in ids and value.get('running') is True:
                containers.append(value)
        # A donor's own PidMode is still private when another container joins
        # it. Require complete namespace metadata and exclude both participants.
        namespaces_known = (not truncated and {c['id'] for c in containers} == set(ids)
                            and all(isinstance(c.get('pid_mode'), str) for c in containers))
        donors = set()
        for container in containers:
            mode = container.get('pid_mode', '')
            if isinstance(mode, str) and mode.startswith('container:'):
                target = mode[len('container:'):].lstrip('/')
                matches = {c['id'] for c in containers if target and
                           (c['id'].startswith(target) or str(c.get('name', '')).lstrip('/') == target)}
                donors.update(matches)
                if not matches:
                    namespaces_known = False
        complete = not truncated and {c['id'] for c in containers} == set(ids)
        # Published bindings protect stopping, so claim them all before the
        # display-only exec probes can exhaust the deadline.
        for container in containers:
            ports = container.get('ports')
            if container.get('network_mode') == 'host' or not isinstance(ports, dict):
                continue
            for port, bindings in ports.items():
                if not port.endswith('/tcp') or not isinstance(bindings, list):
                    continue
                for binding in bindings:
                    if not isinstance(binding, dict):
                        continue
                    address, port = binding.get('HostIp'), str(binding.get('HostPort', ''))
                    if not isinstance(address, str) or not address or not port.isdigit() or not 0 < int(port) < 65536:
                        continue
                    family = 'IPv6' if ':' in address else 'IPv4'
                    claim(('published',) + endpoint_key(address, port, family), container)
        for container in containers:
            if container.get('network_mode') != 'host' or not host_network:
                continue
            if time.monotonic() >= deadline:
                problem('Docker details incomplete: scan time limit reached')
                break
            cid = container['id']
            if not namespaces_known:
                problem('Docker host-network names unavailable: complete PID namespace inventory required')
                continue
            if sys.platform != 'linux':
                problem('Docker host-network names unavailable on this platform')
                continue
            if container.get('pid_mode') not in ('', 'private') or cid in donors:
                problem('Docker names unavailable for containers sharing another PID namespace')
                continue
            try:
                data = json.loads(docker_command(endpoint, ['exec', cid, 'python3', '-I', '-S', '-B', '-c', DOCKER_PROBE], deadline))
                if not isinstance(data, dict) or not isinstance(data.get('sockets'), list):
                    raise ValueError('Invalid socket evidence')
                if data.get('partial'):
                    problem('Docker details incomplete: some container process ownership is unavailable')
                for owned in data['sockets']:
                    if (not isinstance(owned, dict) or not str(owned.get('inode', '')).isdigit()
                            or owned.get('family') not in ('IPv4', 'IPv6')
                            or not isinstance(owned.get('address'), str)
                            or type(owned.get('port')) is not int or not 0 < owned['port'] < 65536):
                        continue
                    claim(('inode', str(owned['inode'])) + endpoint_key(owned['address'], owned['port'], owned['family']), container)
            except (OSError, RuntimeError, ValueError, RecursionError, subprocess.TimeoutExpired):
                problem('Docker names incomplete: container socket inspection unavailable (requires Python 3 and process access)')
    except (OSError, RuntimeError, ValueError, RecursionError, subprocess.TimeoutExpired):
        problem('Docker details unavailable: local daemon could not be inspected within the scan limit')
        complete = False

    ambiguous = set()
    for service in services:
        key = endpoint_key(service['address'], service['port'], service['family'])
        matches = dict(candidates.get(('published',) + key, {}))
        matches.update(candidates.get(('inode', service.get('_socket_inode', '')) + key, {}))
        if len(matches) > 1:
            ambiguous.add(id(service))
            service.update(visibility_reason='Container ownership is ambiguous; no container name assigned',
                           can_stop=False, stop_reason='Container ownership is ambiguous; use Docker to stop it')
        elif matches:
            container = next(iter(matches.values()))
            if not service.get('endpoint_label') and container.get('network_mode') == 'host':
                path, args = container.get('path'), container.get('args')
                if isinstance(path, str) and isinstance(args, list):
                    service.update(launch_endpoint_label([path] + args, service['address'], service['port'], service['family']))
            name = str(container.get('name') or '').lstrip('/')[:120]
            project, component = container.get('compose_project'), container.get('compose_service')
            compose = isinstance(project, str) and project.strip() and isinstance(component, str) and component.strip()
            service.update(container_id=container['id'], container_name=name,
                           container_image=str(container.get('image') or '')[:300],
                           project=(project.strip() + ' / ' + component.strip())[:240] if compose else name,
                           project_path='', project_source='Docker Compose service' if compose else 'Docker container',
                           can_stop=False, stop_reason='Container-managed service; stop it with its container manager')
            service['visibility_reason'] = ('Container identified; owning process details are unavailable'
                                            if service['pid'] == 0 else '')
        elif warnings and service.get('pid') == 0:
            service['visibility_reason'] = 'Owning process unavailable; Docker details are incomplete'
    # Shared Docker proxies may own ports that could not be attributed. Their
    # whole process is unavailable for stopping, without inventing a container
    # owner for those other endpoints.
    protected = {(s['pid'], s['identity']) for s in services
                 if (s.get('container_id') or id(s) in ambiguous) and s.get('pid', 0) > 0 and s.get('identity')}
    for service in services:
        if (service.get('pid'), service.get('identity')) in protected and id(service) not in ambiguous:
            service['can_stop'] = False
            service['stop_reason'] = 'Process also serves a Docker container; use Docker to stop it'
    if listed and not complete:
        refuse_unverified_proxies(services)
    return warnings


def refuse_unverified_proxies(services):
    for service in services:
        if service.get('can_stop') and not service.get('container_id'):
            service.update(can_stop=False, stop_reason='Docker inventory incomplete; process may forward container ports')


def scan(include_docker=True, timeout_seconds=None, docker_host_network=True):
    began = time.monotonic()
    collected = datetime.datetime.now(datetime.timezone.utc).isoformat()
    if sys.platform == 'linux':
        services, warnings = linux_scan()
    elif sys.platform == 'darwin':
        services, warnings = mac_scan()
    else:
        raise RuntimeError('Unsupported platform: ' + sys.platform)
    try:
        protected = protected_pids()
    except (OSError, RuntimeError, ValueError, IndexError):
        protected = {s['pid'] for s in services}
        warnings.append('Cannot verify ancestor process safety; stopping is disabled.')
    unique = {}
    projects = {}
    metadata = {}
    for service in services:
        pid, uid = service['pid'], service['uid']
        service['host'] = ''
        service['user'] = username(uid)
        # Naming depends only on the directory and executable, so processes
        # sharing them share one metadata walk regardless of arguments.
        project_key = (service.get('cwd', ''), service.pop('_executable', ''))
        if project_key not in projects:
            projects[project_key] = project_info(project_key[0], executable=project_key[1], metadata=metadata)
        service.update(projects[project_key])
        argv = service.pop('_argv', [])
        # A container cwd names its own filesystem, not an identically named
        # host directory. Only use host metadata for processes in this context.
        project_label = ({} if service.get('_container_process', False) is not False else
                         project_endpoint_label(service['project_path'], service['port'], metadata))
        service.update(project_label or
                       launch_endpoint_label(argv, service['address'], service['port'], service['family']))
        reason = ''
        container_process = service.pop('_container_process', False)
        if container_process:
            reason = 'Container process; stop the container with its container manager'
        elif pid == 0:
            reason = 'Owning process could not be verified'
        elif pid == 1 or pid in protected:
            reason = 'Protected system or ancestor process'
        elif uid != os.getuid():
            reason = 'Process belongs to another user'
        elif container_process is None:
            reason = 'Container membership could not be verified'
        elif not service.get('identity'):
            reason = 'Process identity unavailable'
        service['can_stop'] = not reason
        service['stop_reason'] = reason
        if pid == 0:
            service['visibility_reason'] = 'Owning process unavailable; additional permissions may be required'
        elif not service.get('project'):
            service['visibility_reason'] = ('Working directory unavailable; project could not be identified'
                                            if not service.get('cwd') else 'No project name found in process metadata')
        # Distinct unknown owners can share an endpoint (SO_REUSEPORT). Keep
        # their inode evidence separate until each container has been resolved.
        inode = service.get('_socket_inode', '') if pid == 0 else ''
        unique[(pid, service['address'], service['port'], service['family'], inode)] = service
    services = list(unique.values())
    if include_docker and services:
        budget = DOCKER_BUDGET
        if type(timeout_seconds) in (int, float) and timeout_seconds > 0:
            # Reserve time for SSH/process startup, serialization and transport.
            budget = max(0, min(budget, timeout_seconds * 0.5 - (time.monotonic() - began)))
        warnings.extend(docker_enrich(services, budget=budget, host_network=docker_host_network))
    for service in services:
        service.pop('_socket_inode', None)
    return dict(schema_version=SCHEMA, host='', platform=sys.platform, hostname=socket.gethostname(),
                services=sorted(services, key=lambda s: (s['port'], s['pid'], s['family'], s['address'])),
                warnings=warnings, partial=bool(warnings), collected_at=collected)


def open_pidfd(pid):
    if sys.platform != 'linux' or not hasattr(os, 'pidfd_open') or not hasattr(signal, 'pidfd_send_signal'):
        return None
    try:
        return os.pidfd_open(pid, 0)
    except OSError as exc:
        # ENOSYS / EINVAL mean unavailable, all other failures must refuse.
        if exc.errno in (38, 22):
            return None
        raise


def stop(target, timeout_seconds=None):
    def result(sent, message):
        return dict(schema_version=SCHEMA, sent=sent, message=message)
    pid = target.get('pid')
    if type(pid) is not int or pid <= 1 or not target.get('identity'):
        return result(False, 'Refused: missing identity or protected PID')
    fd = None
    try:
        if pid in protected_pids():
            return result(False, 'Refused: self or ancestor process')
        fd = open_pidfd(pid)
        # Recheck published Docker bindings so a process that proxies container
        # ports stays protected without relying on the TUI. Container exec probes
        # are skipped; host-network container processes are refused by cgroup.
        snapshot = scan(timeout_seconds=timeout_seconds, docker_host_network=False)
        keys = ('pid', 'identity', 'address', 'port', 'family')
        service = next((s for s in snapshot['services'] if all(s.get(k) == target.get(k) for k in keys)), None)
        if service is None:
            return result(False, 'Refused: process identity or listening endpoint changed')
        if service['uid'] != os.getuid():
            return result(False, 'Refused: process belongs to another user')
        if not service.get('can_stop', True):
            return result(False, 'Refused: ' + service.get('stop_reason', 'Stopping is unavailable'))
        if fd is not None:
            signal.pidfd_send_signal(fd, signal.SIGTERM, None, 0)
            return result(True, 'SIGTERM requested; process exit is not guaranteed')
        # POSIX kill cannot bind a PID to an incarnation. Revalidation above is
        # best effort; macOS and older Linux retain a small PID-reuse race.
        os.kill(pid, signal.SIGTERM)
        return result(True, 'SIGTERM requested; process exit is not guaranteed. PID signaling has a residual reuse race on this host (pidfd unavailable).')
    except (OSError, RuntimeError, ValueError, IndexError) as exc:
        return result(False, 'Refused: ' + str(exc)[:500])
    finally:
        if fd is not None:
            os.close(fd)


def main():
    try:
        request = json.loads(base64.b64decode(sys.argv[1], validate=True))
        if request.get('schema_version') != SCHEMA:
            raise ValueError('Unsupported schema_version')
        action = request.get('action')
        if action == 'scan':
            response = scan(timeout_seconds=request.get('timeout_seconds'))
        elif action == 'stop' and isinstance(request.get('target'), dict):
            response = stop(request['target'], timeout_seconds=request.get('timeout_seconds'))
        else:
            raise ValueError('Unsupported action or missing target')
        print(json.dumps(response, ensure_ascii=True))
    except Exception as exc:
        print(json.dumps(dict(schema_version=SCHEMA, error=str(exc)[:1000])))
        sys.exit(1)


if __name__ == '__main__':
    main()
