package events

import (
	"fmt"
	"slices"
	"time"
)

// DeadLetterSuffix names a topic's dead-letter topic: "documents.events"
// dead-letters to "documents.events.dead-letter".
const DeadLetterSuffix = ".dead-letter"

// Redelivery settings shared by every subscription in
// deploy/compose/pubsub-init.sh. Backoff doubles from MinBackoff and is
// capped at MaxBackoff, so ten attempts span about four minutes.
const (
	DefaultMinBackoff  = time.Second
	DefaultMaxBackoff  = 60 * time.Second
	DefaultAckDeadline = 30 * time.Second
)

// SubscriptionSpec describes one subscription: the consumer's own queue on
// a topic, with its redelivery policy.
type SubscriptionSpec struct {
	Name  string
	Topic string
	// DeadLetterTopic receives a message after MaxDeliveryAttempts failed
	// deliveries. Empty means retry forever (Pub/Sub without a policy).
	DeadLetterTopic     string
	MaxDeliveryAttempts int
	MinBackoff          time.Duration
	MaxBackoff          time.Duration
	// AckDeadline is how long a delivery may go unacknowledged before
	// another consumer may take it: Pub/Sub's ack deadline, the Redis
	// backend's partition lease.
	AckDeadline time.Duration
}

// Backoff returns how long to wait before redelivering after the given
// failed attempt (counting from 1): MinBackoff doubled per earlier attempt,
// capped at MaxBackoff. Pub/Sub documents the same exponential shape.
func (s SubscriptionSpec) Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := s.MinBackoff
	for i := 1; i < attempt && d < s.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, s.MaxBackoff)
}

// Topology is every topic and subscription. Pub/Sub's are created by
// infrastructure; Memory and Redis create theirs from a Topology at startup
// (Provision).
type Topology struct {
	Topics        []string
	Subscriptions []SubscriptionSpec
}

// Subscription returns the named subscription.
func (t Topology) Subscription(name string) (SubscriptionSpec, bool) {
	i := slices.IndexFunc(t.Subscriptions, func(s SubscriptionSpec) bool { return s.Name == name })
	if i < 0 {
		return SubscriptionSpec{}, false
	}
	return t.Subscriptions[i], true
}

// HasTopic reports whether the topology lists topic.
func (t Topology) HasTopic(topic string) bool { return slices.Contains(t.Topics, topic) }

// Validate reports a subscription on an unlisted topic, a dead-letter
// topic that is not listed, a duplicate name or an unusable policy.
func (t Topology) Validate() error {
	topics := map[string]bool{}
	for _, topic := range t.Topics {
		if topic == "" || topics[topic] {
			return fmt.Errorf("events: topology: empty or duplicate topic %q", topic)
		}
		topics[topic] = true
	}
	names := map[string]bool{}
	for _, s := range t.Subscriptions {
		switch {
		case s.Name == "" || names[s.Name]:
			return fmt.Errorf("events: topology: empty or duplicate subscription %q", s.Name)
		case !topics[s.Topic]:
			return fmt.Errorf("events: topology: subscription %q is on unlisted topic %q", s.Name, s.Topic)
		case s.DeadLetterTopic != "" && !topics[s.DeadLetterTopic]:
			return fmt.Errorf("events: topology: subscription %q dead-letters to unlisted topic %q", s.Name, s.DeadLetterTopic)
		case s.MaxDeliveryAttempts < 1, s.MinBackoff <= 0, s.MaxBackoff < s.MinBackoff, s.AckDeadline <= 0:
			return fmt.Errorf("events: topology: subscription %q has an unusable redelivery policy", s.Name)
		}
		names[s.Name] = true
	}
	return nil
}

// TopologyBuilder makes topologies in the shape of pubsub-init.sh: each
// topic with its dead-letter topic, each subscription with the shared
// policy.
type TopologyBuilder struct {
	topology Topology
	policy   SubscriptionSpec
}

// NewTopologyBuilder starts a topology whose subscriptions use the
// pubsub-init.sh policy: 10 attempts, 1 s to 60 s backoff, 30 s deadline.
func NewTopologyBuilder() *TopologyBuilder {
	return &TopologyBuilder{policy: SubscriptionSpec{
		MaxDeliveryAttempts: DefaultMaxDeliveryAttempts,
		MinBackoff:          DefaultMinBackoff,
		MaxBackoff:          DefaultMaxBackoff,
		AckDeadline:         DefaultAckDeadline,
	}}
}

// Policy sets the redelivery policy of subscriptions added after it (tests
// use short backoffs).
func (b *TopologyBuilder) Policy(maxAttempts int, minBackoff, maxBackoff, ackDeadline time.Duration) *TopologyBuilder {
	b.policy.MaxDeliveryAttempts, b.policy.MinBackoff, b.policy.MaxBackoff, b.policy.AckDeadline =
		maxAttempts, minBackoff, maxBackoff, ackDeadline
	return b
}

// Topic adds a topic and its dead-letter topic.
func (b *TopologyBuilder) Topic(name string) *TopologyBuilder {
	b.topology.Topics = append(b.topology.Topics, name, name+DeadLetterSuffix)
	return b
}

// Subscription adds a subscription on topic that dead-letters to the
// topic's dead-letter topic.
func (b *TopologyBuilder) Subscription(name, topic string) *TopologyBuilder {
	s := b.policy
	s.Name, s.Topic, s.DeadLetterTopic = name, topic, topic+DeadLetterSuffix
	b.topology.Subscriptions = append(b.topology.Subscriptions, s)
	return b
}

// PlainSubscription adds a subscription without a dead-letter topic, as a
// person would attach to a dead-letter topic to inspect it.
func (b *TopologyBuilder) PlainSubscription(name, topic string) *TopologyBuilder {
	s := b.policy
	s.Name, s.Topic, s.DeadLetterTopic = name, topic, ""
	b.topology.Subscriptions = append(b.topology.Subscriptions, s)
	return b
}

// Build returns the topology.
func (b *TopologyBuilder) Build() Topology {
	return Topology{
		Topics:        slices.Clone(b.topology.Topics),
		Subscriptions: slices.Clone(b.topology.Subscriptions),
	}
}

// DefaultTopology is Skein's topology. deploy/compose/pubsub-init.sh is the
// source of truth (it provisions the emulator, and Terraform follows it);
// this mirrors it for the backends that provision themselves, and
// TestDefaultTopologyMatchesPubSubInit fails on any difference. The Python
// copy (skein_events.topology) has the same test.
func DefaultTopology() Topology {
	return NewTopologyBuilder().
		Topic("documents.events").
		Topic("identity.events").
		Topic("agent.events").
		Topic("notifications.requests").
		Subscription("search.documents-events", "documents.events").
		Subscription("ml.documents-events", "documents.events").
		Subscription("notifications.documents-events", "documents.events").
		Subscription("identity.documents-events", "documents.events").
		Subscription("documents.identity-events", "identity.events").
		Subscription("search.identity-events", "identity.events").
		Subscription("ml.identity-events", "identity.events").
		Subscription("gateway.identity-events", "identity.events").
		Subscription("notifications.agent-events", "agent.events").
		Subscription("notifications.identity-events", "identity.events").
		Subscription("notifications.requests", "notifications.requests").
		Build()
}
