//go:build integration

package redisx

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/genuinebnt/skein-go/platform/config"
)

// TestRealRedisMatchesMemoryStore repeats the tag and expiry checks against
// the same Redis image as deploy/compose, because miniredis runs Lua in
// gopher-lua rather than Redis's own interpreter.
func TestRealRedisMatchesMemoryStore(t *testing.T) {
	ctx := context.Background()
	// SKEIN_TEST_REDIS_URL runs against an existing server (make up-native)
	// instead of a container. Keys get a per-run prefix, so a shared server
	// (and the stack's own keys) is left alone.
	url := os.Getenv("SKEIN_TEST_REDIS_URL")
	if url == "" {
		container, err := tcredis.Run(ctx, "redis:7-alpine")
		if err != nil {
			t.Fatalf("start redis: %v", err)
		}
		t.Cleanup(func() { _ = container.Terminate(context.Background()) })
		if url, err = container.ConnectionString(ctx); err != nil {
			t.Fatal(err)
		}
	}
	client, err := Open(ctx, Config{URL: config.NewSecret(url)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	store := NewRedisStore(client, "itest-"+strconv.FormatInt(time.Now().UnixNano(), 36))

	_ = store.Set(ctx, "a", []byte("1"), time.Minute, "t1", "t2")
	_ = store.Set(ctx, "b", []byte("2"), time.Minute, "t1")
	_ = store.Set(ctx, "a", []byte("1b"), time.Minute, "t2") // a no longer carries t1
	if n, err := store.InvalidateTags(ctx, "t1"); err != nil || n != 1 {
		t.Fatalf("InvalidateTags(t1) = %d %v, want only b", n, err)
	}
	if v, found, _ := store.Get(ctx, "a"); !found || string(v) != "1b" {
		t.Fatalf("a = %q %v", v, found)
	}
	_ = store.Set(ctx, "short", []byte("x"), 50*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	if _, found, _ := store.Get(ctx, "short"); found {
		t.Fatal("entry did not expire")
	}
}
