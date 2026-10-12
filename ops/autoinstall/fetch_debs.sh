#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
# Optional preparation on Ubuntu 24.04, with network access explicitly allowed.
# The demo needs no extra packages on the standard Desktop ISO.
set -euo pipefail
if (($# < 2)); then
    echo 'Usage: fetch_debs.sh OUTPUT_DIR PACKAGE=VERSION [PACKAGE=VERSION ...]' >&2
    exit 2
fi
OUT="$1"; shift
for package in "$@"; do
    [[ "$package" =~ ^[a-z0-9][a-z0-9+.-]*=[a-zA-Z0-9:.+~_-]+$ ]] || {
        echo 'Use an exact package version.' >&2; exit 2;
    }
done
command -v apt-get >/dev/null
mkdir -p "$OUT"
cd "$OUT"
apt-get download "$@"
sha256sum -- *.deb > SHA256SUMS
printf 'Downloaded only the named packages. Resolve dependencies on a matching Ubuntu machine.\n'
