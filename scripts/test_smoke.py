import unittest
from unittest.mock import patch
from docker_smoke import container_stop_refused
from smoke import await_condition
from smoke import confirmation_matches, selected_service_matches, selected_port_matches, service_details_match


class ProjectFilter(unittest.TestCase):
    def setUp(self):
        self.record = {'pid': 1234, 'port': 56789}
        self.project = 'Smoke Website'
        self.source = 'Custom name (.lhwatch.json)'

    def test_search_query_and_preview_do_not_count_as_a_fixture_row(self):
        screen = """  /  Smoke Website
│ local │ PROJECT / SERVICE │ PROJECT DETAILS │
│       │ No matches        │ Smoke Website   │
"""
        self.assertFalse(selected_service_matches(screen, 'local', self.record, self.project))

    def test_selected_row_requires_exact_project_port_and_host(self):
        screen = "│ local │ › Smoke Website       56789    local │ PROJECT / SERVICE │"
        self.assertTrue(selected_service_matches(screen, 'local', self.record, self.project))
        for original, replacement in [('Smoke Website', 'Smoke Website Copy'),
                                      ('56789', '156789'), ('    local', '    other-host'),
                                      ('› ', '  ')]:
            with self.subTest(replacement=replacement):
                self.assertFalse(selected_service_matches(screen.replace(original, replacement),
                                                          'local', self.record, self.project))

    def test_selected_port_requires_visible_row_host_and_whole_port(self):
        screen = '│ local │ › Smoke Website      56789      local │'
        self.assertTrue(selected_port_matches(screen, 'local', self.record))
        for bad in ('/ 56789', screen.replace('56789', '156789'),
                    screen.replace('      local', '      remote'), screen.replace('› ', '  ')):
            self.assertFalse(selected_port_matches(bad, 'local', self.record))

    def test_details_require_exact_fixture_identity_and_name_source(self):
        screen = """╭ DETAILS ╮
│ PROJECT / SERVICE │
│ Smoke Website │
│ 127.0.0.1:56789 │
│ PROCESS / PID │
│ python / 1234 │
│ HOST │
│ local │
│ NAME SOURCE │
│ Custom name (.lhwatch.json) │
"""
        self.assertTrue(service_details_match(screen, 'local', self.record, self.project, self.source))
        self.assertTrue(service_details_match(screen.replace('127.0.0.1:56789', '127.0.0.1:56789 IPv4'),
                                              'local', self.record, self.project, self.source))
        for label in ('Fixture API', 'Purpose unknown'):
            labeled = screen.replace('127.0.0.1:56789', label + '    127.0.0.1:56789 IPv4')
            self.assertTrue(service_details_match(labeled, 'local', self.record, self.project, self.source))
            self.assertFalse(service_details_match(labeled.replace(':56789', ':156789'),
                                                  'local', self.record, self.project, self.source))
        for original, replacement in [('Smoke Website', 'Smoke Website Copy'),
                                      ('python / 1234', 'python / 91234'),
                                      ('127.0.0.1:56789', '127.0.0.1:5678'),
                                      ('│ local │', '│ other-host │'),
                                      (self.source, 'Working directory (inferred)')]:
            with self.subTest(replacement=replacement):
                self.assertFalse(service_details_match(screen.replace(original, replacement),
                                                       'local', self.record, self.project, self.source))


class ConfirmationTarget(unittest.TestCase):
    def test_only_exact_fixture_target_can_be_confirmed(self):
        screen = """CONFIRM PROCESS STOP
│ HOST │
│ local │
│ PROCESS / PID │
│ python / 1234 │
│ COMMAND │
│ python --port 56789 │
│ ALL KNOWN ENDPOINTS AFFECTED │
│ 127.0.0.1:56789  IPv4 │
"""
        record = {'pid': 1234, 'port': 56789}
        self.assertTrue(confirmation_matches(screen, 'local', record))
        self.assertFalse(confirmation_matches(screen, 'other-host', record))
        self.assertFalse(confirmation_matches(screen.replace('python / 1234', 'python / 91234'), 'local', record))
        self.assertFalse(confirmation_matches(screen.replace('127.0.0.1:56789', '127.0.0.1:5678'), 'local', record))
        self.assertFalse(confirmation_matches(screen.replace('CONFIRM PROCESS STOP', 'LISTENING'), 'local', record))


class DockerStopNotice(unittest.TestCase):
    def test_waits_for_refusal_after_keypress(self):
        before = 'Docker · 3 endpoints\nUse Docker to stop this container.'
        notice = 'Stop unavailable: Use Docker to stop this container.'
        screens = iter([before, notice])
        seen = []
        def capture():
            screen = next(screens)
            seen.append(screen)
            return container_stop_refused(screen)
        with patch('smoke.time.sleep'):
            self.assertTrue(await_condition(capture))
        self.assertEqual(len(seen), 2)
        self.assertFalse(container_stop_refused(notice + '\nCONFIRM PROCESS STOP'))

if __name__ == '__main__':
    unittest.main()
