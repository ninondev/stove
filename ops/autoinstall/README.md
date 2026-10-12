# One-USB demo install

These scripts make an unattended Ubuntu Desktop 24.04 amd64 installer for the
Stove demo service. They do not install a full ordering system. The demo uses
Python's standard library. It needs no Python package download or virtual environment.

**Warning: booting the new ISO erases the selected target disk.** Writing the ISO
to a USB drive also erases that USB drive. Back up both before use. Check the disk
path on the target machine. A path from the build machine may refer to another disk.

## Build the ISO

Start with an original Ubuntu Desktop 24.04 amd64 ISO. Get its SHA256 from a
trusted source and check it yourself. The scripts do not download an ISO.
Install `xorriso`, `git`, and Python 3 on the build machine first.

Commit the public Stove files before building. The payload uses that public git
commit, even if the working tree later changes. It includes only the Python app and its SQLite schema,
the ticket preview helper, the made-up menu, first-boot files, and the license. It has no `.deb` files.

Set these values in your shell. The disk below is an example, not a safe default.

```sh
export STOVE_ISO_SHA256='<the checked ISO SHA256>'
export STOVE_TARGET_DISK='/dev/vda'
export STOVE_CONFIRM_ERASE='ERASE:/dev/vda'
nice -n 10 bash ops/autoinstall/make_usb_iso.sh ubuntu-24.04-desktop-amd64.iso stove-demo.iso
```

The default admin account is locked. If you need local login, set
`STOVE_PASSWORD_HASH` to a sha512-crypt or yescrypt password hash before building.
Use a trusted local password tool. The helper takes a hash, not a plain password.
Treat the built ISO as private if it contains your password hash. Do not commit it.

The helper makes a new ISO and an adjacent `.build.json` file. It refuses to replace
an existing output. It checks a staged copy of the base ISO, archives immutable
public git objects, and puts the payload SHA256 in the install template. It checks
the remastered payload again. The installer checks this pinned hash before
extracting it. This checks consistency; it is not a digital signature.

The remaster keeps the base ISO's boot records through `xorriso` boot replay. This
is an intended BIOS/UEFI path. It still needs a VM test and a test on your machine.

## Install and first boot

Write the ISO to a USB drive with your usual image tool. These scripts do not write
to a USB device. Start the target machine from that drive. The installer has a
short countdown and no extra erase prompt. It selects the exact disk path you set.
If the target disk cannot be found, installation must fail. Check the install log.

The template disables DHCP and extra downloads. It copies the checked demo payload
from the USB drive. It shuts down after installation. Remove the USB drive before
starting the machine again, or the installer may run again and erase the disk again.

First boot creates a system user named `stove`, installs `stove-demo.service`,
and runs a local health check. The app files are in `/opt/stove`. The service runs
at `http://127.0.0.1:8859/`. It is only available on the target machine.
No printer, network rules, remote access service, or desktop app is set up.

Read the result in `/var/lib/stove-firstboot/result.txt`. Read the log at
`/var/log/stove-firstboot.log`, or use `journalctl -u stove-firstboot`.
A failed first boot keeps its pending marker. After fixing the error, run
`sudo systemctl start stove-firstboot`. A successful run removes the marker.

## Offline VM smoke check

Install QEMU on the build machine first. Use a new work directory and `/dev/vda`
as the installer disk. The VM has no network adapter. It uses a disposable image,
not a physical drive. Each phase may take a long time under software emulation.
Run one phase at a time:

```sh
nice -n 10 bash ops/autoinstall/vm_smoke.sh install stove-demo.iso vm-test
nice -n 10 bash ops/autoinstall/vm_smoke.sh boot stove-demo.iso vm-test
```

The install phase waits for QEMU to exit. That alone does not prove success.
The boot phase looks for the first-boot local health marker in the serial log and
then requests a clean guest shutdown. Keep both serial logs. The helper does not
test a physical printer, USB drive, real machine, or UEFI firmware. It uses the
QEMU default BIOS path. If the VM cannot shut down, discard its disposable image.

## Optional `.deb` downloads

The standard demo needs no extra `.deb` packages. None are stored in this repo or
added by the ISO builder. `fetch_debs.sh` is an optional preparation helper for a
future, separate system change. Run it only on Ubuntu 24.04 with network access
authorized. It uses the configured APT sources and downloads only exact package
versions you name:

```sh
bash ops/autoinstall/fetch_debs.sh local-debs package-name=exact-version
```

It writes `SHA256SUMS` beside the downloaded files. This file records the bytes you
downloaded; it does not replace APT's repository trust checks. Resolve dependencies
on a matching Ubuntu machine. This helper does not install packages and does not
add them to the Stove image.

## Scope and checks

The private installer had desktop and printer setup and upgrade checks. This kit
keeps only the unattended OS install, first boot, ISO builder, and VM smoke path.
It has no screenshot tools, guest disk injection, or upgrade failure test.
Unit tests check payload selection, fixed git inputs, template validation,
and imports, queue startup, and ticket rendering from an extracted payload.
They do not prove an Ubuntu installation works. Run the ISO and VM checks yourself
before using this installer on a machine.

The files use GPL-3.0-only. See [LICENSE](../../LICENSE).
