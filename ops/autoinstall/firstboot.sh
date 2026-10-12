#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail
[[ "$EUID" == 0 ]] || { echo 'Run only as root on the target machine.' >&2; exit 1; }
STATE=/var/lib/stove-firstboot
mkdir -p "$STATE"
exec > >(tee -a /var/log/stove-firstboot.log) 2>&1
trap 'echo "First boot failed. The pending marker is kept for retry." >&2' ERR
command -v python3 >/dev/null
/usr/bin/python3 -c 'import http.server, json, sqlite3'
if ! getent passwd stove >/dev/null; then
    useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin stove
fi
[[ "$(getent passwd stove | cut -d: -f7)" == /usr/sbin/nologin ]] || {
    echo 'An existing stove account has a login shell; refusing to reuse it.' >&2; exit 1;
}
chown -R root:root /opt/stove
chmod -R go-w /opt/stove
install -m 0644 /opt/stove/ops/autoinstall/stove-demo.service /etc/systemd/system/stove-demo.service
systemctl daemon-reload
systemctl enable --now stove-demo.service
/usr/bin/python3 - <<'PY'
import json
import time
import urllib.request
for attempt in range(30):
    try:
        with urllib.request.urlopen('http://127.0.0.1:8859/health', timeout=1) as response:
            healthy = response.status == 200 and json.load(response).get('status') == 'ok'
        if healthy:
            break
    except (OSError, ValueError):
        pass
    time.sleep(1)
else:
    raise SystemExit('Demo service health check failed')
PY
printf 'Stove demo is ready at http://127.0.0.1:8859/\n' > "$STATE/result.txt"
date -u '+%Y-%m-%dT%H:%M:%SZ' > "$STATE/done"
rm -f "$STATE/pending"
echo 'STOVE_DEMO_HEALTH_OK'
