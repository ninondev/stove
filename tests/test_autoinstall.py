# SPDX-License-Identifier: GPL-3.0-only
import importlib.util
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('stove_build_payload', ROOT / 'ops/autoinstall/build_payload.py')
payload = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(payload)


class TemplateTests(unittest.TestCase):
    def setUp(self):
        self.template = (ROOT / 'ops/autoinstall/autoinstall.yaml.tmpl').read_text()

    def test_uses_exact_disk_and_pinned_payload(self):
        text = payload.render_template(self.template, '/dev/vda', '!', 'a' * 64)
        self.assertIn('path: "/dev/vda"', text)
        self.assertIn('a' * 64, text)
        self.assertNotIn('{{', text)
        self.assertIn('sha256sum --check --status', text)
        self.assertIn('shutdown: poweroff', text)
        self.assertNotIn('size: largest', text)

    def test_accepts_nvme_whole_disk_and_password_hash(self):
        text = payload.render_template(self.template, '/dev/nvme0n1', '$6$salt$hash', 'b' * 64)
        self.assertIn('password: "$6$salt$hash"', text)

    def test_rejects_partitions_and_yaml_injection(self):
        for disk in ('/dev/vda1', '/dev/nvme0n1p1', '/dev/sda\nother:', 'largest'):
            with self.subTest(disk=disk), self.assertRaises(ValueError):
                payload.render_template(self.template, disk, '!', 'a' * 64)

    def test_rejects_plain_password_or_bad_hash(self):
        for password in ('plain-password', '$6$salt$hash"', '$6$salt$hash\n'):
            with self.subTest(password=password), self.assertRaises(ValueError):
                payload.render_template(self.template, '/dev/vda', password, 'a' * 64)
        with self.assertRaises(ValueError):
            payload.render_template(self.template, '/dev/vda', '!', 'not-a-hash')

    def test_firstboot_preserves_failure_for_retry(self):
        script = (ROOT / 'ops/autoinstall/firstboot.sh').read_text()
        self.assertLess(script.index("'http://127.0.0.1:8859/health'"), script.index('rm -f "$STATE/pending"'))
        self.assertIn('set -euo pipefail', script)
        self.assertIn('Restart=on-failure', (ROOT / 'ops/autoinstall/stove-demo.service').read_text())

    def test_builder_requires_erase_confirmation_and_base_hash(self):
        script = (ROOT / 'ops/autoinstall/make_usb_iso.sh').read_text()
        self.assertIn('"$STOVE_CONFIRM_ERASE" == "ERASE:$STOVE_TARGET_DISK"', script)
        self.assertIn('STOVE_ISO_SHA256', script)
        self.assertIn('Staged base ISO SHA256 mismatch', script)
        self.assertIn('Remastered payload hash mismatch', script)


class PayloadTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.repo = Path(self.directory.name) / 'repo'
        self.repo.mkdir()
        self.git('init', '-q')
        self.git('config', 'user.name', 'Test')
        self.git('config', 'user.email', '284425649+ninondev@users.noreply.github.com')
        for name in payload.REQUIRED:
            target = self.repo / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text('fixture\n')
        self.write('app/domain/queue.py', 'committed\n')
        self.write('data/private.json', '{}\n')
        self.write('extra-note.txt', 'not needed\n')
        for name in sorted(payload.REQUIRED | {'app/domain/queue.py', 'data/private.json', 'extra-note.txt'}):
            self.git('add', '--', name)
        self.git('commit', '-qm', 'Fixture')

    def git(self, *args):
        return subprocess.run(['git', '-C', str(self.repo), *args], check=True, capture_output=True)

    def write(self, name, text):
        target = self.repo / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def archive(self, filename):
        output = Path(self.directory.name) / filename
        digest, revision = payload.create_payload(self.repo, output)
        with tarfile.open(output) as archive:
            contents = {entry.name: archive.extractfile(entry).read() for entry in archive}
        return output, digest, revision, contents

    def test_uses_committed_bytes_and_excludes_other_files(self):
        self.write('app/domain/queue.py', 'working change\n')
        self.write('app/untracked.py', 'untracked\n')
        _, _, _, files = self.archive('payload.tar.gz')
        self.assertEqual(files['app/domain/queue.py'], b'committed\n')
        self.assertNotIn('app/untracked.py', files)
        self.assertNotIn('data/private.json', files)
        self.assertNotIn('extra-note.txt', files)

    def test_is_reproducible(self):
        first, digest1, revision1, _ = self.archive('first.tar.gz')
        second, digest2, revision2, _ = self.archive('second.tar.gz')
        self.assertEqual(first.read_bytes(), second.read_bytes())
        self.assertEqual(digest1, digest2)
        self.assertEqual(revision1, revision2)

    def test_rejects_symlinks_in_app(self):
        (self.repo / 'app/link.py').symlink_to('domain/queue.py')
        self.git('add', '--', 'app/link.py')
        self.git('commit', '-qm', 'Link fixture')
        with self.assertRaises(ValueError):
            self.archive('invalid.tar.gz')

    def test_missing_committed_service_fails(self):
        self.git('rm', '--', 'app/demo_service.py')
        self.git('commit', '-qm', 'Missing fixture')
        with self.assertRaises(ValueError):
            self.archive('invalid.tar.gz')

    def test_extracted_payload_runs_real_demo_without_repo_imports(self):
        sources = sorted((ROOT / 'app').rglob('*.py')) + [
            ROOT / 'app/db/schema.sql', ROOT / 'examples/__init__.py',
            ROOT / 'examples/ticket_preview.py', ROOT / 'examples/demo_menu.json',
        ]
        for source in sources:
            name = source.relative_to(ROOT).as_posix()
            self.write(name, source.read_text())
            self.git('add', '--', name)
        self.git('commit', '-qm', 'Real public demo fixture')
        _, _, _, files = self.archive('real-demo.tar.gz')
        extracted = Path(self.directory.name) / 'extracted'
        extracted.mkdir()
        for name, data in files.items():
            target = extracted / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        # Isolated Python cannot accidentally import the development checkout.
        # No HTTP socket is opened: this exercises startup dependencies directly.
        program = """
import sys
sys.path.insert(0, sys.argv[1])
from app.demo_service import make_handler
from app.db.connection import Database
from app.services import queue
from examples.ticket_preview import sample_payload, text_preview
db = Database(':memory:')
db.init_schema()
assert make_handler(db).__name__ == 'Handler'
ticket = queue.take_number(db, size_group='small', party_size=2)
assert ticket['number'] == 1
preview = text_preview(sample_payload())
assert 'Stove Demo Kitchen' in preview
assert len(preview.splitlines()) > 4
db.close()
"""
        subprocess.run([sys.executable, '-I', '-c', program, str(extracted)],
                       cwd=extracted, check=True, capture_output=True)

    def test_existing_output_is_preserved(self):
        output = Path(self.directory.name) / 'existing.tar.gz'
        output.write_bytes(b'keep')
        with self.assertRaises(FileExistsError):
            payload.create_payload(self.repo, output)
        self.assertEqual(output.read_bytes(), b'keep')


if __name__ == '__main__':
    unittest.main()
