# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Defaults for a 48-column demo kitchen; times are shown in UTC."""
from datetime import timezone

STORE_NAME = 'Stove Demo Kitchen'
TZ = timezone.utc
JIAO_PER_YUAN = 10
PRINT_COLUMNS = 48
PRINT_ENCODING = 'gb18030'
PRINTER_CONNECT_TIMEOUT_S = 4.0
PRINTER_WRITE_TIMEOUT_S = 8.0
RETRY_BACKOFF_S = (1, 2, 5, 15, 30)
RETRY_BACKOFF_CAP_S = 60
RETRY_MAX_ATTEMPTS = 12
