#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-only
"""Build a small archive from immutable public git objects."""
import gzip
import hashlib
import io
from pathlib import Path
import re
import subprocess
import sys
import tarfile

REQUIRED = {
    'app/demo_service.py', 'app/__init__.py', 'app/db/schema.sql',
    'examples/__init__.py', 'examples/ticket_preview.py', 'examples/demo_menu.json',
    'ops/autoinstall/firstboot.sh', 'ops/autoinstall/stove-firstboot.service',
    'ops/autoinstall/stove-demo.service', 'LICENSE',
}


def allowed(name):
    return (name in REQUIRED or
            (name.startswith('app/') and name.endswith('.py') and
             '__pycache__' not in name.split('/')))


def create_payload(repo, output):
    repo = Path(repo)
    revision = subprocess.check_output(['git', '-C', str(repo), 'rev-parse', 'HEAD'], text=True).strip()
    tree = subprocess.check_output(['git', '-C', str(repo), 'ls-tree', '-r', '-z', revision])
    files = {}
    for entry in tree.split(b'\0'):
        if not entry:
            continue
        metadata, raw_name = entry.split(b'\t', 1)
        mode, kind, object_id = metadata.decode('ascii').split()
        name = raw_name.decode('utf-8')
        if not allowed(name):
            continue
        if kind != 'blob' or mode not in ('100644', '100755'):
            raise ValueError('Payload entries must be regular files: ' + name)
        if Path(name).is_absolute() or '..' in Path(name).parts:
            raise ValueError('Unsafe archive name')
        data = subprocess.check_output(['git', '-C', str(repo), 'cat-file', 'blob', object_id])
        files[name] = (data, int(mode[-3:], 8))
    if REQUIRED - files.keys():
        raise ValueError('Committed payload is missing: ' + ', '.join(sorted(REQUIRED - files.keys())))
    with Path(output).open('xb') as raw:
        with gzip.GzipFile(filename='', fileobj=raw, mode='wb', mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode='w') as archive:
                for name, (data, mode) in sorted(files.items()):
                    info = tarfile.TarInfo(name)
                    info.size = len(data)
                    info.mode = mode
                    info.mtime = 0
                    archive.addfile(info, io.BytesIO(data))
    return hashlib.sha256(Path(output).read_bytes()).hexdigest(), revision


def render_template(template, disk, password_hash, payload_hash):
    if not re.fullmatch(r'/dev/(?:sd[a-z]+|vd[a-z]+|nvme[0-9]+n[0-9]+)', disk):
        raise ValueError('Use a whole disk path such as /dev/sda or /dev/nvme0n1')
    if password_hash != '!' and not re.fullmatch(r'\$(?:6|y)\$[A-Za-z0-9./$=,-]+', password_hash):
        raise ValueError('Use a sha512-crypt/yescrypt password hash, or ! to lock login')
    if not re.fullmatch(r'[a-f0-9]{64}', payload_hash):
        raise ValueError('Invalid payload SHA256')
    for key, value in {'TARGET_DISK': disk, 'PASSWORD_HASH': password_hash,
                       'PAYLOAD_SHA256': payload_hash}.items():
        template = template.replace('{{' + key + '}}', value)
    if '{{' in template or '\t' in template:
        raise ValueError('Unfilled template or tab indentation')
    return template


if __name__ == '__main__':
    if len(sys.argv) != 3:
        raise SystemExit('Usage: build_payload.py PUBLIC_REPO OUTPUT.tar.gz')
    digest, revision = create_payload(sys.argv[1], sys.argv[2])
    print(digest + ' ' + revision)
