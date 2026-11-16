package main

import (
	"encoding/base64"
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
