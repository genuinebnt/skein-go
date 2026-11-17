//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"
)

// pubsubURL is the emulator's REST endpoint (not a journey: this file checks
// the stack's configuration, which no gateway call can see).
var pubsubURL = localURL("E2E_PUBSUB_URL", "SKEIN_NATIVE_PUBSUB_PORT", "28085")

// Every subscription backs off before redelivering and dead-letters after
// its attempts. Without a retry policy a nacked event came back at once and
// a one-second outage of a dependency dead-lettered it (seen on the
// notifications consumer while identity restarted).
func TestEverySubscriptionBacksOffBeforeDeadLettering(t *testing.T) {
	t.Parallel()
	resp, err := httpClient.Get(pubsubURL + "/v1/projects/skein-local/subscriptions")
	if err != nil {
		t.Fatalf("emulator: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Subscriptions []struct {
			Name             string `json:"name"`
			DeadLetterPolicy *struct {
				MaxDeliveryAttempts int `json:"maxDeliveryAttempts"`
			} `json:"deadLetterPolicy"`
			RetryPolicy *struct {
				MinimumBackoff string `json:"minimumBackoff"`
				MaximumBackoff string `json:"maximumBackoff"`
			} `json:"retryPolicy"`
		} `json:"subscriptions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Subscriptions) < 11 {
		t.Fatalf("the emulator has %d subscriptions; deploy/compose/pubsub-init.sh creates 11", len(body.Subscriptions))
	}
	for _, s := range body.Subscriptions {
		if s.DeadLetterPolicy == nil || s.DeadLetterPolicy.MaxDeliveryAttempts == 0 {
			t.Errorf("%s has no dead-letter policy", s.Name)
		}
		if s.RetryPolicy == nil || s.RetryPolicy.MinimumBackoff == "" || s.RetryPolicy.MinimumBackoff == "0s" {
			t.Errorf("%s redelivers at once (retry policy %+v)", s.Name, s.RetryPolicy)
		}
	}
}
