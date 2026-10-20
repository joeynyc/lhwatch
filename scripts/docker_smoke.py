#!/usr/bin/env python3
"""Verify Docker naming and grouping using one disposable host-network container.

Requires an existing Linux SSH host and a cached image containing python3.
The test uses existing Docker access, never pulls an image, and only removes
the uniquely named container it creates.
"""
import argparse
import json
import os
from pathlib import Path
import selectors
import shlex
import subprocess
import tempfile
import time
import uuid

from hardware_smoke import ssh_command
from smoke import ROOT, Terminal, await_condition, snapshot

LISTENER = r'''
import json, signal, socket, time
sockets = []
for family, address in [(socket.AF_INET, '127.0.0.1'), (socket.AF_INET, '127.0.0.1'), (socket.AF_INET6, '::1')]:
    s = socket.socket(family)
    if family == socket.AF_INET6:
        s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
    s.bind((address, sockets[0].getsockname()[1] if family == socket.AF_INET6 else 0))
    s.listen(4)
    sockets.append(s)
print(json.dumps({'endpoints': [{'address': s.getsockname()[0], 'port': s.getsockname()[1]} for s in sockets]}), flush=True)
signal.signal(signal.SIGTERM, lambda *_: exit(0))
time.sleep(180)
'''


def remote(host, argv):
    return subprocess.run(ssh_command(host, shlex.join(argv)), capture_output=True,
                          text=True, timeout=25, check=True).stdout


def container_stop_refused(screen):
    return ('Stop unavailable: Use Docker to stop this container.' in screen
            and 'CONFIRM PROCESS STOP' not in screen)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--host', required=True, help='Configured LHWatch display name')
    parser.add_argument('--image', required=True, help='Cached image with python3; never pulled')
    parser.add_argument('--binary', type=Path, default=ROOT / 'bin/lhwatch')
    args = parser.parse_args()
    settings = json.loads(args.config.expanduser().read_text())
    host, = [h for h in settings['hosts'] if h['name'] == args.host and h.get('ssh')]
    binary = args.binary.resolve()
    env = dict(os.environ, TERM='xterm-256color', COLORTERM='truecolor')
    env.pop('NO_COLOR', None)
    # Pin fixture operations to the host's local daemon, regardless of context.
    docker = ['docker', '--host', 'unix:///var/run/docker.sock']
    name = 'lhwatch-verify-' + uuid.uuid4().hex[:12]
    owner = uuid.uuid4().hex
    process = None
    container_id = None
    with tempfile.TemporaryDirectory(prefix='lhwatch-docker-config-') as temp:
        config = Path(temp) / 'config.json'
        settings['refresh_seconds'] = 1
        config.write_text(json.dumps(settings))
        code, _ = snapshot(binary, config, env)
        assert code == 0, 'Configured hosts must scan before testing'
        try:
            argv = docker + ['run', '--rm', '--pull', 'never', '--name', name,
                             '--label', 'lhwatch.test-owner=' + owner,
                             '--label', 'com.docker.compose.project=lhwatch-smoke',
                             '--label', 'com.docker.compose.service=api',
                             '--network', 'host', '--read-only', '--cap-drop', 'ALL',
                             '--security-opt', 'no-new-privileges', '--entrypoint', 'python3',
                             args.image, '-u', '-c', LISTENER]
            process = subprocess.Popen(ssh_command(host, shlex.join(argv)),
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            with selectors.DefaultSelector() as ready:
                ready.register(process.stdout, selectors.EVENT_READ)
                assert ready.select(25), 'Fixture startup timed out'
            line = process.stdout.readline()
            assert line, 'Docker fixture failed to start: ' + process.stderr.read(2048)
            record = json.loads(line)
            template = '{{json .Id}} {{json (index .Config.Labels "lhwatch.test-owner")}}'
            identity = remote(host, docker + ['inspect', '--format', template, name]).strip().split()
            container_id, observed_owner = [json.loads(v) for v in identity]
            assert observed_owner == owner, 'Container ownership mismatch'

            def attributed():
                code, report = snapshot(binary, config, env)
                assert code == 0, 'Host scan failed'
                services = [s for h in report['hosts'] if h['host'] == host['name']
                            for s in h['services'] if s.get('container_id') == container_id]
                if len(services) != len(record['endpoints']):
                    return False
                assert {(s['address'], s['port']) for s in services} == {
                    (ep['address'], ep['port']) for ep in record['endpoints']}
                assert all(s['container_name'] == name and not s['can_stop'] for s in services)
                assert all(s['project_source'] == 'Docker Compose service' for s in services)
                return services

            services = await_condition(attributed, message='Docker socket attribution')
            project = services[0]['project']
            assert 'lhwatch-smoke' in project and 'api' in project
            terminal = Terminal([binary, '--config', config], env, width=144, height=42)
            try:
                terminal.wait_text('lhwatch')
                terminal.text('/'); terminal.text(name); terminal.send('Enter')
                def grouped(screen):
                    cells = [cell.strip() for line in screen.splitlines() for cell in line.split('│')]
                    rows = [c for c in cells if c.startswith('› ') and project in c and host['name'] in c]
                    return len(rows) == 1 and all(str(ep['port']) in rows[0] for ep in record['endpoints'])
                terminal.wait_screen(grouped, 'one Docker service row with both ports')
                terminal.send('Enter')
                terminal.wait_screen(lambda s: all(str(ep['port']) in s for ep in record['endpoints'])
                                     and 'Docker' in s, 'container endpoint details and unavailable action')
                terminal.send('Escape')
                terminal.text('x')
                terminal.wait_screen(container_stop_refused,
                                     'Docker stop is unavailable')
                deadline = time.monotonic() + 1
                while time.monotonic() < deadline:
                    assert 'CONFIRM PROCESS STOP' not in terminal.capture()
                    time.sleep(0.1)
                assert remote(host, docker + ['inspect', '--format', '{{.State.Running}}', name]).strip() == 'true'
            finally:
                terminal.close()
            print('PASS:', host['name'], 'Docker Compose name, exact sockets, one grouped row, both ports, stop refusal', flush=True)
        finally:
            # Never remove by a guessed name alone: prove this run's owner label.
            check = subprocess.run(ssh_command(host, shlex.join(docker + ['inspect', '--format',
                '{{index .Config.Labels "lhwatch.test-owner"}}', name])), capture_output=True, text=True, timeout=20)
            if check.returncode == 0:
                assert check.stdout.strip() == owner, 'Refusing cleanup of a foreign container'
                remote(host, docker + ['rm', '-f', name])
            if process is not None:
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.terminate()
                    process.wait(timeout=5)
                process.stdout.close(); process.stderr.close()
        code, report = snapshot(binary, config, env)
        assert code == 0
        assert not any(s.get('container_id') == container_id for h in report['hosts'] for s in h['services'])
        print('PASS:', host['name'], 'owned Docker fixture removed and endpoints disappeared', flush=True)


if __name__ == '__main__':
    main()
