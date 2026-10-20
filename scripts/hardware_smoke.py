#!/usr/bin/env python3
"""Verify configured Linux SSH hosts using disposable, self-cleaning listeners.

Requires an existing LHWatch config, working noninteractive SSH, and tmux.
Uses existing SSH credentials and known-host trust without modifying either.
"""
import argparse
import base64
import contextlib
import json
import os
from pathlib import Path
import selectors
import shlex
import shutil
import subprocess
import tempfile
import uuid

from smoke import ROOT, Terminal, find_service, snapshot


REMOTE_LISTENER = r'''
import contextlib, json, os, pathlib, select, shutil, signal, socket, sys, tempfile, time
project, mode = sys.argv[1:3]
stopped = 0
def stop(signum, frame):
    global stopped
    stopped = signum
signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGHUP, stop)
with contextlib.ExitStack() as cleanup:
    if len(sys.argv) == 4:
        directory = sys.argv[3]
        cleanup.callback(shutil.rmtree, directory)
    else:
        directory = cleanup.enter_context(tempfile.TemporaryDirectory(prefix='lhwatch-hardware-'))
    path = pathlib.Path(directory)
    (path/'package.json').write_text(json.dumps({'name': project}))
    custom = (json.dumps({'name': project}) if mode == 'custom' else
              '{"name":' + '[' * 10000 + '0' + ']' * 10000 + '}')
    (path/'.lhwatch.json').write_text(custom)
    os.chdir(path)
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        listener.listen(4)
        if mode == 'custom':
            (path/'.lhwatch.json').write_text(json.dumps({
                'name': project, 'endpoints': {str(listener.getsockname()[1]): 'Fixture API'}}))
        pid = os.getpid()
        stat = pathlib.Path('/proc/self/stat').read_text()
        ticks = stat[stat.rindex(')') + 1:].split()[19]
        boot = pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()
        print(json.dumps({'pid': pid, 'port': listener.getsockname()[1],
                          'path': directory, 'project': project,
                          'identity': 'linux:%s:%s:%s' % (boot, pid, ticks)}), flush=True)
        # EOF closes the fixture if the client fails. A deadline also bounds its
        # lifetime if a broken network leaves the SSH server holding stdin open.
        deadline = time.monotonic() + 180
        while not stopped and time.monotonic() < deadline:
            if select.select([sys.stdin], [], [], 0.2)[0]:
                if not os.read(sys.stdin.fileno(), 1):
                    break
    os.chdir('/')
print(json.dumps({'event': 'cleaned', 'signal': stopped}), flush=True)
sys.exit(128 + stopped if stopped else 0)
'''


REMOTE_BOOTSTRAP = r'''
import base64, os, pathlib, shutil, sys, tempfile
project, mode, source = sys.argv[1:]
directory = tempfile.mkdtemp(prefix='lhwatch-hardware-')
try:
    script = pathlib.Path(directory) / 'fixture.py'
    script.write_bytes(base64.b64decode(source))
    os.execv(sys.executable, [sys.executable, '-u', str(script), project, mode, directory])
finally:
    # exec replaces this process; this runs only if bootstrap or exec fails.
    shutil.rmtree(directory)
'''


def fixture_command(project, mode, python='python3'):
    source = base64.b64encode(REMOTE_LISTENER.encode()).decode('ascii')
    return [python, '-u', '-c', REMOTE_BOOTSTRAP, project, mode, source]


def ssh_command(host, remote_command):
    args = ['ssh', '-T', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
            '-o', 'ClearAllForwardings=yes', '-o', 'PermitLocalCommand=no',
            '-o', 'ConnectTimeout=10', '-o', 'ServerAliveInterval=15',
            '-o', 'ServerAliveCountMax=2']
    if host.get('port'):
        args += ['-p', str(host['port'])]
    if host.get('identity_file'):
        args += ['-i', os.path.expanduser(host['identity_file'])]
    return args + ['--', host['ssh'], remote_command]


