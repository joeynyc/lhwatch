import json
from pathlib import Path
import subprocess
import sys
import unittest

from hardware_smoke import REMOTE_LISTENER, fixture_command, ssh_command


class SSHArguments(unittest.TestCase):
    def test_existing_identity_port_and_trust_are_preserved(self):
        argv = ssh_command({'ssh': 'existing-alias', 'port': 2200,
                            'identity_file': '/tmp/key with spaces'}, 'python3 --version')
        for option in ('BatchMode=yes', 'StrictHostKeyChecking=yes',
                       'ClearAllForwardings=yes', 'PermitLocalCommand=no'):
            self.assertIn(option, argv)
        self.assertEqual(argv[argv.index('-i') + 1], '/tmp/key with spaces')
        self.assertEqual(argv[argv.index('-p') + 1], '2200')
        self.assertEqual(argv[-3:], ['--', 'existing-alias', 'python3 --version'])


@unittest.skipUnless(sys.platform.startswith('linux'), 'Fixture requires Linux procfs')
class FixtureCleanup(unittest.TestCase):
    def check_cleanup(self, terminate, script_path=False):
        argv = (fixture_command('lhwatch-cleanup-test', 'fallback', sys.executable)
                if script_path else [sys.executable, '-u', '-c', REMOTE_LISTENER,
                                     'lhwatch-cleanup-test', 'fallback'])
        process = subprocess.Popen(argv,
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, text=True)
        try:
            record = json.loads(process.stdout.readline())
            directory = Path(record['path'])
            self.assertTrue(directory.is_dir())
            self.assertGreater((directory / '.lhwatch.json').stat().st_size, 20000)
            if script_path:
                command = Path('/proc/%s/cmdline' % record['pid']).read_bytes().split(b'\0')
                self.assertIn(str(directory / 'fixture.py').encode(), command)
                self.assertNotIn(b'-c', command)
                self.assertLess(len(b' '.join(command)), 300)
            if terminate:
                process.terminate()  # Only the local subprocess created above.
            else:
                process.stdin.close()
            self.assertEqual(process.wait(timeout=5), 143 if terminate else 0)
            self.assertEqual(json.loads(process.stdout.readline()),
                             {'event': 'cleaned', 'signal': 15 if terminate else 0})
            self.assertFalse(directory.exists())
        finally:
            if process.poll() is None:
                process.stdin.close()
                process.wait(timeout=5)
            for pipe in (process.stdin, process.stdout, process.stderr):
                pipe.close()

    def test_stdin_eof_removes_listener_and_temporary_project(self):
        self.check_cleanup(terminate=False)

    def test_sigterm_removes_listener_and_temporary_project(self):
        self.check_cleanup(terminate=True)

    def test_script_path_keeps_command_short_and_cleans_owned_directory(self):
        self.check_cleanup(terminate=True, script_path=True)


if __name__ == '__main__':
    unittest.main()
