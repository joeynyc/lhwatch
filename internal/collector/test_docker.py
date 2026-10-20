"""Docker naming needs ownership evidence and must never grant process control."""
import importlib.util
import json
import os
import subprocess
import socket
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('docker_helper', Path(__file__).with_name('helper.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
ENRICH = h.docker_enrich

CID = 'a' * 64
OTHER = 'b' * 64


def listener(**changes):
    service = dict(pid=0, uid=0, name='unknown', identity='', address='0.0.0.0',
                   port=8000, family='IPv4', _socket_inode='1234')
    service.update(changes)
    return service


def container(**changes):
    value = dict(id=CID, name='/qwen-server', image='example/model:1', running=True,
                 network_mode='host', pid_mode='', compose_project='', compose_service='', ports={})
    value.update(changes)
    return value


def owned(**changes):
    value = dict(inode='1234', address='0.0.0.0', port=8000, family='IPv4')
    value.update(changes)
    return value


class DockerNaming(unittest.TestCase):
    def enrich(self, services, containers=None, ownership=None, partial=False):
        containers = [container()] if containers is None else containers
        ownership = [owned()] if ownership is None else ownership
        calls = []

        def command(endpoint, args, deadline, **kwargs):
            calls.append(args)
            if args[0] == 'ps':
                output = '\n'.join(c['id'] for c in containers)
            elif args[0] == 'inspect':
                output = '\n'.join(json.dumps(c) for c in containers)
            elif args[0] == 'exec':
                output = json.dumps(dict(sockets=ownership, partial=partial))
            else:
                self.fail('Unexpected Docker command: %r' % args)
            return output

        with patch.object(h, 'docker_endpoint', return_value=('unix:///var/run/docker.sock', '')), patch.object(h, 'docker_command', side_effect=command), patch.object(h.sys, 'platform', 'linux'):
            warnings = ENRICH(services)
        return warnings, calls

    def test_host_network_requires_inode_and_endpoint_and_keeps_raw_identity(self):
        valid = listener()
        wrong_inode = listener(_socket_inode='4321', port=8000)
        wrong_port = listener(port=8001)
        wrong_address = listener(address='127.0.0.1')
        wrong_family = listener(family='IPv6')
        warnings, calls = self.enrich([valid, wrong_inode, wrong_port, wrong_address, wrong_family])
        self.assertEqual(warnings, [])
        self.assertEqual(valid['project'], 'qwen-server')
        self.assertEqual(valid['container_id'], CID)
        self.assertEqual(valid['project_source'], 'Docker container')
        self.assertEqual((valid['pid'], valid['uid'], valid['identity']), (0, 0, ''))
        self.assertFalse(valid['can_stop'])
        self.assertIn('container', valid['stop_reason'].lower())
        for service in [wrong_inode, wrong_port, wrong_address, wrong_family]:
            self.assertNotIn('container_id', service)
        self.assertEqual(len([c for c in calls if c[0] == 'exec']), 1)

    def test_compose_label_names_the_project_and_service(self):
        service = listener()
        self.enrich([service], [container(compose_project='studio', compose_service='api')])
        self.assertEqual(service['project'], 'studio / api')
        self.assertEqual(service['project_source'], 'Docker Compose service')

    def test_ambiguous_ownership_is_not_assigned_to_either_container(self):
        service = listener()
        warnings, _ = self.enrich([service], [container(), container(id=OTHER, name='/other')])
        self.assertNotIn('container_id', service)
        self.assertIn('ambiguous', service['visibility_reason'].lower())

    def test_shared_pid_namespace_is_never_used_as_ownership_evidence(self):
        for mode in ['host', 'container:' + OTHER]:
            with self.subTest(mode=mode):
                service = listener()
                _, calls = self.enrich([service], [container(pid_mode=mode)])
                self.assertNotIn('container_id', service)
                self.assertFalse(any(c[0] == 'exec' for c in calls))

    def test_published_port_needs_exact_unique_address_and_port(self):
        services = [listener(address='127.0.0.1', port=18000), listener(address='0.0.0.0', port=18000)]
        self.enrich(services, [container(network_mode='bridge', ports={'8000/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '18000'}]})])
        self.assertEqual(services[0]['container_id'], CID)
        self.assertNotIn('container_id', services[1])

    def test_pid_namespace_donors_are_not_used_as_ownership_evidence(self):
        for target in [CID, CID[:12], 'qwen-server', '/qwen-server']:
            with self.subTest(target=target):
                service = listener()
                warnings, calls = self.enrich([service], [container(),
                    container(id=OTHER, name='/joiner', pid_mode='container:' + target)])
                self.assertNotIn('container_id', service)
                self.assertFalse(any(c[0] == 'exec' for c in calls))
                self.assertTrue(warnings)

    def test_incomplete_inventory_cannot_prove_private_pid_namespace(self):
        containers = [container(id=format(i + 1, '064x'), name='/c' + str(i)) for i in range(25)]
        service = listener()
        warnings, calls = self.enrich([service], containers)
        self.assertNotIn('container_id', service)
        self.assertFalse(any(c[0] == 'exec' for c in calls))
        self.assertTrue(warnings)

    def test_unattributed_sibling_of_container_proxy_cannot_stop(self):
        known = listener(pid=400, identity='boot:400:1', address='127.0.0.1', port=18000, can_stop=True)
        sibling = dict(known, port=18001)
        reused_pid = dict(known, identity='boot:400:2', port=18002)
        self.enrich([known, sibling, reused_pid], [container(network_mode='bridge',
            ports={'8000/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '18000'}]})])
        self.assertFalse(known['can_stop'])
        self.assertFalse(sibling['can_stop'])
        self.assertIn('Docker', sibling['stop_reason'])
        self.assertNotIn('container_id', sibling)
        self.assertTrue(reused_pid['can_stop'])

    def test_docker_absent_or_unavailable_preserves_process_discovery(self):
        services = [listener()]
        with patch.object(h, 'docker_endpoint', return_value=('', '')):
            self.assertEqual(h.docker_enrich(services), [])
        for exc in [FileNotFoundError('docker absent'), subprocess.TimeoutExpired(['docker'], 1), RuntimeError('daemon unavailable')]:
            with self.subTest(exc=exc), patch.object(h, 'docker_endpoint', return_value=('unix:///var/run/docker.sock', '')), patch.object(h, 'docker_command', side_effect=exc):
                warnings = ENRICH(services)
                self.assertTrue(warnings)
                self.assertEqual(services[0]['port'], 8000)
                self.assertNotIn('container_id', services[0])

    def test_scan_strips_private_evidence_and_enforces_container_stop_refusal(self):
        raw = listener(pid=900001, uid=os.getuid(), identity='linux:boot:1', _executable='/usr/bin/python3')
        with patch.object(h.sys, 'platform', 'linux'), patch.object(h, 'linux_scan', return_value=([raw], [])), patch.object(h, 'protected_pids', return_value={0, 1}), patch.object(h, 'docker_enrich', side_effect=lambda services, **kwargs: self.enrich(services)[0]):
            service = h.scan()['services'][0]
        self.assertEqual(service['container_id'], CID)
        self.assertFalse(service['can_stop'])
        self.assertFalse(any(k.startswith('_') for k in service))

    def stop_with_docker(self, raws, target, containers):
        calls = []

        def command(endpoint, args, deadline, **kwargs):
            calls.append(args)
            if args[0] == 'ps':
                return '\n'.join(c['id'] for c in containers)
            if args[0] == 'inspect':
                return '\n'.join(json.dumps(c) for c in containers)
            self.fail('Stop must not run Docker command: %r' % args)

        with patch.object(h.sys, 'platform', 'linux'), patch.object(h, 'linux_scan', return_value=([dict(r) for r in raws], [])), patch.object(h, 'protected_pids', return_value={0, 1}), patch.object(h, 'docker_endpoint', return_value=('unix:///var/run/docker.sock', '')), patch.object(h, 'docker_command', side_effect=command), patch.object(h, 'open_pidfd', return_value=None), patch.object(h.os, 'kill') as kill:
            result = h.stop(target)
        return result, calls, kill

    def test_stop_respects_container_membership_without_exec_probes(self):
        raw = listener(pid=900001, uid=os.getuid(), identity='linux:boot:1', _container_process=True)
        target = {k: raw[k] for k in ['pid', 'identity', 'address', 'port', 'family']}
        result, calls, kill = self.stop_with_docker([raw], target, [container()])
        self.assertFalse(result['sent'])
        self.assertIn('container', result['message'].lower())
        self.assertFalse(any(c[0] == 'exec' for c in calls))
        kill.assert_not_called()

    def test_stop_refuses_process_that_proxies_a_published_container_port(self):
        # A rootless Docker port forwarder runs as the user; only its sibling
        # endpoint is targeted, so the collector itself must find the binding.
        proxy = listener(pid=900002, uid=os.getuid(), identity='linux:boot:2', address='127.0.0.1', port=18000)
        sibling = dict(proxy, port=18001, _socket_inode='5678')
        target = {k: sibling[k] for k in ['pid', 'identity', 'address', 'port', 'family']}
        published = container(network_mode='bridge', ports={'8000/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '18000'}]})
        result, calls, kill = self.stop_with_docker([proxy, sibling], target, [published])
        self.assertFalse(result['sent'])
        self.assertIn('Docker', result['message'])
        self.assertEqual([c[0] for c in calls], ['ps', 'inspect'])
        kill.assert_not_called()

    def test_stop_signals_ordinary_process_when_docker_is_unrelated(self):
        raw = listener(pid=900003, uid=os.getuid(), identity='linux:boot:3', address='127.0.0.1', port=18002)
        target = {k: raw[k] for k in ['pid', 'identity', 'address', 'port', 'family']}
        published = container(network_mode='bridge', ports={'8000/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '18000'}]})
        result, calls, kill = self.stop_with_docker([raw], target, [published, container(id=OTHER)])
        self.assertTrue(result['sent'])
        self.assertFalse(any(c[0] == 'exec' for c in calls))
        kill.assert_called_once_with(900003, h.signal.SIGTERM)

    def test_failed_or_malformed_container_probe_keeps_unidentified_listener(self):
        for probe in ['not json', 'null', '{"sockets": "wrong type"}', RuntimeError('python3 not installed')]:
            with self.subTest(probe=probe):
                services = [listener()]

                def command(endpoint, args, deadline):
                    if args[0] == 'ps':
                        return CID
                    if args[0] == 'inspect':
                        return json.dumps(container())
                    if isinstance(probe, Exception):
                        raise probe
                    return probe

                with patch.object(h, 'docker_endpoint', return_value=('unix:///var/run/docker.sock', '')), patch.object(h, 'docker_command', side_effect=command), patch.object(h.sys, 'platform', 'linux'):
                    warnings = h.docker_enrich(services)
                self.assertTrue(warnings)
                self.assertEqual(services[0]['port'], 8000)
                self.assertNotIn('container_id', services[0])
                self.assertIn('incomplete', services[0]['visibility_reason'])

    def test_partial_probe_only_names_proven_sockets(self):
        raw = [listener(), listener(port=8001, _socket_inode='4321')]
        with patch.object(h.sys, 'platform', 'linux'), patch.object(h, 'linux_scan', return_value=(raw, [])), patch.object(h, 'protected_pids', return_value={0, 1}), patch.object(h, 'docker_enrich', side_effect=lambda services, **kwargs: self.enrich(services, partial=True)[0]):
            snapshot = h.scan()
        proven, unknown = snapshot['services']
        self.assertEqual(proven['container_id'], CID)
        self.assertNotIn('container_id', unknown)
        self.assertFalse(proven['can_stop'])
        self.assertFalse(unknown['can_stop'])
        self.assertTrue(snapshot['partial'])
        self.assertTrue(any('incomplete' in warning for warning in snapshot['warnings']))

    def test_mac_never_uses_linux_vm_host_network_inode_as_mac_ownership(self):
        service = listener()
        with patch.object(h.sys, 'platform', 'darwin'), patch.object(h, 'docker_endpoint', return_value=('unix:///var/run/docker.sock', '')), patch.object(h, 'docker_command', side_effect=[CID, json.dumps(container())]) as command:
            warnings = h.docker_enrich([service])
        self.assertEqual(command.call_count, 2)
        self.assertNotIn('container_id', service)
        self.assertTrue(warnings)


class DockerProbeAndTransport(unittest.TestCase):
    def test_probe_requires_a_process_fd_not_just_shared_network_namespace(self):
        namespace = {'__name__': 'probe_test'}
        exec(h.DOCKER_PROBE, namespace)
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            (base / 'net').mkdir()
            (base / 'net/tcp').write_text('header\n'
                '0: 00000000:1F40 00000000:0000 0A 0:0 0:0 0 0 0 1234\n'
                '1: 00000000:1F41 00000000:0000 0A 0:0 0:0 0 0 0 4321\n')
            (base / '20/fd').mkdir(parents=True)
            (base / '20/fd/3').symlink_to('socket:[1234]')
            (base / '20/fd/4').symlink_to('socket:[1234]')
            (base / '20/fd/5').symlink_to('socket:[9999]')
            result = namespace['collect'](root)
        self.assertFalse(result['partial'])
        self.assertEqual(result['sockets'], [owned()])

    def test_probe_timeout_returns_no_unproven_sockets(self):
        namespace = {'__name__': 'probe_test'}
        exec(h.DOCKER_PROBE, namespace)
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            (base / 'net').mkdir()
            (base / 'net/tcp').write_text('header\n')
            result = namespace['collect'](root, budget=-1)
        self.assertTrue(result['partial'])
        self.assertEqual(result['sockets'], [])

    def test_local_socket_is_pinned_and_remote_context_environment_removed(self):
        endpoint = 'unix:///tmp/lhwatch-fixture-docker.sock'
        completed = subprocess.CompletedProcess([], 0, '', '')
        with patch.dict(h.os.environ, {'DOCKER_HOST': 'tcp://remote.invalid:2375', 'DOCKER_CONTEXT': 'remote', 'DOCKER_TLS_VERIFY': '1'}), patch.object(h.subprocess, 'run', return_value=completed) as run:
            h.docker_command(endpoint, ['ps', '-q'], time.monotonic() + 0.4)
        args, kwargs = run.call_args
        self.assertEqual(args[0], ['docker', '--host', endpoint, 'ps', '-q'])
        for name in ['DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_TLS_VERIFY']:
            self.assertNotIn(name, kwargs['env'])
        self.assertGreater(kwargs['timeout'], 0)
        self.assertLessEqual(kwargs['timeout'], 0.4)

    def test_remote_endpoint_cannot_relabel_local_listeners(self):
        with patch.object(h.shutil, 'which', return_value='/usr/bin/docker'), patch.dict(h.os.environ, {'DOCKER_HOST': 'ssh://remote'}), patch.object(h, 'docker_command') as command:
            endpoint, reason = h.docker_endpoint()
            self.assertFalse(endpoint)
            self.assertIn('local Unix socket', reason)
            warnings = h.docker_enrich([listener()])
            self.assertTrue(warnings)
            command.assert_not_called()

    def test_configured_socket_must_be_a_real_unix_socket(self):
        with tempfile.TemporaryDirectory() as root:
            path = str(Path(root) / 'docker.sock')
            with patch.object(h.shutil, 'which', return_value='/usr/bin/docker'), patch.dict(h.os.environ, {'DOCKER_HOST': 'unix://' + path}):
                Path(path).write_text('not a socket')
                self.assertFalse(h.docker_endpoint()[0])
                Path(path).unlink()
                sock = socket.socket(socket.AF_UNIX)
                try:
                    sock.bind(path)
                    self.assertEqual(h.docker_endpoint(), ('unix://' + path, ''))
                finally:
                    sock.close()

    def test_short_host_budget_survives_a_stalled_docker_command(self):
        original_run = subprocess.run
        timeouts = []

        def stalled(args, **kwargs):
            timeouts.append(kwargs['timeout'])
            return original_run([h.sys.executable, '-c', 'import time; time.sleep(2)'], **kwargs)

        with patch.object(h.sys, 'platform', 'linux'), patch.object(h, 'linux_scan', return_value=([listener()], [])), patch.object(h, 'protected_pids', return_value={0, 1}), patch.object(h, 'docker_endpoint', return_value=('unix:///tmp/docker.sock', '')), patch.object(h.subprocess, 'run', side_effect=stalled):
            began = time.monotonic()
            snapshot = h.scan(timeout_seconds=1)
            elapsed = time.monotonic() - began
        self.assertLessEqual(timeouts[0], 0.5)
        self.assertLess(elapsed, 1.5)
        self.assertEqual(snapshot['services'][0]['port'], 8000)
        self.assertTrue(snapshot['warnings'])

    def test_os_collection_time_is_deducted_from_docker_budget(self):
        with patch.object(h.sys, 'platform', 'linux'), patch.object(h, 'linux_scan', return_value=([listener()], [])), patch.object(h, 'protected_pids', return_value={0, 1}), patch.object(h.time, 'monotonic', side_effect=[100, 100.7]), patch.object(h, 'docker_enrich', return_value=[]) as enrich:
            h.scan(timeout_seconds=1)
        self.assertEqual(enrich.call_args.kwargs['budget'], 0)

    def test_container_membership_is_relative_to_collector(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            (base / 'self').mkdir()
            (base / '123').mkdir()
            docker_group = '0::/system.slice/docker-' + CID + '.scope\n'
            (base / '123/cgroup').write_text(docker_group)
            (base / 'self/cgroup').write_text('0::/user.slice/session.scope\n')
            self.assertTrue(h.other_container_cgroup(str(base / '123'), root))
            (base / 'self/cgroup').write_text(docker_group)
            self.assertFalse(h.other_container_cgroup(str(base / '123'), root))
            (base / '123/cgroup').write_text(docker_group.rstrip() + '/worker.slice\n')
            self.assertFalse(h.other_container_cgroup(str(base / '123'), root))
            (base / '123/cgroup').write_text('0::/user.slice/session.scope\n')
            self.assertFalse(h.other_container_cgroup(str(base / '123'), root))
            (base / '123/cgroup').unlink()
            self.assertFalse(h.other_container_cgroup(str(base / '123'), root))

    def test_other_container_runtimes_are_recognized(self):
        uid_slice = '/user.slice/user-1000.slice/user@1000.service/user.slice/'
        for cgroup in ['0::/system.slice/docker-' + CID + '.scope\n',
                       '12:pids:/docker/' + CID + '\n11:memory:/docker/' + CID + '\n',
                       '0::' + uid_slice + 'libpod-' + CID + '.scope/container\n',
                       '0::/machine.slice/libpod-' + CID + '.scope\n',
                       '0::/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod1234.slice/cri-containerd-' + CID + '.scope\n',
                       '0::/kubepods.slice/kubepods-pod1234.slice/crio-' + CID + '.scope\n',
                       '0::/kubepods/besteffort/pod0f1e2d3c/' + CID + '\n',
                       '0::/default/' + CID + '\n',
                       '0::/lxc.payload.web01/system.slice/nginx.service\n']:
            with self.subTest(cgroup=cgroup), tempfile.TemporaryDirectory() as root:
                base = Path(root)
                (base / 'self').mkdir()
                (base / '123').mkdir()
                (base / '123/cgroup').write_text(cgroup)
                (base / 'self/cgroup').write_text('0::/user.slice/session.scope\n')
                self.assertTrue(h.other_container_cgroup(str(base / '123'), root))
                (base / 'self/cgroup').write_text(cgroup)
                self.assertFalse(h.other_container_cgroup(str(base / '123'), root))
        for host in ['0::/user.slice/user-1000.slice/session-4.scope\n',
                     '0::/system.slice/run-r' + 'c' * 32 + '.scope\n',
                     '0::/user.slice/app-flatpak-org.example.App-1234.scope\n',
                     '0::/system.slice/' + 'a' * 63 + '.service\n']:
            with self.subTest(host=host):
                self.assertEqual(h.container_ids(host), set())

    def test_unknown_shared_endpoint_preserves_each_inode_for_attribution(self):
        services = [listener(), listener(_socket_inode='4321')]
        observed = []

        def enrich(values, **kwargs):
            observed.extend(service['_socket_inode'] for service in values)
            return []

        with patch.object(h.sys, 'platform', 'linux'), patch.object(h, 'linux_scan', return_value=(services, [])), patch.object(h, 'protected_pids', return_value={0, 1}), patch.object(h, 'docker_enrich', side_effect=enrich):
            snapshot = h.scan()
        self.assertEqual(observed, ['1234', '4321'])
        self.assertEqual(len(snapshot['services']), 2)
        self.assertTrue(all('_socket_inode' not in service for service in snapshot['services']))


if __name__ == '__main__':
    unittest.main()
