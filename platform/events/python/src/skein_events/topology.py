"""Topics and subscriptions, mirrored from ``deploy/compose/pubsub-init.sh``.

The script is the source of truth (it provisions the emulator, and
Terraform follows it). `default_topology` mirrors it for the backends that
provision themselves (Redis creates its consumer groups at startup), and
``tests/test_topology.py`` fails on any difference, as Go's
``TestDefaultTopologyMatchesPubSubInit`` does for its copy.
"""

from __future__ import annotations

from dataclasses import dataclass, replace

__all__ = [
    "DEAD_LETTER_SUFFIX",
    "SubscriptionSpec",
    "Topology",
    "TopologyBuilder",
    "default_topology",
]

DEAD_LETTER_SUFFIX = ".dead-letter"


@dataclass(frozen=True)
class SubscriptionSpec:
    """A consumer's own queue on a topic, with its redelivery policy."""

    name: str
    topic: str
    dead_letter_topic: str = ""
    """Receives a message after ``max_delivery_attempts`` failures; empty retries forever."""
    max_delivery_attempts: int = 10
    min_backoff: float = 1.0
    max_backoff: float = 60.0
    ack_deadline: float = 30.0
    """Seconds a delivery may go unacknowledged before another consumer may take it."""

    def backoff(self, attempt: int) -> float:
        """Seconds to wait after failed attempt ``attempt`` (from 1): doubling, capped."""
        delay = self.min_backoff
        for _ in range(1, max(attempt, 1)):
            if delay >= self.max_backoff:
                break
            delay *= 2
        return min(delay, self.max_backoff)


@dataclass(frozen=True)
class Topology:
    """Every topic and subscription."""

    topics: tuple[str, ...]
    subscriptions: tuple[SubscriptionSpec, ...]

    def subscription(self, name: str) -> SubscriptionSpec | None:
        """The named subscription, or None."""
        return next((s for s in self.subscriptions if s.name == name), None)

    def validate(self) -> None:
        """Raise ValueError for duplicates, dangling names or an unusable policy."""
        if len(set(self.topics)) != len(self.topics) or "" in self.topics:
            raise ValueError("topology: empty or duplicate topic")
        names = [s.name for s in self.subscriptions]
        if len(set(names)) != len(names):
            raise ValueError("topology: duplicate subscription")
        for s in self.subscriptions:
            if s.topic not in self.topics:
                raise ValueError(f"topology: {s.name} is on unlisted topic {s.topic}")
            if s.dead_letter_topic and s.dead_letter_topic not in self.topics:
                raise ValueError(
                    f"topology: {s.name} dead-letters to unlisted {s.dead_letter_topic}"
                )
            if s.max_delivery_attempts < 1 or s.min_backoff <= 0 or s.max_backoff < s.min_backoff:
                raise ValueError(f"topology: {s.name} has an unusable redelivery policy")


class TopologyBuilder:
    """Builds topologies in the shape of pubsub-init.sh."""

    def __init__(self) -> None:
        self._topics: list[str] = []
        self._subs: list[SubscriptionSpec] = []
        self._policy = SubscriptionSpec(name="", topic="")

    def policy(
        self, max_attempts: int, min_backoff: float, max_backoff: float, ack_deadline: float
    ) -> TopologyBuilder:
        """Set the policy of subscriptions added after this call."""
        self._policy = replace(
            self._policy,
            max_delivery_attempts=max_attempts,
            min_backoff=min_backoff,
            max_backoff=max_backoff,
            ack_deadline=ack_deadline,
        )
        return self

    def topic(self, name: str) -> TopologyBuilder:
        """Add a topic and its dead-letter topic."""
        self._topics += [name, name + DEAD_LETTER_SUFFIX]
        return self

    def subscription(self, name: str, topic: str) -> TopologyBuilder:
        """Add a subscription that dead-letters to its topic's dead-letter topic."""
        self._subs.append(
            replace(
                self._policy, name=name, topic=topic, dead_letter_topic=topic + DEAD_LETTER_SUFFIX
            )
        )
        return self

    def plain_subscription(self, name: str, topic: str) -> TopologyBuilder:
        """Add a subscription without a dead-letter topic (to inspect dead letters)."""
        self._subs.append(replace(self._policy, name=name, topic=topic, dead_letter_topic=""))
        return self

    def build(self) -> Topology:
        """The topology so far."""
        return Topology(tuple(self._topics), tuple(self._subs))


def default_topology() -> Topology:
    """Skein's topology: keep in step with deploy/compose/pubsub-init.sh."""
    return (
        TopologyBuilder()
        .topic("documents.events")
        .topic("identity.events")
        .topic("agent.events")
        .topic("notifications.requests")
        .subscription("search.documents-events", "documents.events")
        .subscription("ml.documents-events", "documents.events")
        .subscription("notifications.documents-events", "documents.events")
        .subscription("identity.documents-events", "documents.events")
        .subscription("documents.identity-events", "identity.events")
        .subscription("search.identity-events", "identity.events")
        .subscription("ml.identity-events", "identity.events")
        .subscription("gateway.identity-events", "identity.events")
        .subscription("notifications.agent-events", "agent.events")
        .subscription("notifications.identity-events", "identity.events")
        .subscription("notifications.requests", "notifications.requests")
        .build()
    )
