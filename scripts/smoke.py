#!/usr/bin/env python3
"""Exercise the real executable and terminal. --docker adds a real SSH Linux host.

Only listeners and containers created by this script are stopped. tmux uses an
isolated socket, and SSH uses disposable keys and a private known_hosts file.
"""
import argparse
import contextlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
LISTENER = """import json,os,signal,socket
s=socket.socket(socket.AF_INET6 if os.environ.get('TEST_IPV6') else socket.AF_INET)
s.bind(('::1' if os.environ.get('TEST_IPV6') else '127.0.0.1',0))
s.listen(4)
print(json.dumps({'pid':os.getpid(),'port':s.getsockname()[1]}),flush=True)
signal.pause()
"""


def command(args, **kwargs):
    return subprocess.run([str(x) for x in args], check=True, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs)


def await_condition(fn, timeout=20, message="condition"):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            last = fn()
            if last:
                return last
        except (subprocess.CalledProcessError, json.JSONDecodeError, OSError):
            pass
        time.sleep(0.15)
    raise AssertionError(f"Timed out waiting for {message}; last result: {last!r}")


@contextlib.contextmanager
def listener(workdir, ipv6=False):
    env = os.environ.copy()
    if ipv6:
        env['TEST_IPV6'] = '1'
    process = subprocess.Popen([sys.executable, '-u', '-c', LISTENER], cwd=workdir,
                               env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        record = json.loads(process.stdout.readline())
        yield process, record
    finally:
        if process.poll() is None:
            process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()  # Only this script's own disposable fixture.
            process.wait(timeout=5)
        process.stdout.close()
        process.stderr.close()


def confirmation_matches(screen, host, record):
    if 'CONFIRM PROCESS STOP' not in screen:
        return False
    lines = [line.strip(' │') for line in screen.splitlines()]
    def value_after(label):
        try:
            return lines[lines.index(label) + 1]
        except (ValueError, IndexError):
            return ''
    process = value_after('PROCESS / PID')
    endpoint = f"127.0.0.1:{record['port']}  IPv4"
    return (value_after('HOST') == host and
            process.rpartition(' / ')[2] == str(record['pid']) and
            value_after('ALL KNOWN ENDPOINTS AFFECTED') == endpoint)


def selected_service_matches(screen, host, record, project):
    expected = f"› {project} {record['port']} {host}"
    return any(' '.join(cell.split()) == expected
               for line in screen.splitlines() for cell in line.split('│'))


def selected_port_matches(screen, host, record):
    pattern = r'^› .+ (?<!\d)' + str(record['port']) + r'(?!\d).* ' + re.escape(host) + r'$'
    return any(re.match(pattern, ' '.join(cell.split()))
               for line in screen.splitlines() for cell in line.split('│'))


def service_details_match(screen, host, record, project, source):
    lines = [line.strip(' │') for line in screen.splitlines()]
    def value_after(label):
        try:
            return lines[lines.index(label) + 1]
        except (ValueError, IndexError):
            return ''
    endpoint = f"127.0.0.1:{record['port']}"
    return (value_after('PROJECT / SERVICE') == project and
            value_after('PROCESS / PID').rpartition(' / ')[2] == str(record['pid']) and
            value_after('HOST') == host and value_after('NAME SOURCE') == source and
            any(line == endpoint or line.split()[-2:] == [endpoint, 'IPv4'] for line in lines))


class Terminal:
    def __init__(self, cmd, env, width=132, height=40):
        self.socket = 'lhwatch-e2e-' + uuid.uuid4().hex[:10]
        self.env = env
        command(['tmux', '-L', self.socket, '-f', '/dev/null', 'new-session', '-d',
                 '-s', 'check', '-x', width, '-y', height,
                 'exec ' + shlex.join([str(x) for x in cmd])], env=env)

    def invoke(self, *args):
        return command(['tmux', '-L', self.socket, *args], env=self.env).stdout

    def capture(self):
        return self.invoke('capture-pane', '-p', '-t', 'check:0.0')

    def send(self, *keys):
        self.invoke('send-keys', '-t', 'check:0.0', *keys)
        if 'Escape' in keys:
            # Allow the terminal decoder to disambiguate bare Escape from Alt+key.
            time.sleep(0.15)

    def text(self, text):
        self.invoke('send-keys', '-t', 'check:0.0', '-l', text)

    def wait_text(self, text):
        return self.wait_screen(lambda screen: text in screen, f"terminal text {text!r}")

    def wait_screen(self, predicate, message):
        try:
            return await_condition(lambda: predicate(self.capture()), message=message)
        except AssertionError:
            failure = ROOT / '.local' / 'smoke-failure.txt'
            failure.parent.mkdir(exist_ok=True)
            failure.write_text(self.capture())
            print(f'Terminal failure captured in {failure}', file=sys.stderr)
            raise

    def check_project_filter(self, record, project, source, host='local', endpoint_label=None):
        self.text('/')
        self.text(project)
        self.send('Enter')
        self.wait_screen(lambda screen: selected_service_matches(screen, host, record, project),
                         f"selected {host} fixture row named {project!r}")
        self.send('Enter')
        self.wait_screen(lambda screen: service_details_match(screen, host, record, project, source),
                         f"exact {host} fixture project and process details")
        if endpoint_label:
            expected = f"{endpoint_label} 127.0.0.1:{record['port']} IPv4"
            self.wait_screen(lambda screen: any(' '.join(line.strip(' │').split()) == expected
                                                for line in screen.splitlines()),
                             f"purpose label left of the {host} fixture endpoint")
        self.send('Escape')
        self.send('Escape')

    def stop_selected(self, record, alive, host="local"):
        self.text('/')
        self.text(str(record['port']))
        self.send('Enter')
        self.wait_screen(lambda s: selected_port_matches(s, host, record),
                         'selected fixture port and host')
        self.text('x')
        self.wait_text('CONFIRM PROCESS STOP')
        self.send('Escape')
        assert alive(), 'Cancel unexpectedly stopped the test process'
        self.text('x')
        self.wait_text('CONFIRM PROCESS STOP')
        if not confirmation_matches(self.capture(), host, record):
            self.send('Escape')
            raise AssertionError('Refusing to confirm a target that is not the disposable fixture')
        self.text('y')
        await_condition(lambda: not alive(), message='confirmed process exit')
        await_condition(lambda: 'No matches' in self.capture() or 'No listeners' in self.capture()
                        or '0 endpoint' in self.capture().lower(), message='stopped listener removed')
        self.send('Escape')

    def close(self):
        subprocess.run(['tmux', '-L', self.socket, 'kill-server'], stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL)


@contextlib.contextmanager
def linux_host(workdir, env):
    name = 'lhwatch-e2e-' + uuid.uuid4().hex[:10]
    key = workdir / 'id_test'
    command(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', key, '-C', 'lhwatch-test'])
    print('Building disposable Linux SSH fixture…', flush=True)
    subprocess.run(['docker', 'build', '-q', '-t', 'lhwatch-e2e:local', ROOT / 'scripts/e2e'], check=True)
    command(['docker', 'run', '-d', '--rm', '--name', name, '-p', '127.0.0.1::2222', 'lhwatch-e2e:local'])
    try:
        command(['docker', 'cp', str(key) + '.pub', name + ':/home/lab/.ssh/authorized_keys'])
        command(['docker', 'exec', name, 'chown', 'lab:lab', '/home/lab/.ssh/authorized_keys'])
        command(['docker', 'exec', name, 'chmod', '600', '/home/lab/.ssh/authorized_keys'])
        port = int(command(['docker', 'port', name, '2222/tcp']).stdout.strip().split(':')[-1])
        pub = command(['docker', 'exec', name, 'cat', '/etc/ssh/ssh_host_ed25519_key.pub']).stdout.split()
        known = workdir / 'known_hosts'
        known.write_text(f"[127.0.0.1]:{port} {pub[0]} {pub[1]}\n")
        bindir = workdir / 'bin'
        bindir.mkdir()
        ssh = shutil.which('ssh')
        wrapper = bindir / 'ssh'
        wrapper.write_text('#!/bin/sh\nexec ' + shlex.quote(ssh) + ' -o ' +
                           shlex.quote('UserKnownHostsFile=' + str(known)) + ' "$@"\n')
        wrapper.chmod(0o700)
        env = dict(env, PATH=str(bindir) + os.pathsep + env['PATH'])
        host = {'name': 'linux-lab', 'ssh': 'lab@127.0.0.1', 'port': port, 'identity_file': str(key)}
        await_condition(lambda: command([wrapper, '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
                                        '-i', key, '-p', port, 'lab@127.0.0.1', 'true']).returncode == 0,
                        message='SSH authentication')
        yield name, host, env
    finally:
        command(['docker', 'rm', '-f', name])


def snapshot(binary, config, env):
    result = subprocess.run([str(binary), '--config', str(config), '--json'], env=env,
                            capture_output=True, text=True, timeout=30)
    return result.returncode, json.loads(result.stdout)


def find_service(report, host, record):
    return [s for h in report['hosts'] if h['host'] == host for s in h['services']
            if s['port'] == record['port'] and s['pid'] == record['pid']]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--docker', action='store_true')
    args = parser.parse_args()
    if not shutil.which('tmux'):
        parser.error('tmux is needed for terminal end-to-end checks')
    subprocess.run(['go', 'build', '-o', ROOT / 'bin/lhwatch', '.'], cwd=ROOT, check=True)
    binary = ROOT / 'bin/lhwatch'
    env = dict(os.environ, TERM='xterm-256color', COLORTERM='truecolor')
    env.pop('NO_COLOR', None)
    with tempfile.TemporaryDirectory(prefix='lhwatch-smoke-') as temp:
        workdir = Path(temp)
        (workdir / '.lhwatch.json').write_text('{"name":"Smoke Website"}')
        config = workdir / 'config.json'
        config.write_text('{"refresh_seconds":1,"timeout_seconds":3}')
        with listener(workdir) as (process, record):
            (workdir / '.lhwatch.json').write_text(json.dumps({
                'name': 'Smoke Website', 'endpoints': {str(record['port']): 'Fixture API'}}))
            code, report = snapshot(binary, config, env)
            assert code == 0 and find_service(report, 'local', record), 'Local listener not discovered'
            assert find_service(report, 'local', record)[0]['project'] == 'Smoke Website', 'Local project name missing'
            assert find_service(report, 'local', record)[0]['can_stop'], 'Own test listener not stoppable'
            assert find_service(report, 'local', record)[0]['endpoint_label'] == 'Fixture API'
            assert find_service(report, 'local', record)[0]['endpoint_label_source'] == '.lhwatch.json'
            terminal = Terminal([binary, '--config', config], env)
            try:
                terminal.wait_text('lhwatch')
                terminal.check_project_filter(record, 'Smoke Website', 'Custom name (.lhwatch.json)',
                                              endpoint_label='Fixture API')
                terminal.text('?'); terminal.wait_text('KEYBOARD GUIDE'); terminal.send('Escape')
                terminal.stop_selected(record, lambda: process.poll() is None)
            finally:
                terminal.close()
            assert process.wait(timeout=5) == -15, 'Expected graceful SIGTERM'
        print('PASS: local snapshot, purpose label, keyboard filter, cancel, confirmed SIGTERM, disappearance', flush=True)
        # An independent IPv6 listener proves this is not an IPv4-only scan.
        try:
            probe = socket.socket(socket.AF_INET6); probe.bind(('::1', 0)); probe.close()
        except OSError:
            print('SKIP: IPv6 loopback unavailable', flush=True)
        else:
            with listener(workdir, ipv6=True) as (_, record):
                code, report = snapshot(binary, config, env)
                assert code == 0 and find_service(report, 'local', record), 'IPv6 listener missing'
            print('PASS: live IPv6 loopback discovery', flush=True)
        # A real failed SSH connection must not erase the local result.
        config.write_text(json.dumps({'timeout_seconds': 1, 'hosts': [
            {'name': 'offline', 'ssh': 'localhost', 'port': 1}]}))
        code, report = snapshot(binary, config, env)
        assert code == 1 and len(report['hosts']) == 2
        assert report['hosts'][0].get('error', '') == '' and report['hosts'][1]['error']
        print('PASS: mixed-host JSON preserves local success and reports SSH failure', flush=True)
        demo = Terminal([binary, '--demo'], env, 132, 40)
        try:
            demo.wait_text('DEMO')
            demo.invoke('resize-window', '-t', 'check', '-x', '80', '-y', '24')
            demo.text('/'); demo.text('never-matches-xyz'); demo.send('Enter')
            demo.wait_text('No matches')
            demo.send('Escape'); demo.text('q')
            await_condition(lambda: subprocess.run(['tmux', '-L', demo.socket, 'has-session'],
                            capture_output=True).returncode != 0, message='clean terminal quit')
        finally:
            demo.close()
        print('PASS: demo, narrow terminal, empty search, clear and quit', flush=True)
        if args.docker:
            with linux_host(workdir, env) as (container, host, sshenv), listener(workdir) as (_, local_record):
                config.write_text(json.dumps({'refresh_seconds': 1, 'timeout_seconds': 5, 'hosts': [host]}))
                remote = subprocess.Popen(['docker', 'exec', '--user', 'lab', container,
                                           'python3', '-u', '-c', """import pathlib,os
p=pathlib.Path('/tmp/smoke-project')
p.mkdir(exist_ok=True)
(p/'package.json').write_text('{"name":"remote-catalog-api"}')
# Under the metadata byte limit, but too deeply nested for the JSON decoder.
(p/'.lhwatch.json').write_text('{"name":' + '['*10000 + '0' + ']'*10000 + '}')
os.chdir(p)
""" + LISTENER], stdout=subprocess.PIPE,
                                          stderr=subprocess.PIPE, text=True)
                try:
                    record = json.loads(remote.stdout.readline())
                    code, report = snapshot(binary, config, sshenv)
                    assert code == 0 and find_service(report, 'linux-lab', record), 'SSH listener missing'
                    assert find_service(report, 'local', local_record), 'Remote metadata failure erased local listener'
                    assert all(not h.get('error') for h in report['hosts']), 'Metadata failure broke a host observation'
                    assert find_service(report, 'linux-lab', record)[0]['project'] == 'remote-catalog-api', 'Remote package name missing'
                    assert find_service(report, 'linux-lab', record)[0]['project_source'] == 'Package name (package.json)', 'Deep custom metadata did not fall back to package name'
                    arch = command(['docker', 'exec', container, 'uname', '-m']).stdout.strip()
                    goarch = {'aarch64': 'arm64', 'x86_64': 'amd64'}[arch]
                    linuxbin = workdir / 'lhwatch-linux'
                    command(['go', 'build', '-o', linuxbin, '.'], cwd=ROOT,
                            env=dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH=goarch))
                    command(['docker', 'cp', linuxbin, container + ':/tmp/lhwatch'])
                    local_report = json.loads(command(['docker', 'exec', '--user', 'lab', container,
                                                       '/tmp/lhwatch', '--json']).stdout)
                    assert find_service(local_report, 'local', record), 'Linux-native binary did not discover listener'
                    assert find_service(local_report, 'local', record)[0]['project'] == 'remote-catalog-api', 'Linux-native metadata fallback failed'
                    terminal = Terminal([binary, '--config', config], sshenv)
                    try:
                        terminal.wait_text('linux-lab')
                        terminal.check_project_filter(record, 'remote-catalog-api', 'Package name (package.json)', host='linux-lab')
                        terminal.stop_selected(record, lambda: remote.poll() is None, host="linux-lab")
                    finally:
                        terminal.close()
                    remote.wait(timeout=5)
                    code, report = snapshot(binary, config, sshenv)
                    assert code == 0 and not find_service(report, 'linux-lab', record), 'Remote port remained'
                    assert find_service(report, 'local', local_record), 'Remote stop erased local listener'
                    print(f'PASS: real SSH metadata fallback, project filter and confirmed stop; native Linux {goarch} execution', flush=True)
                finally:
                    if remote.poll() is None:
                        # The container teardown below terminates only this isolated fixture.
                        remote.terminate()
                    remote.wait(timeout=5)
                    remote.stdout.close(); remote.stderr.close()
    print('All requested smoke checks passed.', flush=True)


if __name__ == '__main__':
    main()
