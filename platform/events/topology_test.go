package events

import (
	"bufio"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pubsubInit parses the topic and subscription calls and the shared
// policy out of deploy/compose/pubsub-init.sh.
func pubsubInit(t *testing.T) Topology {
	t.Helper()
	f, err := os.Open("../../deploy/compose/pubsub-init.sh")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	attempts := regexp.MustCompile(`\\?"maxDeliveryAttempts\\?": (\d+)`)
	backoff := regexp.MustCompile(`\\?"minimumBackoff\\?": \\?"(\d+)s\\?", \\?"maximumBackoff\\?": \\?"(\d+)s\\?"`)
	deadline := regexp.MustCompile(`\\?"ackDeadlineSeconds\\?": (\d+)`)
	var topics []string
	var subs [][2]string
	policy := SubscriptionSpec{}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		fields := strings.Fields(line)
		switch {
		case len(fields) == 2 && fields[0] == "topic":
			topics = append(topics, fields[1], fields[1]+DeadLetterSuffix)
		case len(fields) == 3 && fields[0] == "subscription":
			subs = append(subs, [2]string{fields[1], fields[2]})
		}
		if m := attempts.FindStringSubmatch(line); m != nil {
			policy.MaxDeliveryAttempts, _ = strconv.Atoi(m[1])
		}
		if m := backoff.FindStringSubmatch(line); m != nil {
			lo, _ := strconv.Atoi(m[1])
			hi, _ := strconv.Atoi(m[2])
			policy.MinBackoff, policy.MaxBackoff = time.Duration(lo)*time.Second, time.Duration(hi)*time.Second
		}
		if m := deadline.FindStringSubmatch(line); m != nil {
			s, _ := strconv.Atoi(m[1])
			policy.AckDeadline = time.Duration(s) * time.Second
		}
	}
	b := NewTopologyBuilder().Policy(policy.MaxDeliveryAttempts, policy.MinBackoff, policy.MaxBackoff, policy.AckDeadline)
	out := b.Build()
	out.Topics = topics
	for _, s := range subs {
		b.Subscription(s[0], s[1])
	}
	out.Subscriptions = b.Build().Subscriptions
	return out
}

func TestDefaultTopologyMatchesPubSubInit(t *testing.T) {
	want := pubsubInit(t)
	got := DefaultTopology()
	if len(want.Subscriptions) == 0 || len(want.Topics) == 0 {
		t.Fatal("parsed nothing from pubsub-init.sh; did its format change?")
	}
	if !slices.Equal(got.Topics, want.Topics) {
		t.Errorf("topics differ:\n got %v\nwant %v", got.Topics, want.Topics)
	}
	if !slices.Equal(got.Subscriptions, want.Subscriptions) {
		t.Errorf("subscriptions differ:\n got %+v\nwant %+v", got.Subscriptions, want.Subscriptions)
	}
	if err := got.Validate(); err != nil {
		t.Error(err)
	}
}

func TestBackoffDoublesFromMinimumAndCapsAtMaximum(t *testing.T) {
	s := DefaultTopology().Subscriptions[0]
	var got []time.Duration
	for attempt := range 9 {
		got = append(got, s.Backoff(attempt+1))
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60}
	for i := range want {
		want[i] *= time.Second
	}
	if !slices.Equal(got, want) {
		t.Fatalf("backoff %v, want %v", got, want)
	}
	var total time.Duration
	for _, d := range got {
		total += d
	}
	// pubsub-init.sh promises that ten attempts span about four minutes.
	if total < 3*time.Minute || total > 5*time.Minute {
		t.Errorf("nine waits total %v", total)
	}
}

func TestTopologyValidateRejectsDanglingNames(t *testing.T) {
	good := NewTopologyBuilder().Topic("t").Subscription("s", "t").Build()
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, topo := range map[string]Topology{
		"subscription on unknown topic": {Topics: []string{"t"}, Subscriptions: []SubscriptionSpec{{Name: "s", Topic: "x", MaxDeliveryAttempts: 1, MinBackoff: 1, MaxBackoff: 1, AckDeadline: 1}}},
		"unknown dead-letter topic":     {Topics: []string{"t"}, Subscriptions: []SubscriptionSpec{{Name: "s", Topic: "t", DeadLetterTopic: "d", MaxDeliveryAttempts: 1, MinBackoff: 1, MaxBackoff: 1, AckDeadline: 1}}},
		"duplicate topic":               {Topics: []string{"t", "t"}},
		"zero attempts":                 {Topics: []string{"t"}, Subscriptions: []SubscriptionSpec{{Name: "s", Topic: "t", MinBackoff: 1, MaxBackoff: 1, AckDeadline: 1}}},
	} {
		if topo.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
