//go:build integration

// Package testredis gives the gateway's integration tests a real Redis:
// the server named by SKEIN_TEST_REDIS_URL (make integration-native points
// it at the native stack's Redis), or a redis:7-alpine container started
// with testcontainers when the variable is unset.
//
// Every call returns a fresh namespace, and the gateway prefixes all of
// its keys and channels with the namespace, so tests never see each
// other's keys or the running stack's.
//
// # Porting notes
//
// Rust: the same environment variable, the testcontainers crate's redis
// module, and a per-test key prefix.
package testredis

import (
	"context"
	"crypto/rand"
	"os"
	"strconv"
	"testing"

	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/redisx"
)

// Open returns a client for a real Redis and a namespace unique to the
// test. The client is closed and the container stopped when t ends.
func Open(t *testing.T) (*redis.Client, string) {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("SKEIN_TEST_REDIS_URL")
	if url == "" {
		container, err := tcredis.Run(ctx, "redis:7-alpine")
		if err != nil {
			t.Fatalf("start redis (or set SKEIN_TEST_REDIS_URL): %v", err)
		}
		t.Cleanup(func() { _ = container.Terminate(context.Background()) })
		if url, err = container.ConnectionString(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return Client(t, url), "itest-" + rand.Text()[:12]
}

// Client opens another client for url, as a second gateway replica would.
func Client(t *testing.T, url string) *redis.Client {
	t.Helper()
	client, err := redisx.Open(context.Background(), redisx.Config{URL: config.NewSecret(url)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// URL returns the address client was opened with, for a second replica.
// The test servers have no password, so none is carried over.
func URL(client *redis.Client) string {
	opts := client.Options()
	return "redis://" + opts.Addr + "/" + strconv.Itoa(opts.DB)
}
