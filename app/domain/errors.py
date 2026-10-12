# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Errors that can be shown by an application using the kit."""


class KitError(Exception):
    status = 400

    def __init__(self, message: str):
        super().__init__(message)
        self.message = message


class ValidationError(KitError):
    status = 400


class ConflictError(KitError):
    status = 409


class NotFoundError(KitError):
    status = 404
