#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-only
"""Offline QEMU checks on a new, disposable disk image."""
import argparse
import json
from pathlib import Path
import shutil
import socket
import subprocess
import time


def power_down(monitor):
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(5)
        client.connect(str(monitor))
        stream = client.makefile('rwb')
        json.loads(stream.readline())
        stream.write(b'{"execute":"qmp_capabilities"}\n')
        stream.flush()
        while 'return' not in json.loads(stream.readline()):
            pass
        stream.write(b'{"execute":"system_powerdown"}\n')
        stream.flush()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('phase', choices=('install', 'boot'))
    parser.add_argument('iso', type=Path)
    parser.add_argument('work', type=Path)
    parser.add_argument('--timeout', type=int, default=2400)
    args = parser.parse_args()
    for tool in ('qemu-system-x86_64', 'qemu-img'):
        if shutil.which(tool) is None:
            parser.error('Missing tool: ' + tool)
    if not args.iso.is_file() or not args.iso.name.endswith('.iso'):
        parser.error('Use a locally built Stove ISO')
    if args.timeout < 1:
        parser.error('Timeout must be positive')
    work = args.work.resolve()
    if ',' in str(work):
        parser.error('The work path must not contain a comma; QEMU uses it as an option separator')
    work.mkdir(parents=True, exist_ok=True)
    disk = work / 'disk.qcow2'
    if args.phase == 'install':
        if disk.exists() or disk.is_symlink():
            parser.error('The install disk must not exist. Use a new work directory.')
        subprocess.run(['qemu-img', 'create', '-f', 'qcow2', str(disk), '30G'], check=True)
    elif not disk.is_file() or disk.is_symlink():
        parser.error('Run the install phase first')
    monitor = work / 'qmp.sock'
    if monitor.exists():
        parser.error('A QMP socket already exists. Check that no VM is running.')
    log = work / (args.phase + '.serial.log')
    if log.exists():
        parser.error('The phase log already exists. Keep it and use a new work directory.')
    command = ['qemu-system-x86_64', '-m', '4096', '-smp', '2',
               '-accel', 'tcg', '-drive', f'file={disk},format=qcow2,if=virtio',
               '-nic', 'none', '-display', 'none', '-serial', 'file:' + str(log),
               '-qmp', 'unix:' + str(monitor) + ',server=on,wait=off', '-no-reboot']
    if args.phase == 'install':
        command += ['-cdrom', str(args.iso.resolve()), '-boot', 'd']
    else:
        command += ['-boot', 'c']
    process = subprocess.Popen(command)
    marker_seen = False
    deadline = time.monotonic() + args.timeout
    try:
        while process.poll() is None and time.monotonic() < deadline:
            if args.phase == 'boot' and log.exists():
                marker_seen = b'STOVE_DEMO_HEALTH_OK' in log.read_bytes()
                if marker_seen:
                    power_down(monitor)
                    process.wait(timeout=60)
                    break
            time.sleep(1)
        if process.poll() is None:
            raise SystemExit('VM timed out. Keep the serial log for review.')
        if process.returncode != 0:
            raise SystemExit('QEMU failed; inspect the serial log.')
        if args.phase == 'boot' and not marker_seen:
            raise SystemExit('No first-boot health marker; inspect the serial log.')
        print('Install VM exited; run boot next.' if args.phase == 'install'
              else 'First-boot local health check passed in the VM.')
    finally:
        if process.poll() is None:
            try:
                power_down(monitor)
                process.wait(timeout=30)
            except (OSError, ValueError, subprocess.TimeoutExpired):
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                print('VM did not shut down cleanly. Discard this disposable image.')


if __name__ == '__main__':
    main()
