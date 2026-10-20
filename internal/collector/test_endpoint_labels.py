"""Endpoint purpose requires per-port settings or a verifiable server launch."""
import importlib.util
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('endpoint_helper', Path(__file__).with_name('helper.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)

CID = 'a' * 64


def listener(**changes):
    value = dict(pid=900001, uid=os.getuid(), identity='linux:boot:900001:1',
                 name='python', command='', cwd='', address='0.0.0.0',
                 port=30000, family='IPv4', _socket_inode='1234')
    value.update(changes)
    return value


def scan(services):
    with patch.object(h.sys, 'platform', 'linux'), patch.object(h, 'linux_scan', return_value=(services, [])), patch.object(h, 'protected_pids', return_value={0, 1}):
        return h.scan(include_docker=False)


class LaunchLabels(unittest.TestCase):
    def label(self, argv, port=30000, address='0.0.0.0', family='IPv4'):
        return h.launch_endpoint_label(argv, address, port, family)

    def test_explicit_launch_forms_label_only_primary_port(self):
        for argv, source in [
            (['sglang', 'serve', '--model-path', 'example/model', '--host', '0.0.0.0', '--port', '30000'], 'SGLang'),
            (['/usr/bin/python3', '-m', 'sglang.launch_server', '--port=30000'], 'SGLang'),
            (['/venv/bin/vllm', 'serve', 'example/model', '--host=0.0.0.0', '--port=30000'], 'vLLM'),
            (['python3.12', '-m', 'vllm.entrypoints.openai.api_server', '--port', '30000'], 'vLLM'),
            (['/opt/nvidia/nvidia_entrypoint.sh', 'vllm', 'serve', 'example/model', '--port', '30000'], 'vLLM'),
        ]:
            with self.subTest(argv=argv):
                self.assertEqual(self.label(argv), dict(endpoint_label='Model API', endpoint_label_source=source + ' explicit launch port'))
                self.assertEqual(self.label(argv, port=30001), {})

    def test_grpc_is_still_model_api(self):
        for argv in [
            ['sglang', 'serve', '--grpc-mode', '--port', '30000'],
            ['vllm', 'serve', 'example/model', '--grpc', '--port', '30000'],
        ]:
            self.assertEqual(self.label(argv)['endpoint_label'], 'Model API')

    def test_python_console_scripts_retain_explicit_launch_checks(self):
        for program in ('vllm', 'sglang'):
            argv = ['/venv/bin/python3.12', '/venv/bin/' + program, 'serve',
                    '--host', '0.0.0.0', '--port', '30000']
            with self.subTest(program=program):
                self.assertEqual(self.label(argv).get('endpoint_label'), 'Model API')
                self.assertEqual(self.label(argv, port=30001), {})
                self.assertEqual(self.label(argv, address='127.0.0.1'), {})
                self.assertEqual(self.label(argv + ['--headless']), {})
                self.assertEqual(self.label(argv + ['--port=30000']), {})
        for argv in [
            ['python3', '/work/report.py', 'vllm', 'serve', '--port', '30000'],
            ['python3', '-c', 'vllm', 'serve', '--port', '30000'],
            ['python3', '/venv/bin/vllm', 'chat', '--port', '30000'],
        ]:
            self.assertEqual(self.label(argv), {})

    def test_defaults_mentions_shells_and_ambiguous_launches_are_unknown(self):
        for argv in [
            ['sglang', 'serve'], ['vllm', 'serve', 'example/model'],
            ['bash', '-c', 'sglang serve --port 30000'],
            ['bash', '-c', 'vllm', 'serve', '--port', '30000'],
            ['python', 'report.py', 'vllm', 'serve', '--port', '30000'],
            ['python', '-c', 'vllm serve --port 30000'],
            ['echo', 'sglang', 'serve', '--port', '30000'],
            ['vllm', 'chat', '--port', '30000'],
            ['/other/nvidia_entrypoint.sh', 'vllm', 'serve', '--port', '30000'],
            ['vllm', 'serve', 'example/model', '--port 30000'],
            ['vllm', 'serve', 'example/model', '--port', '30000', '--port=30000'],
            ['vllm', 'serve', 'example/model', '--port', '30000', '--host', '0.0.0.0', '--host=127.0.0.1'],
            ['vllm', 'serve', 'example/model', '--port', '0'],
            ['vllm', 'serve', 'example/model', '--port', '65536'],
            ['vllm', 'serve', 'example/model', '--port', 'many'],
            ['vllm', 'serve', 'example/model', '--port'],
            ['vllm', 'serve', 'example/model', '--', '--port', '30000'],
            ['vllm', 'serve', 'example/model', '--port', '30000', '--headless'],
            ['vllm', 'serve', 'example/model', '--port', '30000', '--headless=true'],
            ['vllm', 'serve', 'example/model', '--port', '30000', '--uds', '/tmp/model.sock'],
            ['vllm', 'serve', 'example/model', '--port', '30000', '--api-server-count', '0'],
            ['sglang', 'serve', '--port', '30000', '--node-rank', '1'],
            None, '', ['vllm', 'serve', None, '--port', '30000'],
        ]:
            with self.subTest(argv=argv):
                self.assertEqual(self.label(argv), {})

    def test_explicit_bind_must_match_without_resolving_names(self):
        base = ['sglang', 'serve', '--port', '30000', '--host']
        self.assertEqual(self.label(base + ['127.0.0.1']), {})
        self.assertEqual(self.label(base + ['localhost']), {})
        self.assertEqual(self.label(base + ['']), {})
        self.assertEqual(self.label(base + ['::']), {})
        self.assertEqual(self.label(base + ['0:0:0:0:0:0:0:1'], address='::1', family='IPv6')['endpoint_label'], 'Model API')

    def test_python_http_server_needs_explicit_positional_port(self):
        argv = ['python3', '-m', 'http.server', '49199', '--bind', '127.0.0.1']
        self.assertEqual(self.label(argv, port=49199, address='127.0.0.1'),
                         dict(endpoint_label='HTTP server', endpoint_label_source='Python http.server explicit launch port'))
        for args in [[], ['--bind', '127.0.0.1'], ['49199', '49200'], ['--directory', '49199'], ['--port', '49199']]:
            with self.subTest(args=args):
                self.assertEqual(self.label(['python3', '-m', 'http.server'] + args, port=49199), {})


class ProjectLabels(unittest.TestCase):
    def test_project_mapping_is_per_port_overrides_launch_and_is_cached_per_scan(self):
        with tempfile.TemporaryDirectory() as root:
            project = Path(root) / 'my-app'
            (project / 'src').mkdir(parents=True)
            config = project / '.lhwatch.json'
            config.write_text(json.dumps(dict(name='My app', endpoints={'3000': 'Web UI', '8000': 'API', '30000': 'Model playground'})))
            services = [listener(cwd=str(project / 'src'), port=p,
                                 _argv=['sglang', 'serve', '--port', str(p)]) for p in (3000, 8000, 30000)]
            services.append(listener(cwd=str(project / 'src'), port=5432))
            with patch.object(h.os, 'open', wraps=h.os.open) as opened:
                result = scan(services)
            self.assertEqual(sum(call.args[0] == str(config) for call in opened.call_args_list), 1)
            self.assertEqual([s.get('endpoint_label', '') for s in result['services']], ['Web UI', '', 'API', 'Model playground'])
            self.assertTrue(all(s['project_path'] == str(project) for s in result['services']))
            self.assertTrue(all(s.get('endpoint_label_source') == '.lhwatch.json' for s in result['services'] if s.get('endpoint_label')))
            config.write_text('{"name":"My app","endpoints":{"3000":"New UI"}}')
            self.assertEqual(scan([listener(cwd=str(project / 'src'), port=3000)])['services'][0]['endpoint_label'], 'New UI')

    def test_malformed_settings_do_not_disable_scans_or_stopping(self):
        with tempfile.TemporaryDirectory() as root:
            config = Path(root) / '.lhwatch.json'
            for text in ['null', '[]', '{', '{"endpoints":[]}',
                         '{"endpoints":{"30000":42}}', '{"endpoints":{"30000":" "}}',
                         json.dumps({'endpoints': {'30000': 'x' * 121}}),
                         json.dumps({'endpoints': {'30000': 'API\nControl'}}),
                         json.dumps({'endpoints': {'30000': '\x1b[31mAPI'}}),
                         json.dumps({'endpoints': {'30000': 'API'}, 'padding': 'x' * 131072})]:
                with self.subTest(text=text[:80]):
                    config.write_text(text)
                    result = scan([listener(cwd=root)])
                    service = result['services'][0]
                    self.assertEqual(service.get('endpoint_label', ''), '')
                    self.assertTrue(service['can_stop'])
                    self.assertFalse(result['partial'])

    def test_valid_entries_survive_invalid_neighbors_and_trim_whitespace(self):
        with tempfile.TemporaryDirectory() as root:
            (Path(root) / '.lhwatch.json').write_text(json.dumps({'endpoints': {'30000': '  API  ', '30001': False, '30002': ''}}))
            services = scan([listener(cwd=root, port=p) for p in (30000, 30001, 30002)])['services']
            self.assertEqual([s.get('endpoint_label', '') for s in services], ['API', '', ''])


class CollectorLabels(unittest.TestCase):
    def test_container_cwd_cannot_use_unrelated_host_project_purpose(self):
        with tempfile.TemporaryDirectory() as root:
            (Path(root) / '.lhwatch.json').write_text(json.dumps({
                'name': 'Host project', 'endpoints': {'30000': 'Host Web UI'}}))
            unknown = listener(cwd=root, _container_process=True)
            launched = listener(cwd=root, _container_process=True,
                                _argv=['sglang', 'serve', '--port', '30000'])
            for item, label in ((unknown, ''), (launched, 'Model API')):
                service = scan([item])['services'][0]
                self.assertEqual(service.get('endpoint_label', ''), label)
                self.assertNotEqual(service.get('endpoint_label_source'), '.lhwatch.json')
                self.assertEqual(service['identity'], item['identity'])
                self.assertFalse(service['can_stop'])

    def test_scan_keeps_identity_and_stop_eligibility_and_strips_argv(self):
        native = listener(_argv=['sglang', 'serve', '--port', '30000'])
        extra = listener(port=30001, _argv=native['_argv'])
        unknown = listener(pid=0, uid=0, identity='', port=30002)
        services = scan([native, extra, unknown])['services']
        self.assertEqual([s.get('endpoint_label', '') for s in services], ['Model API', '', ''])
        self.assertEqual([s['can_stop'] for s in services], [True, True, False])
        self.assertEqual([s['identity'] for s in services], ['linux:boot:900001:1', 'linux:boot:900001:1', ''])
        self.assertFalse(any(k.startswith('_') for s in services for k in s))
        self.assertNotIn('_argv', json.dumps(services))

    def test_flattened_command_does_not_recover_lost_argument_boundaries(self):
        services = scan([listener(command='sglang serve --port 30000'),
                         listener(port=30001, name='vllm', command='python report.py --note "vllm serve --port 30001"')])['services']
        self.assertTrue(all(not s.get('endpoint_label') for s in services))

    def test_linux_scan_retains_real_argv_until_labeling(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            (base / 'net').mkdir()
            (base / 'net/tcp').write_text('header\n0: 0100007F:C02F 00000000:0000 0A 0:0 0:0 0 0 0 1234\n')
            (base / 'sys/kernel/random').mkdir(parents=True)
            (base / 'sys/kernel/random/boot_id').write_text('test-boot')
            proc = base / '900001'
            (proc / 'fd').mkdir(parents=True)
            (proc / 'fd/3').symlink_to('socket:[1234]')
            (proc / 'stat').write_text('900001 (python3) S 22 ' + '0 ' * 17 + '98765 0 0')
            argv = ['python3', '-m', 'http.server', '49199', '--bind', '127.0.0.1']
            (proc / 'cmdline').write_text('\x00'.join(argv) + '\x00')
            raw, warnings = h.linux_scan(root)
            self.assertEqual(warnings, [])
            self.assertEqual(raw[0]['_argv'], argv)
            self.assertEqual(scan(raw)['services'][0]['endpoint_label'], 'HTTP server')


class DockerLabels(unittest.TestCase):
    def enrich(self, services, path='sglang', args=None, **changes):
        container = dict(id=CID, name='/qwen-server', image='example/model:1', running=True,
                         network_mode='host', pid_mode='', compose_project='', compose_service='', ports={},
                         path=path, args=args if args is not None else ['serve', '--port', '30000', '--host', '0.0.0.0'])
        container.update(changes)
        ownership = [dict(inode=s['_socket_inode'], address=s['address'], port=s['port'], family=s['family']) for s in services if s['_socket_inode'] != 'wrong']

        def command(endpoint, argv, deadline):
            if argv[0] == 'ps':
                return CID
            if argv[0] == 'inspect':
                self.assertIn('{{json .Path}}', argv[2])
                self.assertIn('{{json .Args}}', argv[2])
                self.assertNotIn('.Config.Env', argv[2])
                return json.dumps(container)
            return json.dumps(dict(sockets=ownership, partial=False))

        with patch.object(h.sys, 'platform', 'linux'), patch.object(h, 'docker_endpoint', return_value=('unix:///tmp/docker.sock', '')), patch.object(h, 'docker_command', side_effect=command):
            return h.docker_enrich(services)

    def test_only_attributed_primary_host_port_gets_model_api(self):
        services = [listener(), listener(port=30001), listener(_socket_inode='wrong')]
        self.assertEqual(self.enrich(services), [])
        self.assertEqual([s.get('endpoint_label', '') for s in services], ['Model API', '', ''])
        self.assertFalse(services[0]['can_stop'])
        self.assertEqual(services[0]['identity'], 'linux:boot:900001:1')
        self.assertNotIn('container_id', services[2])

    def test_ngc_wrapper_and_bind_are_checked_and_args_stay_private(self):
        services = [listener(port=8000), listener(port=8000, address='127.0.0.1')]
        self.enrich(services, path='/opt/nvidia/nvidia_entrypoint.sh', args=['vllm', 'serve', 'example/model', '--api-key', 'private-example', '--port', '8000', '--host', '0.0.0.0'])
        self.assertEqual([s.get('endpoint_label', '') for s in services], ['Model API', ''])
        self.assertEqual(services[0]['endpoint_label_source'], 'vLLM explicit launch port')
        serialized = json.dumps(services)
        self.assertNotIn('private-example', serialized)
        self.assertNotIn('"args"', serialized)

    def test_unknown_container_launch_preserves_ordinary_purpose(self):
        service = listener(endpoint_label='User web UI', endpoint_label_source='.lhwatch.json')
        self.enrich([service], path='bash', args=['-c', 'vllm serve model --port 30000'])
        self.assertEqual(service['endpoint_label'], 'User web UI')
        self.assertEqual(service['endpoint_label_source'], '.lhwatch.json')
        self.enrich([service])
        self.assertEqual(service['endpoint_label'], 'User web UI')

    def test_published_port_translation_and_image_names_do_not_invent_purpose(self):
        service = listener(port=18000, address='127.0.0.1')
        self.enrich([service], network_mode='bridge', ports={'30000/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '18000'}]})
        self.assertEqual(service['container_id'], CID)
        self.assertFalse(service.get('endpoint_label'))
        service = listener()
        self.enrich([service], path='python3', args=['worker.py', 'sglang serve --port 30000'])
        self.assertEqual(service['container_id'], CID)
        self.assertFalse(service.get('endpoint_label'))


if __name__ == '__main__':
    unittest.main()