@contextlib.contextmanager
def remote_listener(host, env=None, mode='fallback'):
    """Yield (SSH process, identity record); closing stdin cleans its own fixture."""
    if mode not in ('custom', 'fallback'):
        raise ValueError('Unknown naming fixture mode')
    project = 'lhw-' + mode + '-' + uuid.uuid4().hex[:8]
    argv = ssh_command(host, shlex.join(fixture_command(project, mode)))
    process = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, text=True, env=env)
    try:
        with selectors.DefaultSelector() as ready:
            ready.register(process.stdout, selectors.EVENT_READ)
            if not ready.select(timeout=20):
                raise AssertionError('SSH fixture did not become ready within 20 seconds')
        line = process.stdout.readline()
        if not line:
            raise AssertionError('SSH fixture failed: ' + process.stderr.read(4096))
        record = json.loads(line)
        if (not isinstance(record.get('pid'), int) or record['pid'] <= 1 or
                not isinstance(record.get('port'), int) or not 0 < record['port'] < 65536 or
                record.get('project') != project or not record.get('identity', '').startswith('linux:')):
            raise AssertionError('Invalid disposable fixture identity')
        yield process, record
    finally:
        # Never signal a remote PID during cleanup: the fixture owns its socket
        # and directory, and exits when its dedicated SSH input reaches EOF.
        with contextlib.suppress(BrokenPipeError):
            process.stdin.close()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.terminate()  # This local SSH subprocess only.
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
        finally:
            process.stdout.close()
            process.stderr.close()


def check_host(binary, config, host, env, mode):
    with remote_listener(host, env, mode) as (remote, record):
        code, report = snapshot(binary, config, env)
        assert code == 0, 'A configured host scan failed'
        services = find_service(report, host['name'], record)
        assert len(services) == 1, 'Fixture endpoint was not uniquely discovered'
        service = services[0]
        source = ('Custom name (.lhwatch.json)' if mode == 'custom' else
                  'Package name (package.json)')
        assert service['identity'] == record['identity'], 'Fixture process identity differs'
        assert service['project'] == record['project'], 'Project name missing'
        assert service['project_path'] == record['path'], 'Project directory differs'
        assert service['project_source'] == source, 'Naming source differs'
        assert service['can_stop'], 'Disposable same-user listener is not stoppable'
        label = 'Fixture API' if mode == 'custom' else 'Purpose unknown'
        if mode == 'custom':
            assert service.get('endpoint_label') == label, 'Explicit endpoint purpose missing'
            assert service.get('endpoint_label_source') == '.lhwatch.json', 'Purpose source missing'
        else:
            assert not service.get('endpoint_label'), 'Unknown endpoint purpose was guessed'
        terminal = Terminal([binary, '--config', config], env)
        try:
            terminal.wait_text(host['name'])
            terminal.check_project_filter(record, record['project'], source, host=host['name'],
                                          endpoint_label=label)
            terminal.stop_selected(record, lambda: remote.poll() is None, host=host['name'])
        finally:
            terminal.close()
        assert remote.wait(timeout=5) == 143, 'Expected graceful SIGTERM and fixture cleanup'
        cleanup = json.loads(remote.stdout.readline())
        assert cleanup == {'event': 'cleaned', 'signal': 15}, 'Fixture did not confirm cleanup'
        code, report = snapshot(binary, config, env)
        assert code == 0 and not find_service(report, host['name'], record), 'Stopped endpoint remained'
    print(f"PASS: {host['name']} {mode} naming, endpoint purpose, search/details, cancel, SIGTERM, cleanup and disappearance", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True, help='Existing LHWatch config JSON')
    parser.add_argument('--binary', type=Path, default=ROOT / 'bin/lhwatch')
    args = parser.parse_args()
    if not shutil.which('tmux'):
        parser.error('tmux is needed for terminal end-to-end checks')
    binary = args.binary.expanduser().resolve()
    settings = json.loads(args.config.expanduser().read_text())
    hosts = [host for host in settings.get('hosts', []) if host.get('ssh')]
    if not hosts:
        parser.error('Config must include at least one SSH host')
    env = dict(os.environ, TERM='xterm-256color', COLORTERM='truecolor')
    env.pop('NO_COLOR', None)
    # A disposable copy speeds observation without changing the user's config.
    with tempfile.TemporaryDirectory(prefix='lhwatch-hardware-config-') as directory:
        config = Path(directory) / 'config.json'
        settings['refresh_seconds'] = 1
        config.write_text(json.dumps(settings))
        code, _ = snapshot(binary, config, env)
        if code != 0:
            parser.error('All configured hosts must scan successfully before fixture testing')
        for host in hosts:
            for mode in ('custom', 'fallback'):
                check_host(binary, config, host, env, mode)
    print('All configured hardware smoke checks passed.', flush=True)


if __name__ == '__main__':
    main()
