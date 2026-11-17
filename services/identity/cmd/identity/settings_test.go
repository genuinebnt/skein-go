package main

import (
	"encoding/base64"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/platform/config"

	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
)

func TestDefaultsInDevNeedNoSecrets(t *testing.T) {
	l := config.FromMap(map[string]string{})
	s := loadSettings(l)
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
	if s.Argon2 != credential.DefaultParams || s.App.ShareLinkDefaultTTL != 7*24*time.Hour || s.GRPCAddr != ":9090" || s.AdminAddr != ":8080" {
		t.Fatalf("%+v", s)
	}
	ring, err := s.keyring(time.Now)
	if err != nil || ring.CheckInvariants() != nil {
		t.Fatalf("generated keyring: %v", err)
	}
}

func TestProductionRefusesUnsafeSettings(t *testing.T) {
	l := config.FromMap(map[string]string{
		"SKEIN_ENV":                      "prod",
		"IDENTITY_FIXED_NOW":             "2026-11-01T00:00:00Z",
		"IDENTITY_KEY_ROTATION_INTERVAL": "1h",
		"IDENTITY_GOOGLE_CLIENT_ID":      "client",
	})
	loadSettings(l)
	err := l.Err()
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{"IDENTITY_SIGNING_KEYS", "IDENTITY_FIXED_NOW", "IDENTITY_KEY_ROTATION_INTERVAL", "IDENTITY_GOOGLE_CLIENT_SECRET", "IDENTITY_OAUTH_REDIRECT_URIS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestSigningKeysComeFromTheSecretAndAreNeverEchoed(t *testing.T) {
	seed := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	l := config.FromMap(map[string]string{"SKEIN_ENV": "prod", "IDENTITY_SIGNING_KEYS": "active:" + seed})
	s := loadSettings(l)
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.keyring(time.Now); err != nil {
		t.Fatal(err)
	}
	bad := config.FromMap(map[string]string{"IDENTITY_SIGNING_KEYS": "active:" + seed[:20]})
	loadSettings(bad)
	if err := bad.Err(); err == nil || strings.Contains(err.Error(), seed[:20]) {
		t.Fatalf("bad key: %v", err)
	}
}

func TestDevSessionsAreRefusedOutsideDevAndTest(t *testing.T) {
	for env, allowed := range map[string]bool{"dev": true, "test": true, "staging": false, "prod": false} {
		vars := map[string]string{"SKEIN_ENV": env, "IDENTITY_DEV_SESSIONS": "true"}
		if env == "staging" || env == "prod" {
			vars["IDENTITY_SIGNING_KEYS"] = "active:" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
		}
		l := config.FromMap(vars)
		s := loadSettings(l)
		err := l.Err()
		if allowed && (err != nil || !s.App.DevSessions) {
			t.Errorf("%s: %v, enabled %v", env, err, s.App.DevSessions)
		}
		if !allowed && (err == nil || !strings.Contains(err.Error(), "IDENTITY_DEV_SESSIONS")) {
			t.Errorf("%s: accepted (%v)", env, err)
		}
	}
}

// The default share-link lifetime is part of the contract: identity.proto
// and docs/api/identity.md state it, and they once said "never expires"
// while the service used 7 days. This keeps the three in step.
func TestShareLinkDefaultExpiryIsTheOneTheContractDocuments(t *testing.T) {
	s := loadSettings(config.FromMap(map[string]string{}))
	days := int(s.App.ShareLinkDefaultTTL / (24 * time.Hour))
	want := strconv.Itoa(days) + " days after"
	for _, path := range []string{"../../../../proto/skein/v1/identity.proto", "../../../../docs/api/identity.md"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(strings.Fields(string(raw)), " ")
		if !strings.Contains(text, want) {
			t.Errorf("%s does not document the default share-link expiry (%q)", path, want)
		}
		if strings.Contains(text, "Unset means it never expires") {
			t.Errorf("%s still says links without expires_at never expire", path)
		}
	}
}
