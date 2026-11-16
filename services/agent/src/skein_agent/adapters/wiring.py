"""Production wiring: real adapters behind the same ports the fakes implement.

``serve`` builds everything from Settings, starts the gRPC server with the
standard health service, and shuts down in order on SIGTERM or SIGINT:
mark NOT_SERVING (so the load balancer stops routing), cancel active runs
so each records RunFinished, stop the server with a grace period, then
close channels, Redis and the checkpointer, and flush telemetry.
"""

from __future__ import annotations

import asyncio
import logging
import signal
from contextlib import AsyncExitStack
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
from .anthropic_model import AnthropicModelProvider
from .checkpointer import open_checkpointer
from .grpc_clients import build_services
from .grpc_server import AgentServicer
from .memory import InMemoryChatStore, InMemoryRunStore

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
        checkpointer = await stack.enter_async_context(open_checkpointer(settings.checkpointer, settings.postgres_dsn))
        services = build_services(channels)
        executor = ToolExecutor(build_catalogue(), cache, cache_ttl_s=settings.cache_ttl_s)
        provider = model_provider(settings)
        chat_store = InMemoryChatStore()
        runs = AgentRuns(
            services=services,
            executor=executor,
            model_provider=provider,
            run_store=InMemoryRunStore(),
            chat_store=chat_store,
            rate_limiter=limiter,
            clock=clock,
            checkpointer=checkpointer,
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
        loop = asyncio.get_running_loop()
        for sig in (signal.SIGTERM, signal.SIGINT):
            loop.add_signal_handler(sig, stop.set)
        await stop.wait()

        log.info("shutting down")
        await health_servicer.enter_graceful_shutdown()
        await runs.shutdown()
        await server.stop(_SHUTDOWN_GRACE_S)
