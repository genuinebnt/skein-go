#!/bin/sh
# Creates Skein's topics and subscriptions on the local Pub/Sub emulator.
# Each consumer gets its own subscription; each topic has a dead letter topic.
set -eu
API="http://pubsub:8085/v1/projects/skein-local"

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
    \"deadLetterPolicy\": {\"deadLetterTopic\": \"projects/skein-local/topics/$2.dead-letter\", \"maxDeliveryAttempts\": 10}
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
