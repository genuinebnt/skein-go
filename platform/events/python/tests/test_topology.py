"""default_topology must equal deploy/compose/pubsub-init.sh, the source of truth."""

from __future__ import annotations

import re

from conftest import REPO

from skein_events import TopologyBuilder, default_topology


def _pubsub_init():
    text = (REPO / "deploy/compose/pubsub-init.sh").read_text()
    attempts = int(re.search(r'\\?"maxDeliveryAttempts\\?": (\d+)', text).group(1))
    lo, hi = re.search(
        r'\\?"minimumBackoff\\?": \\?"(\d+)s\\?", \\?"maximumBackoff\\?": \\?"(\d+)s', text
    ).groups()
    deadline = int(re.search(r'\\?"ackDeadlineSeconds\\?": (\d+)', text).group(1))
    b = TopologyBuilder().policy(attempts, float(lo), float(hi), float(deadline))
    for line in text.splitlines():
        fields = line.split()
        if len(fields) == 2 and fields[0] == "topic":
            b.topic(fields[1])
        elif len(fields) == 3 and fields[0] == "subscription":
            b.subscription(fields[1], fields[2])
    return b.build()


def test_default_topology_matches_pubsub_init():
    want = _pubsub_init()
    assert want.subscriptions, "parsed nothing; did pubsub-init.sh change shape?"
    assert default_topology() == want
    default_topology().validate()


def test_backoff_doubles_from_one_second_and_caps_at_sixty():
    spec = default_topology().subscriptions[0]
    assert [spec.backoff(a) for a in range(1, 10)] == [1, 2, 4, 8, 16, 32, 60, 60, 60]
