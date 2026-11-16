"""Identifiers: UUIDv7 for chats, runs and confirmations (ADR-0004).

Python 3.12 and 3.13 have no ``uuid.uuid7``, so this builds one from RFC 9562
section 5.7: 48 bits of Unix milliseconds, then version and variant bits,
then random bits. Time comes first so IDs sort by creation, which keeps
Postgres B-tree inserts local when runs are stored there later.
"""

from __future__ import annotations

import os
import time
import uuid


def uuid7(now_ms: int | None = None, rand: bytes | None = None) -> str:
    """Return a UUIDv7 string. Both arguments exist for deterministic tests."""
    ms = int(time.time() * 1000) if now_ms is None else now_ms
    r = os.urandom(10) if rand is None else rand
    if len(r) != 10:
        raise ValueError("uuid7 needs 10 random bytes")
    value = (ms & ((1 << 48) - 1)) << 80
    value |= 0x7 << 76  # version
    value |= (int.from_bytes(r[0:2], "big") & 0x0FFF) << 64  # rand_a, 12 bits
    value |= 0b10 << 62  # variant
    value |= int.from_bytes(r[2:10], "big") & ((1 << 62) - 1)  # rand_b, 62 bits
    return str(uuid.UUID(int=value))


def random_replica_id() -> int:
    """Return a random non-zero 64-bit CRDT replica ID (0 is the root)."""
    while True:
        rid = int.from_bytes(os.urandom(8), "big")
        if rid != 0:
            return rid
