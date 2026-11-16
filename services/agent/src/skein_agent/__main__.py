"""``python -m skein_agent``: run the agent gRPC service."""

from __future__ import annotations

import asyncio
import sys


def main() -> int:
    """Entry point: configure logging and telemetry, then serve."""
    from .adapters.logging import configure_logging
    from .adapters.telemetry import configure_telemetry
    from .adapters.wiring import serve
    from .config import ConfigError, load_settings

    try:
        settings = load_settings()
    except ConfigError as err:
        print(f"configuration error: {err}", file=sys.stderr)
        return 2
    configure_logging(settings.log_level)
    shutdown_telemetry = configure_telemetry()
    try:
        asyncio.run(serve(settings))
    finally:
        shutdown_telemetry()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
