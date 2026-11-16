"""Production wiring: real adapters behind the same ports the fakes implement.

``serve`` builds everything from Settings, starts the gRPC server with the
standard health service, and shuts down in order on SIGTERM or SIGINT:
mark NOT_SERVING and ``/readyz`` 503 (so the load balancer stops routing),
let go of paused
runs (they stay paused in storage, for any replica to resume) and cancel
the working ones so each records RunFinished, stop the server with a grace
period, then close channels, Redis and storage, and flush telemetry.

A sweeper task ends expired pauses and runs abandoned by a stopped process
every ``SKEIN_AGENT_SWEEP_SECONDS``; every replica runs it, and the
RunStore's claims make that safe.
"""

from __future__ import annotations

import asyncio
import logging
import signal
from contextlib import AsyncExitStack, suppress
from typing import Any

import grpc
from grpc_health.v1 import health, health_pb2, health_pb2_grpc

from .. import pb
from ..app.chats import Chats
from ..app.ports import Cache, ModelProvider, RateLimiter
from ..app.runs import AgentRuns, RunSettings
from ..app.tools.catalogue import build_catalogue
from ..app.tools.executor import ToolExecutor
from ..config import Settings
from ..fakes.infra import InMemoryCache, InMemoryRateLimiter, SystemClock
from ..fakes.model import offline_provider
from .admin import start_admin
from .anthropic_model import AnthropicModelProvider
from .grpc_clients import build_services
from .grpc_server import AgentServicer
from .storage import open_storage

log = logging.getLogger(__name__)
SERVICE_NAME = "skein.v1.AgentService"
_SHUTDOWN_GRACE_S = 10.0


def model_provider(settings: Settings) -> ModelProvider:
    """The configured model backend."""
    if settings.model_backend == "scripted":
        return offline_provider()
    return AnthropicModelProvider(
        api_key=settings.anthropic_api_key, model=settings.model, max_tokens=settings.max_output_tokens
    )


async def serve(settings: Settings) -> None:
    """Run the service until a termination signal."""
    clock = SystemClock()
    async with AsyncExitStack() as stack:
        channels = {}
        for name, addr in settings.service_addrs.items():
            channel = grpc.aio.insecure_channel(addr, options=[("grpc.lb_policy_name", "round_robin")])
            stack.push_async_callback(channel.close)
            channels[name] = channel
        cache: Cache
        limiter: RateLimiter
        if settings.redis_url:
            import redis.asyncio as redis

            from .redis_store import RedisCache, RedisRateLimiter

            client: Any = redis.from_url(settings.redis_url)
            stack.push_async_callback(client.aclose)
            cache, limiter = RedisCache(client), RedisRateLimiter(client, settings.rate_limit, clock)
        else:
            log.warning("SKEIN_AGENT_REDIS_URL not set; cache and rate limits are per process")
            cache, limiter = InMemoryCache(clock), InMemoryRateLimiter(settings.rate_limit, clock)
        storage = await stack.enter_async_context(open_storage(settings.storage, settings.postgres_dsn))
        if not storage.durable:
            log.warning("SKEIN_AGENT_STORAGE=memory; chats, traces and paused runs do not survive a restart")
        services = build_services(channels)
        executor = ToolExecutor(build_catalogue(), cache, cache_ttl_s=settings.cache_ttl_s)
        provider = model_provider(settings)
        chat_store = storage.chats
        runs = AgentRuns(
            services=services,
            executor=executor,
            model_provider=provider,
            run_store=storage.runs,
            chat_store=chat_store,
            rate_limiter=limiter,
            clock=clock,
            checkpointer=storage.checkpointer,
            settings=RunSettings(
                budget=settings.budget, pricing=settings.pricing, confirmation_timeout_s=settings.confirmation_timeout_s
            ),
        )
        chats = Chats(chat_store, services.identity, clock)

        server = grpc.aio.server()
        pb.agent_pb2_grpc.add_AgentServiceServicer_to_server(AgentServicer(runs, chats), server)
        health_servicer = health.aio.HealthServicer()
        health_pb2_grpc.add_HealthServicer_to_server(health_servicer, server)
        port = server.add_insecure_port(settings.addr)
        await server.start()
        await health_servicer.set(SERVICE_NAME, health_pb2.HealthCheckResponse.SERVING)
        await health_servicer.set("", health_pb2.HealthCheckResponse.SERVING)
        log.info("agent service listening", extra={"port": port, "model": provider.model_name})

        stop = asyncio.Event()
        if settings.admin_addr:
            admin = await start_admin(settings.admin_addr, lambda: not stop.is_set())
            stack.push_async_callback(admin.wait_closed)
            stack.callback(admin.close)
        loop = asyncio.get_running_loop()
        for sig in (signal.SIGTERM, signal.SIGINT):
            loop.add_signal_handler(sig, stop.set)
        sweeper = asyncio.create_task(_sweep(runs, settings.sweep_interval_s, stop), name="agent-sweeper")
        await stop.wait()

        log.info("shutting down")
        await health_servicer.enter_graceful_shutdown()
        await sweeper
        await runs.shutdown()
        await server.stop(_SHUTDOWN_GRACE_S)


async def _sweep(runs: AgentRuns, interval_s: float, stop: asyncio.Event) -> None:
    """End expired pauses and abandoned runs until ``stop`` is set. A failed
    sweep is logged and retried next interval; it never stops the service."""
    while not stop.is_set():
        try:
            ended = await runs.expire_stale()
            if ended:
                log.info("ended stale runs", extra={"count": ended})
        except Exception as err:
            log.warning("sweep failed", extra={"error_type": type(err).__name__})
        with suppress(TimeoutError):
            await asyncio.wait_for(stop.wait(), interval_s)
