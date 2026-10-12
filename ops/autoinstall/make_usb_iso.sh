#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
# Creates an ISO file only. It never writes a USB device.
set -euo pipefail
if (($# != 2)); then
    echo 'Usage: make_usb_iso.sh ORIGINAL_UBUNTU_24.04_DESKTOP_AMD64.iso OUTPUT.iso' >&2
    exit 2
fi
IN="$1"; OUT="$2"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
: "${STOVE_ISO_SHA256:?Set STOVE_ISO_SHA256 to the independently checked Ubuntu ISO SHA256}"
: "${STOVE_TARGET_DISK:?Set STOVE_TARGET_DISK to the exact target disk path}"
: "${STOVE_CONFIRM_ERASE:?Set STOVE_CONFIRM_ERASE to ERASE:<target disk>}"
[[ "$STOVE_CONFIRM_ERASE" == "ERASE:$STOVE_TARGET_DISK" ]] || {
    echo 'Erase confirmation does not match the target disk.' >&2; exit 2;
}
echo "WARNING: booting this ISO will erase $STOVE_TARGET_DISK without another prompt." >&2
[[ -f "$IN" && ! -L "$IN" ]] || { echo 'Input must be a regular ISO file.' >&2; exit 2; }
[[ ! -e "$OUT" && ! -L "$OUT" && ! -e "$OUT.build.json" && ! -L "$OUT.build.json" && "$OUT" == *.iso ]] || {
    echo 'Output must be a new .iso file.' >&2; exit 2;
}
for tool in xorriso python3 git; do command -v "$tool" >/dev/null; done
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
mkdir -p "$W/stove" "$(dirname "$OUT")"
# Stage the input once and check the staged copy, not a changing source file.
cp "$IN" "$W/base.iso"
python3 - "$W/base.iso" "$STOVE_ISO_SHA256" <<'PY'
import hashlib, re, sys
if not re.fullmatch('[a-fA-F0-9]{64}', sys.argv[2]):
    raise SystemExit('Invalid ISO SHA256')
hash_value = hashlib.sha256()
with open(sys.argv[1], 'rb') as source:
    for chunk in iter(lambda: source.read(1024 * 1024), b''):
        hash_value.update(chunk)
if hash_value.hexdigest() != sys.argv[2].lower():
    raise SystemExit('Staged base ISO SHA256 mismatch')
PY
# Reject an earlier custom Stove ISO; start with the original Ubuntu image.
BASE_ROOT="$(xorriso -indev "$W/base.iso" -find / -maxdepth 1 2>/dev/null)"
if grep -Fxq "'/stove'" <<< "$BASE_ROOT"; then
    echo 'Use an original Ubuntu ISO, not an earlier Stove ISO.' >&2; exit 2
fi
xorriso -osirrox on -indev "$W/base.iso" -extract /.disk/info "$W/iso-info"
python3 - "$W/iso-info" <<'PYINFO'
import pathlib, re, sys
text = pathlib.Path(sys.argv[1]).read_text()
if not re.search(r'Ubuntu 24\.04(?:\.[0-9]+)?\b', text) or 'amd64' not in text:
    raise SystemExit('Base ISO must be Ubuntu 24.04 amd64')
PYINFO
xorriso -osirrox on -indev "$W/base.iso" -extract /casper/filesystem.manifest "$W/filesystem.manifest"
python3 - "$W/filesystem.manifest" <<'PY'
import sys
packages = {line.split()[0] for line in open(sys.argv[1]) if line.strip()}
if not {'python3', 'python3-minimal', 'systemd'} <= packages:
    raise SystemExit('Base ISO does not list the required system packages')
PY
read -r PAYLOAD_SHA PUBLIC_REV < <(python3 "$HERE/build_payload.py" "$REPO" "$W/stove/stove-src.tar.gz")
export PAYLOAD_SHA
# Read the template from the same pinned public commit as the payload.
git -C "$REPO" show "$PUBLIC_REV:ops/autoinstall/autoinstall.yaml.tmpl" > "$W/template"
python3 - "$HERE" "$W/template" "$W/autoinstall.yaml" <<'PY'
import os, pathlib, sys
sys.path.insert(0, sys.argv[1])
from build_payload import render_template
text = render_template(pathlib.Path(sys.argv[2]).read_text(),
    os.environ['STOVE_TARGET_DISK'], os.environ.get('STOVE_PASSWORD_HASH', '!'),
    os.environ['PAYLOAD_SHA'])
pathlib.Path(sys.argv[3]).write_text(text)
PY
xorriso -osirrox on -indev "$W/base.iso" -extract /boot/grub/grub.cfg "$W/grub.cfg"
chmod u+w "$W/grub.cfg"
python3 - "$W/grub.cfg" <<'PY'
from pathlib import Path
import re, sys
path = Path(sys.argv[1])
text = path.read_text()
if not re.search(r'linux\s+/casper/vmlinuz\b', text):
    raise SystemExit('Unsupported Ubuntu GRUB layout')
text = re.sub(r'(linux\s+/casper/vmlinuz)(?![^\n]*\bautoinstall\b)',
    r'\1 autoinstall console=ttyS0,115200', text)
text = re.sub(r'(?m)^set timeout=\d+', 'set timeout=5', text)
path.write_text(text)
PY
xorriso -indev "$W/base.iso" -outdev "$W/output.iso" -boot_image any replay \
    -map "$W/autoinstall.yaml" /autoinstall.yaml \
    -map "$W/stove" /stove -map "$W/grub.cfg" /boot/grub/grub.cfg -end
# Check the actual remastered payload and template before delivering the ISO.
xorriso -osirrox on -indev "$W/output.iso" -extract /stove/stove-src.tar.gz "$W/check.tar.gz"
xorriso -osirrox on -indev "$W/output.iso" -extract /autoinstall.yaml "$W/check.yaml"
python3 - "$W/check.tar.gz" "$W/check.yaml" "$W/autoinstall.yaml" "$PAYLOAD_SHA" <<'PY'
import hashlib, pathlib, sys
if hashlib.sha256(pathlib.Path(sys.argv[1]).read_bytes()).hexdigest() != sys.argv[4]:
    raise SystemExit('Remastered payload hash mismatch')
if pathlib.Path(sys.argv[2]).read_bytes() != pathlib.Path(sys.argv[3]).read_bytes():
    raise SystemExit('Remastered template mismatch')
PY
# Exclusive creation prevents a later race from overwriting an existing file.
python3 - "$W/output.iso" "$OUT" "$PUBLIC_REV" "$PAYLOAD_SHA" <<'PY'
import hashlib, json, os, pathlib, shutil, sys
source, target, revision, payload = sys.argv[1:]
with open(source, 'rb') as src, open(target, 'xb') as dst:
    shutil.copyfileobj(src, dst)
h = hashlib.sha256()
with open(target, 'rb') as src:
    for chunk in iter(lambda: src.read(1024 * 1024), b''):
        h.update(chunk)
with open(target + '.build.json', 'x') as metadata:
    json.dump({'public_commit': revision, 'payload_sha256': payload,
               'base_iso_sha256': os.environ['STOVE_ISO_SHA256'].lower(),
               'iso_sha256': h.hexdigest()}, metadata, indent=2)
    metadata.write('\n')
PY
echo "Created $OUT. Shut down after install, remove the USB drive, then boot the target disk."
