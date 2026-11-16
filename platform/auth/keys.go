package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
)

// PublicKey is an Ed25519 verification key and its ID.
type PublicKey struct {
	ID  string
	Key ed25519.PublicKey
}

// KeyID returns the RFC 7638 thumbprint of an Ed25519 public key: the
// base64url SHA-256 of its canonical JWK, {"crv":"Ed25519","kty":"OKP","x":...}.
func KeyID(pub ed25519.PublicKey) string {
	canonical := `{"crv":"Ed25519","kty":"OKP","x":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}`
	sum := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// GenerateKey creates a new signing key. identity stores the private key in
// Secret Manager; tests and local runs generate one at startup.
func GenerateKey() (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("auth: generate key: %w", err)
	}
	return priv, nil
}

// KeySource finds the public key for a key ID. KeySet and RemoteKeySet
// implement it.
type KeySource interface {
	PublicKey(ctx context.Context, keyID string) (ed25519.PublicKey, error)
}

// KeySet is an immutable set of public keys. Build a new one to rotate.
type KeySet struct {
	keys map[string]ed25519.PublicKey
}

// NewKeySet returns a set of keys. Each key's ID is recomputed from the key
// itself, so a mislabelled key cannot shadow another.
func NewKeySet(keys ...ed25519.PublicKey) KeySet {
	set := KeySet{keys: make(map[string]ed25519.PublicKey, len(keys))}
	for _, k := range keys {
		set.keys[KeyID(k)] = slices.Clone(k)
	}
	return set
}

// With returns a copy of the set with keys added.
func (s KeySet) With(keys ...ed25519.PublicKey) KeySet {
	all := make([]ed25519.PublicKey, 0, len(s.keys)+len(keys))
	for _, k := range s.keys {
		all = append(all, k)
	}
	return NewKeySet(append(all, keys...)...)
}

// Without returns a copy of the set without the key with this ID.
func (s KeySet) Without(keyID string) KeySet {
	out := KeySet{keys: make(map[string]ed25519.PublicKey, len(s.keys))}
	for id, k := range s.keys {
		if id != keyID {
			out.keys[id] = k
		}
	}
	return out
}

// Len returns the number of keys.
func (s KeySet) Len() int { return len(s.keys) }

// PublicKey implements KeySource.
func (s KeySet) PublicKey(_ context.Context, keyID string) (ed25519.PublicKey, error) {
	k, ok := s.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKey, keyID)
	}
	return k, nil
}

// JWK is one entry of a JSON Web Key Set, restricted to Ed25519 signing
// keys.
type JWK struct {
	KeyType   string `json:"kty"`
	Curve     string `json:"crv"`
	X         string `json:"x"`
	KeyID     string `json:"kid"`
	Use       string `json:"use"`
	Algorithm string `json:"alg"`
}

// JWKS is the body of the public key set endpoint, which identity serves
// at GET /.well-known/jwks.json through the gateway. Internal callers fetch
// the same document through whatever RPC the contracts give identity for
// it. Cache it for at most C, the cache period in the rotation procedure.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWKS returns the set in its published shape, sorted by key ID so the
// output is stable.
func (s KeySet) JWKS() JWKS {
	out := JWKS{Keys: make([]JWK, 0, len(s.keys))}
	for id, k := range s.keys {
		out.Keys = append(out.Keys, JWK{
			KeyType: "OKP", Curve: "Ed25519", X: base64.RawURLEncoding.EncodeToString(k),
			KeyID: id, Use: "sig", Algorithm: algorithm,
		})
	}
	slices.SortFunc(out.Keys, func(a, b JWK) int {
		switch {
		case a.KeyID < b.KeyID:
			return -1
		case a.KeyID > b.KeyID:
			return 1
		}
		return 0
	})
	return out
}

// ParseJWKS decodes a JWKS document. Every entry must be a well-formed
// Ed25519 signing key whose kid equals its thumbprint; any other entry
// fails the whole document, because a key set that is partly wrong means
// the publisher is misconfigured and silently dropping keys would surface
// later as unexplained ErrUnknownKey failures.
func ParseJWKS(data []byte) (KeySet, error) {
	var doc JWKS
	if err := json.Unmarshal(data, &doc); err != nil {
		return KeySet{}, fmt.Errorf("%w: %v", ErrInvalidKeySet, err)
	}
	keys := make([]ed25519.PublicKey, 0, len(doc.Keys))
	for i, k := range doc.Keys {
		if k.KeyType != "OKP" || k.Curve != "Ed25519" {
			return KeySet{}, fmt.Errorf("%w: key %d is %s/%s, want OKP/Ed25519", ErrInvalidKeySet, i, k.KeyType, k.Curve)
		}
		if k.Algorithm != "" && k.Algorithm != algorithm {
			return KeySet{}, fmt.Errorf("%w: key %d has alg %q", ErrInvalidKeySet, i, k.Algorithm)
		}
		if k.Use != "" && k.Use != "sig" {
			return KeySet{}, fmt.Errorf("%w: key %d has use %q", ErrInvalidKeySet, i, k.Use)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			return KeySet{}, fmt.Errorf("%w: key %d has a malformed x", ErrInvalidKeySet, i)
		}
		pub := ed25519.PublicKey(x)
		if KeyID(pub) != k.KeyID {
			return KeySet{}, fmt.Errorf("%w: key %d kid is not its thumbprint", ErrInvalidKeySet, i)
		}
		keys = append(keys, pub)
	}
	return NewKeySet(keys...), nil
}
