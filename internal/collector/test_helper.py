import importlib.util
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('helper', os.path.join(os.path.dirname(__file__), 'helper.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)

class StopSafety(unittest.TestCase):
    def setUp(self):
        self.target = dict(pid=999999, identity='boot:123', address='127.0.0.1', port=54321, family='IPv4')
        self.service = dict(self.target, uid=os.getuid())

    def refuse(self, services, target=None):
        with patch.object(h, 'scan', return_value={'services': services}), patch.object(h, 'open_pidfd', return_value=None), patch.object(h, 'protected_pids', return_value={1, os.getpid()}), patch.object(h.os, 'kill') as kill:
            result = h.stop(target or self.target)
            self.assertFalse(result['sent'])
            kill.assert_not_called()

    def test_changed_identity_refuses(self):
        self.refuse([dict(self.service, identity='boot:124')])

    def test_missing_listener_refuses(self):
        self.refuse([])

    def test_foreign_owner_refuses(self):
        self.refuse([dict(self.service, uid=os.getuid()+1)])

    def test_missing_identity_refuses(self):
        self.refuse([self.service], dict(self.target, identity=''))

    def test_changed_endpoint_refuses(self):
        self.refuse([dict(self.service, port=54322)])

    def test_protected_pid_refuses(self):
        self.refuse([self.service], dict(self.target, pid=1))

    def test_ancestor_and_ancestry_failure_refuse_before_signal(self):
        for outcome in [{1, self.target['pid']}, RuntimeError('ancestry unavailable')]:
            with self.subTest(outcome=outcome):
                kwargs = {'side_effect': outcome} if isinstance(outcome, Exception) else {'return_value': outcome}
                with patch.object(h, 'protected_pids', **kwargs), patch.object(h, 'open_pidfd') as acquire, patch.object(h, 'scan') as scan, patch.object(h.os, 'kill') as kill, patch.object(h.signal, 'pidfd_send_signal', create=True) as send:
                    self.assertFalse(h.stop(self.target)['sent'])
                    acquire.assert_not_called()
                    scan.assert_not_called()
                    kill.assert_not_called()
                    send.assert_not_called()

class Parsing(unittest.TestCase):
    def test_linux_ipv4_ipv6(self):
        self.assertEqual(h.linux_address('0100007F:1F90', 'IPv4'), ('127.0.0.1', 8080))
        self.assertEqual(h.linux_address('00000000000000000000000001000000:01BB', 'IPv6'), ('::1', 443))
        self.assertEqual(h.linux_address('00000000:0050', 'IPv4'), ('0.0.0.0', 80))

    def test_stat_command_with_parentheses_and_spaces(self):
        stat = h.parse_linux_stat('321 (my worker ) (one)) S 22 ' + '0 '*17 + '98765 0 0')
        self.assertEqual(stat, dict(pid=321, name='my worker ) (one)', ppid=22, start_ticks='98765'))

    def test_lsof_ipv6_wildcard_and_duplicate_descriptors(self):
        text = 'p321\ncnode\nu501\nf4\ntIPv4\nn127.0.0.1:3000\nTST=LISTEN\nf5\ntIPv4\nn127.0.0.1:3000\np456\ncPython\nu501\nf8\ntIPv6\nn*:8080\nf9\ntIPv6\nn[::1]:9090\n'
        services = h.parse_lsof(text)
        self.assertEqual([(s['pid'], s['address'], s['port']) for s in services], [(321, '127.0.0.1', 3000), (321, '127.0.0.1', 3000), (456, '::', 8080), (456, '::1', 9090)])
        with patch.object(h.sys, 'platform', 'darwin'), patch.object(h, 'mac_scan', return_value=(services, [])), patch.object(h, 'protected_pids', return_value={1}):
            self.assertEqual(len(h.scan(include_docker=False)['services']), 3)

    def test_mac_full_command_and_lstart(self):
        output = ' 321 22 501 Thu Oct  1 12:34:56 2026 /a path/python3 my script.py\n'
        with patch.object(h, 'run', return_value=h.subprocess.CompletedProcess([], 0, output, '')):
            info = h.mac_processes()[321]
        self.assertEqual(info['started'], 'Thu Oct 1 12:34:56 2026')
        self.assertEqual(info['command'], '/a path/python3 my script.py')

    def test_mac_executables_preserve_paths_and_ignore_malformed_lines(self):
        output = (' 321 /Applications/My App.app/Contents/MacOS/My App\n'
                  'not-a-pid /Applications/Other.app/Contents/MacOS/Other\n'
                  '456\n\n'
                  ' 789 /usr/local/bin/python3\n')
        with patch.object(h, 'run', return_value=h.subprocess.CompletedProcess([], 0, output, '')):
            self.assertEqual(h.mac_executables(), {
                321: '/Applications/My App.app/Contents/MacOS/My App',
                789: '/usr/local/bin/python3',
            })

    def test_mac_executables_are_optional_when_ps_fails(self):
        with patch.object(h, 'run', return_value=h.subprocess.CompletedProcess([], 1, '', 'permission denied')):
            self.assertEqual(h.mac_executables(), {})
        with patch.object(h, 'run', side_effect=OSError('ps unavailable')):
            self.assertEqual(h.mac_executables(), {})

    def test_lsof_empty_is_success_but_failure_is_not(self):
        with patch.object(h, 'run', return_value=h.subprocess.CompletedProcess([], 1, '', '')):
            services, warnings = h.mac_scan()
            self.assertEqual(services, [])
        for status, stderr in [(1, 'permission denied'), (127, 'lsof missing'), (2, '')]:
            with self.subTest(status=status), patch.object(h, 'run', return_value=h.subprocess.CompletedProcess([], status, '', stderr)):
                with self.assertRaises(RuntimeError):
                    h.mac_scan()

    def test_linux_unknown_and_shared_ownership(self):
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as root:
            p = Path(root)
            (p/'net').mkdir()
            (p/'net/tcp').write_text('header\n 0: 0100007F:1F90 00000000:0000 0A 0:0 0:0 0 501 0 12345\n 1: 00000000:0050 00000000:0000 0A 0:0 0:0 0 0 0 98765\n')
            (p/'sys/kernel/random').mkdir(parents=True)
            (p/'sys/kernel/random/boot_id').write_text('test-boot')
            for pid in [321, 456]:
                base = p/str(pid)
                (base/'fd').mkdir(parents=True)
                (base/'stat').write_text(str(pid)+' (test worker) S 22 '+'0 '*17+'98765 0 0')
                (base/'cmdline').write_text('/path with spaces/server\x00--port\x008080\x00')
                (base/'fd/3').symlink_to('socket:[12345]')
                (base/'fd/4').symlink_to('socket:[12345]')
                (base/'fd/5').symlink_to('socket:[55555]')
                (base/'cwd').symlink_to('/working directory')
                if pid == 321:
                    (base/'exe').symlink_to('/path with spaces/server')
            services, warnings = h.linux_scan(root)
            self.assertEqual(len(services), 3)
            known = [s for s in services if s['pid']]
            self.assertEqual({s['pid'] for s in known}, {321, 456})
            self.assertTrue(all(s['command'] == '/path with spaces/server --port 8080' for s in known))
            self.assertTrue(all(s['cwd'] == '/working directory' for s in known))
            self.assertEqual({s['pid']: s.get('_executable', '') for s in known},
                             {321: '/path with spaces/server', 456: ''})
            unknown = next(s for s in services if not s['pid'])
            self.assertEqual((unknown['port'], unknown['uid'], unknown['identity']), (80, 0, ''))
            self.assertTrue(warnings)
            original_read = h.read_text
            def unavailable(path):
                if path.endswith('/321/stat'):
                    raise PermissionError('permission denied')
                if path.endswith('/456/stat'):
                    raise FileNotFoundError('process exited')
                return original_read(path)
            with patch.object(h, 'read_text', side_effect=unavailable):
                services, warnings = h.linux_scan(root)
            self.assertEqual(len(services), 2)
            self.assertTrue(all(s['pid'] == 0 and not s['identity'] for s in services))
            self.assertTrue(warnings)

class SignalPaths(unittest.TestCase):
    setUp = StopSafety.setUp
    def test_pidfd_signal_only_after_matching_owner(self):
        with patch.object(h, 'scan', return_value={'services': [self.service]}), patch.object(h, 'open_pidfd', return_value=123), patch.object(h, 'protected_pids', return_value={1}), patch.object(h.signal, 'pidfd_send_signal', create=True) as send, patch.object(h.os, 'close') as close, patch.object(h.os, 'kill') as kill:
            result = h.stop(self.target)
            self.assertTrue(result['sent'])
            send.assert_called_once_with(123, h.signal.SIGTERM, None, 0)
            kill.assert_not_called()
            close.assert_called_once_with(123)

    def test_pidfd_identity_failure_never_signals(self):
        with patch.object(h, 'scan', return_value={'services': [dict(self.service, identity='different')]}), patch.object(h, 'open_pidfd', return_value=123), patch.object(h, 'protected_pids', return_value={1}), patch.object(h.signal, 'pidfd_send_signal', create=True) as send, patch.object(h.os, 'close'), patch.object(h.os, 'kill') as kill:
            self.assertFalse(h.stop(self.target)['sent'])
            send.assert_not_called()
            kill.assert_not_called()

    def test_pidfd_unavailable_only_falls_back_for_unsupported(self):
        with patch.object(h.sys, 'platform', 'linux'), patch.object(h.signal, 'pidfd_send_signal', create=True), patch.object(h.os, 'pidfd_open', create=True, side_effect=OSError(38, 'unsupported')):
            self.assertIsNone(h.open_pidfd(321))
        with patch.object(h.sys, 'platform', 'linux'), patch.object(h.signal, 'pidfd_send_signal', create=True), patch.object(h.os, 'pidfd_open', create=True, side_effect=PermissionError(1, 'denied')):
            with self.assertRaises(PermissionError):
                h.open_pidfd(321)


class ProjectNames(unittest.TestCase):
    def test_package_custom_and_nested_project_names(self):
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as root:
            project = Path(root) / 'website'
            child = project / 'src'
            child.mkdir(parents=True)
            (project / 'package.json').write_text('{"name":"@studio/storefront"}')
            found = h.project_info(str(child))
            self.assertEqual(found['project'], '@studio/storefront')
            self.assertEqual(found['project_path'], str(project))
            (project / '.lhwatch.json').write_text('{"name":"My Store Website"}')
            self.assertEqual(h.project_info(str(child))['project'], 'My Store Website')
            dependency = project / 'node_modules' / 'vite'
            dependency.mkdir(parents=True)
            (dependency / 'package.json').write_text('{"name":"vite"}')
            self.assertEqual(h.project_info(str(dependency))['project'], 'My Store Website')

    def test_project_markers_and_unidentified_sources(self):
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as root:
            project = Path(root) / 'catalog-api'
            project.mkdir()
            (project / 'pyproject.toml').write_text('[project]\nname="not-parsed"\n')
            found = h.project_info(str(project))
            self.assertEqual(found['project'], 'catalog-api')
            self.assertIn('pyproject.toml', found['project_source'])
            (project / 'pyproject.toml').unlink()
            self.assertEqual(h.project_info(str(project))['project_source'], 'Working directory (inferred)')
        self.assertEqual(h.project_info('/')['project'], '')
        self.assertEqual(h.project_info('')['project'], '')
        self.assertEqual(h.project_info(os.path.expanduser('~'))['project'], '')
        self.assertEqual(h.project_info('/', executable='/Applications/My App.app/Contents/MacOS/helper')['project'], 'My App')

    def test_invalid_and_special_metadata_do_not_break_discovery(self):
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as root:
            project = Path(root) / 'test-app'
            project.mkdir()
            package = project / 'package.json'
            for content in ['null', '[]', '{broken', '{"name":12}', '{"name":""}', '{"name":"' + 'x'*121 + '"}', ' '*140000]:
                package.write_text(content)
                self.assertEqual(h.project_info(str(project))['project_source'], 'Working directory (inferred)')
            package.unlink()
            os.mkfifo(package)
            self.assertEqual(h.project_info(str(project))['project'], 'test-app')


class ProjectNameRegressions(unittest.TestCase):
    def listeners(self, project, healthy):
        return [dict(pid=999991 + index, identity='boot:%d' % index,
                     address='127.0.0.1', port=54321 + index, family='IPv4',
                     uid=os.getuid(), cwd=str(directory), command='python server.py')
                for index, directory in enumerate((project, healthy))]

    def assert_metadata_scan_and_stop(self, project, healthy):
        listeners = self.listeners(project, healthy)
        with patch.object(h.sys, 'platform', 'darwin'), patch.object(h, 'mac_scan', side_effect=lambda: ([dict(service) for service in listeners], [])), patch.object(h, 'protected_pids', return_value={0, 1}), patch.object(h, 'open_pidfd', return_value=None), patch.object(h, 'docker_endpoint', return_value=('', '')), patch.object(h.os, 'kill') as kill:
            snapshot = h.scan(include_docker=False)
            self.assertEqual([service['port'] for service in snapshot['services']], [54321, 54322])
            self.assertIn(snapshot['services'][0]['project'], {'Nested Project', project.name})
            self.assertEqual(snapshot['services'][1]['project'], 'Healthy Project')
            self.assertTrue(all(service['can_stop'] for service in snapshot['services']))
            # Metadata failure must neither prevent valid revalidation nor bypass it.
            self.assertFalse(h.stop(dict(listeners[0], identity='reused-pid'))['sent'])
            kill.assert_not_called()
            self.assertTrue(h.stop(listeners[0])['sent'])
            kill.assert_called_once_with(listeners[0]['pid'], h.signal.SIGTERM)

    def test_metadata_recursion_error_keeps_other_listeners_and_stop_safety(self):
        with tempfile.TemporaryDirectory() as root:
            project, healthy = Path(root) / 'nested-project', Path(root) / 'healthy-project'
            project.mkdir()
            healthy.mkdir()
            (project / 'package.json').write_text('{"name":"Nested Project"}')
            (healthy / 'package.json').write_text('{"name":"Healthy Project"}')
            loads = h.json.loads

            def decode(data):
                if b'Nested Project' in data:
                    raise RecursionError('maximum recursion depth exceeded while decoding JSON')
                return loads(data)

            with patch.object(h.json, 'loads', side_effect=decode):
                self.assert_metadata_scan_and_stop(project, healthy)

    def test_actual_deep_metadata_keeps_other_listeners_and_stop_safety(self):
        with tempfile.TemporaryDirectory() as root:
            project, healthy = Path(root) / 'nested-project', Path(root) / 'healthy-project'
            project.mkdir()
            healthy.mkdir()
            # Python 3.14 can decode this depth; older supported decoders may reject it.
            # Both a valid name and a safe inferred fallback preserve discovery.
            content = '{"name":"Nested Project","extra":' + '[' * 10000 + '0' + ']' * 10000 + '}'
            (project / 'package.json').write_text(content)
            (healthy / 'package.json').write_text('{"name":"Healthy Project"}')
            self.assert_metadata_scan_and_stop(project, healthy)

    def test_dependency_fallback_uses_project_directory_without_markers(self):
        with tempfile.TemporaryDirectory() as root:
            project = Path(root) / 'website'
            for suffix in ('node_modules/vite', '.venv/lib/python3.14/site-packages',
                           'venv/bin', '.venv/lib/node_modules/vite',
                           'node_modules/tool/venv/bin', 'node_modules', '.venv', 'venv'):
                with self.subTest(suffix=suffix):
                    dependency = project / suffix
                    dependency.mkdir(parents=True, exist_ok=True)
                    found = h.project_info(str(dependency))
                    self.assertEqual(found['project'], 'website')
                    self.assertEqual(found['project_path'], str(project))
                    self.assertEqual(found['project_source'], 'Working directory (inferred)')

    def test_app_bundle_names_come_from_executable_with_spaces(self):
        for executable in ('/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
                           '/Applications/My Tools/My App.app/Contents/MacOS/My App'):
            with self.subTest(executable=executable):
                found = h.project_info('/', executable + ' --serve', executable=executable)
                self.assertEqual(found['project'], 'My App' if '/My App.app/' in executable else 'Google Chrome')
                self.assertEqual(found['project_source'], 'Application bundle (executable)')

    def test_app_bundle_arguments_do_not_name_python_or_jupyter(self):
        executable = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
        for command in ('python -m jupyter lab --browser=' + executable,
                        'python -m jupyter lab --browser="' + executable + '"',
                        "python -m jupyter lab --browser='" + executable + "'",
                        '/usr/local/bin/jupyter lab --browser ' + executable,
                        'python server.py "' + executable + '"',
                        'python /Applications/My App.app/Contents/Resources/server.py'):
            with self.subTest(command=command):
                self.assertEqual(h.project_info('/', command),
                                 dict(project='', project_path='', project_source=''))
                self.assertEqual(h.project_info('/', command, executable='/usr/local/bin/python'),
                                 dict(project='', project_path='', project_source=''))

    def test_scan_uses_executable_evidence_and_hides_private_field(self):
        executable = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
        base = dict(address='127.0.0.1', family='IPv4', uid=os.getuid(), cwd='/')
        listeners = [dict(base, pid=999991, port=54321, identity='boot:app',
                          command=executable + ' --serve', _executable=executable),
                     dict(base, pid=999992, port=54322, identity='boot:python',
                          command='python -m jupyter --browser=' + executable,
                          _executable='/usr/local/bin/python'),
                     dict(base, pid=999993, port=54323, identity='boot:unknown',
                          command=executable + ' --serve')]
        with patch.object(h.sys, 'platform', 'darwin'), patch.object(h, 'mac_scan', return_value=(listeners, [])), patch.object(h, 'protected_pids', return_value={0, 1}):
            services = h.scan(include_docker=False)['services']
        self.assertEqual([service['project'] for service in services], ['Google Chrome', '', ''])
        self.assertEqual([service['identity'] for service in services], ['boot:app', 'boot:python', 'boot:unknown'])
        self.assertTrue(all('_executable' not in service for service in services))

    def test_shared_prefix_markers_and_names_do_not_label_descendants(self):
        with tempfile.TemporaryDirectory() as root:
            shared = Path(root) / 'shared-prefix'
            child = shared / 'var' / 'postgresql@16'
            child.mkdir(parents=True)
            for filename, content in (('.git', ''), ('package.json', '{"name":"Shared Package"}'),
                                      ('.lhwatch.json', '{"name":"Shared Custom Name"}')):
                with self.subTest(filename=filename), patch.object(h, 'PROJECT_BOUNDARIES', {str(shared)}, create=True):
                    marker = shared / filename
                    marker.write_text(content)
                    try:
                        found = h.project_info(str(child))
                        self.assertEqual(found['project'], 'postgresql@16')
                        self.assertEqual(found['project_path'], str(child))
                        self.assertEqual(found['project_source'], 'Working directory (inferred)')
                        self.assertEqual(h.project_info(str(shared))['project'], '')
                    finally:
                        marker.unlink()

    def test_shared_prefix_stops_search_before_metadata_above_it(self):
        with tempfile.TemporaryDirectory() as root:
            outer = Path(root)
            shared = outer / 'shared-prefix'
            child = shared / 'var' / 'service-data'
            child.mkdir(parents=True)
            (outer / '.lhwatch.json').write_text('{"name":"Unrelated Parent"}')
            with patch.object(h, 'PROJECT_BOUNDARIES', {str(shared)}, create=True):
                found = h.project_info(str(child))
            self.assertEqual(found['project'], 'service-data')
            self.assertEqual(found['project_path'], str(child))

    def test_project_markers_and_custom_names_below_shared_prefix_are_allowed(self):
        with tempfile.TemporaryDirectory() as root:
            shared = Path(root) / 'shared-prefix'
            project = shared / 'projects' / 'catalog-api'
            child = project / 'src'
            child.mkdir(parents=True)
            (shared / '.lhwatch.json').write_text('{"name":"Shared Custom Name"}')
            for filename, content, expected in (('.git', '', 'catalog-api'),
                                                ('package.json', '{"name":"Catalog Package"}', 'Catalog Package'),
                                                ('.lhwatch.json', '{"name":"Catalog API"}', 'Catalog API')):
                with self.subTest(filename=filename), patch.object(h, 'PROJECT_BOUNDARIES', {str(shared)}, create=True):
                    marker = project / filename
                    marker.write_text(content)
                    try:
                        found = h.project_info(str(child))
                        self.assertEqual(found['project'], expected)
                        self.assertEqual(found['project_path'], str(project))
                    finally:
                        marker.unlink()

if __name__ == '__main__':
    unittest.main()
