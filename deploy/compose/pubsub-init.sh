#!/bin/sh
# Creates Skein's topics and subscriptions on the local Pub/Sub emulator.
# Each consumer gets its own subscription; each topic has a dead letter topic.
# Redelivery backs off exponentially from 1 s to 60 s: without a retry
# policy a nacked message comes back at once, so a dependency down for one
# second exhausted all 10 attempts and dead-lettered the event. With it, 10
# attempts span about four minutes.
set -eu
# PUBSUB_API_BASE points the script at another emulator (the native stack,
# deploy/local/up.sh); compose uses the default.
API="${PUBSUB_API_BASE:-http://pubsub:8085}/v1/projects/skein-local"

topic() {
  curl -sf -X PUT "$API/topics/$1" >/dev/null
  curl -sf -X PUT "$API/topics/$1.dead-letter" >/dev/null
  echo "topic $1"
}

subscription() { # name topic
  curl -sf -X PUT "$API/subscriptions/$1" -H 'Content-Type: application/json' -d "{
    \"topic\": \"projects/skein-local/topics/$2\",
    \"enableMessageOrdering\": true,
    \"ackDeadlineSeconds\": 30,
    \"deadLetterPolicy\": {\"deadLetterTopic\": \"projects/skein-local/topics/$2.dead-letter\", \"maxDeliveryAttempts\": 10},
    \"retryPolicy\": {\"minimumBackoff\": \"1s\", \"maximumBackoff\": \"60s\"}
  }" >/dev/null
  echo "subscription $1 -> $2"
}

topic documents.events
topic identity.events
topic agent.events
topic notifications.requests

subscription search.documents-events documents.events
subscription ml.documents-events documents.events
subscription notifications.documents-events documents.events
subscription identity.documents-events documents.events
subscription documents.identity-events identity.events
subscription search.identity-events identity.events
subscription ml.identity-events identity.events
subscription gateway.identity-events identity.events
subscription notifications.agent-events agent.events
subscription notifications.identity-events identity.events
subscription notifications.requests notifications.requests
