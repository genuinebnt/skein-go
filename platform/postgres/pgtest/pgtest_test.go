package pgtest

import "testing"

func TestFreshDatabaseDefersToTheCallerWhenNoServerIsNamed(t *testing.T) {
	t.Setenv(EnvDSN, "")
	if url, ok := FreshDatabase(t); ok || url != "" {
		t.Fatalf("got %q %v without %s", url, ok, EnvDSN)
	}
}
