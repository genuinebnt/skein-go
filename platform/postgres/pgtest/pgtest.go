// Package pgtest gives integration tests a disposable PostgreSQL database
// on an existing server, so they can run without Docker: against make
// up-native, a CI service container, or any PostgreSQL 16+.
//
// FreshDatabase reads SKEIN_TEST_POSTGRES_DSN, a connection URL for a role
// that may create databases (the native stack's superuser:
// postgres://skein@127.0.0.1:25432/postgres?sslmode=disable). It creates a
// uniquely named database, returns its URL, and drops it when the test
// ends. Each test gets its own database, which isolates it as completely as
// a fresh testcontainers server does, and nothing the running stack owns is
// touched. When the variable is unset it reports ok=false and the caller
// starts its container as before, so Docker stays the default.
//
// Porting notes: Rust tests do the same with tokio-postgres; the database
// name is the only state, so no shared registry is needed.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// EnvDSN names the variable holding the admin connection URL.
const EnvDSN = "SKEIN_TEST_POSTGRES_DSN"

// FreshDatabase returns the URL of a new, empty database on the server in
// SKEIN_TEST_POSTGRES_DSN, dropped when t ends, or ok=false when the
// variable is unset.
func FreshDatabase(t testing.TB) (dbURL string, ok bool) {
	t.Helper()
	admin := os.Getenv(EnvDSN)
	if admin == "" {
		return "", false
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "skein_test_" + hex.EncodeToString(suffix)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("pgtest: connect to %s: %v", EnvDSN, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	// The name is generated above from hex digits, so quoting is enough.
	if _, err := conn.Exec(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatalf("pgtest: create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := pgx.Connect(ctx, admin)
		if err != nil {
			t.Logf("pgtest: drop %s: %v", name, err)
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		if _, err := c.Exec(ctx, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`); err != nil {
			t.Logf("pgtest: drop %s: %v", name, err)
		}
	})

	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("pgtest: %s is not a URL: %v", EnvDSN, err)
	}
	u.Path = "/" + name
	return u.String(), true
}
