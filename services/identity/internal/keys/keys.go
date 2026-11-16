// Package keys holds identity's access-token signing keys and rotates them
// with key IDs, following the procedure in platform/auth:
//
//  1. Stage a new key: its public half is published in the key set, but
//     tokens are still signed with the active key.
//  2. After C (the longest JWKS cache period of any verifier), Activate:
//     the staged key signs from now on, and the previous active key becomes
//     retiring.
//  3. A retiring key stays published until RetireAfter (T, the longest
//     token lifetime, plus the verifiers' leeway) has passed, so every token
//     it signed can still be verified; Prune then drops it.
//
// Key IDs are RFC 7638 thumbprints (auth.KeyID), so a key's ID is derived
// from the key and two different keys cannot share one.
//
// # Invariants (CheckInvariants)
//
//   - K1 exactly one active key, and the signer uses it;
//   - K2 at most one staged key;
//   - K3 key IDs are unique and equal each key's thumbprint;
//   - K4 every retiring key has a removal time; no other key has one.
//
// # Where keys come from
//
// In production every replica must sign with the same key, so keys come
// from a secret (IDENTITY_SIGNING_KEYS, see ParseConfig) and rotation is a
// change to that secret followed by a rolling restart, one step of the
// procedure per change. In dev and test, a key is generated at startup and
// Rotator can run the procedure on a timer; that is only correct for a
// single replica.
//
// # Porting notes
//
// Rust: a `RwLock<Vec<Entry>>` (or `arc-swap` for lock-free reads);
// ed25519-dalek `SigningKey::from_bytes(seed)` reads the same 32-byte seeds.
package keys

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/genuinebnt/skein-go/platform/auth"

	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// State is a key's place in the rotation.
type State int

// Key states.
const (
	// StateStaged keys are published but do not sign yet.
	StateStaged State = iota + 1
	// StateActive is the one key that signs.
	StateActive
	// StateRetiring keys no longer sign and are published until their
	// removal time.
	StateRetiring
)

// String names the state as in IDENTITY_SIGNING_KEYS.
func (s State) String() string {
	switch s {
	case StateStaged:
		return "staged"
	case StateActive:
		return "active"
	case StateRetiring:
		return "retiring"
	default:
		return "unknown"
	}
}

// ErrNoStagedKey means Activate was called with nothing staged.
var ErrNoStagedKey = errors.New("keys: no staged key to activate")

// ErrInvalidConfig means IDENTITY_SIGNING_KEYS could not be used.
var ErrInvalidConfig = errors.New("keys: invalid signing key configuration")

type entry struct {
	id       string
	private  ed25519.PrivateKey
	state    State
	removeAt time.Time // retiring keys only
}

// Keyring is safe for concurrent use: verification reads it on every
// request while rotation writes it rarely.
type Keyring struct {
	mu          sync.RWMutex
	entries     []entry
	signer      *auth.Signer
	now         func() time.Time
	retireAfter time.Duration
}

// Options configure New.
type Options struct {
	// RetireAfter is how long a key stays published after it stops
	// signing: the longest token lifetime plus the verifier leeway.
	RetireAfter time.Duration
	Now         func() time.Time // time.Now when nil
}

// New returns a keyring whose active key is active.
func New(active ed25519.PrivateKey, opts Options) (*Keyring, error) {
	if len(active) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: active key has %d bytes", ErrInvalidConfig, len(active))
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	k := &Keyring{now: opts.Now, retireAfter: opts.RetireAfter}
	k.entries = []entry{{id: idOf(active), private: active, state: StateActive}}
	k.signer = auth.NewSigner(active, opts.Now)
	return k, nil
}

func idOf(priv ed25519.PrivateKey) string {
	pub, _ := priv.Public().(ed25519.PublicKey)
	return auth.KeyID(pub)
}

// Signer returns the signer for the active key. Callers fetch it per token,
// so an Activate takes effect at once.
func (k *Keyring) Signer() *auth.Signer {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.signer
}

// Stage adds key as the next key: published, not signing.
func (k *Keyring) Stage(key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("%w: staged key has %d bytes", ErrInvalidConfig, len(key))
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	id := idOf(key)
	for _, e := range k.entries {
		if e.id == id {
			return fmt.Errorf("%w: key %s is already in the ring", ErrInvalidConfig, id)
		}
		if e.state == StateStaged {
			return fmt.Errorf("%w: a key is already staged", ErrInvalidConfig)
		}
	}
	k.entries = append(k.entries, entry{id: id, private: key, state: StateStaged})
	return nil
}

// AddRetiring publishes a key that no longer signs, until removeAt. Startup
// uses it for retiring keys listed in the configuration.
func (k *Keyring) AddRetiring(key ed25519.PrivateKey, removeAt time.Time) error {
	if len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("%w: retiring key has %d bytes", ErrInvalidConfig, len(key))
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	id := idOf(key)
	for _, e := range k.entries {
		if e.id == id {
			return fmt.Errorf("%w: key %s is already in the ring", ErrInvalidConfig, id)
		}
	}
	k.entries = append(k.entries, entry{id: id, private: key, state: StateRetiring, removeAt: removeAt})
	return nil
}

// Activate makes the staged key sign and starts retiring the previous one.
// It returns the new active key's ID.
func (k *Keyring) Activate() (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	staged := slices.IndexFunc(k.entries, func(e entry) bool { return e.state == StateStaged })
	if staged < 0 {
		return "", ErrNoStagedKey
	}
	now := k.now()
	for i := range k.entries {
		if k.entries[i].state == StateActive {
			k.entries[i].state = StateRetiring
			k.entries[i].removeAt = now.Add(k.retireAfter)
		}
	}
	k.entries[staged].state = StateActive
	k.signer = auth.NewSigner(k.entries[staged].private, k.now)
	return k.entries[staged].id, nil
}

// Prune removes retiring keys whose removal time has passed and returns how
// many it removed.
func (k *Keyring) Prune() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	before := len(k.entries)
	k.entries = slices.DeleteFunc(k.entries, func(e entry) bool {
		return e.state == StateRetiring && !e.removeAt.After(now)
	})
	return before - len(k.entries)
}

// HasStaged reports whether a key is staged.
func (k *Keyring) HasStaged() bool {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return slices.ContainsFunc(k.entries, func(e entry) bool { return e.state == StateStaged })
}

// PublicKey implements auth.KeySource, so identity verifies its own tokens
// with exactly the keys it publishes.
func (k *Keyring) PublicKey(_ context.Context, keyID string) (ed25519.PublicKey, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	for _, e := range k.entries {
		if e.id == keyID {
			pub, _ := e.private.Public().(ed25519.PublicKey)
			return pub, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", auth.ErrUnknownKey, keyID)
}

// JWKS returns every published key (staged, active, retiring), sorted by ID.
func (k *Keyring) JWKS() auth.JWKS {
	k.mu.RLock()
	defer k.mu.RUnlock()
	pubs := make([]ed25519.PublicKey, 0, len(k.entries))
	for _, e := range k.entries {
		pub, _ := e.private.Public().(ed25519.PublicKey)
		pubs = append(pubs, pub)
	}
	return auth.NewKeySet(pubs...).JWKS()
}

// CheckInvariants verifies K1 to K4.
func (k *Keyring) CheckInvariants() error {
	k.mu.RLock()
	defer k.mu.RUnlock()
	active, staged := 0, 0
	seen := map[string]bool{}
	for _, e := range k.entries {
		if seen[e.id] {
			return fmt.Errorf("K3: duplicate key ID %s", e.id)
		}
		seen[e.id] = true
		if idOf(e.private) != e.id {
			return fmt.Errorf("K3: key ID %s is not its thumbprint", e.id)
		}
		switch e.state {
		case StateActive:
			active++
			if k.signer == nil || k.signer.KeyID() != e.id {
				return fmt.Errorf("K1: signer does not use the active key %s", e.id)
			}
		case StateStaged:
			staged++
		case StateRetiring:
		default:
			return fmt.Errorf("unknown state %d for %s", e.state, e.id)
		}
		if (e.state == StateRetiring) == e.removeAt.IsZero() {
			return fmt.Errorf("K4: key %s in state %s has removal time %v", e.id, e.state, e.removeAt)
		}
	}
	if active != 1 {
		return fmt.Errorf("K1: %d active keys", active)
	}
	if staged > 1 {
		return fmt.Errorf("K2: %d staged keys", staged)
	}
	return nil
}

// ConfiguredKey is one entry of IDENTITY_SIGNING_KEYS.
type ConfiguredKey struct {
	State State
	Key   ed25519.PrivateKey
}

// ParseConfig reads IDENTITY_SIGNING_KEYS: comma-separated
// "<state>:<seed>" entries, where state is active, staged or retiring and
// seed is a 32-byte Ed25519 seed in unpadded base64url. Exactly one entry
// must be active. Example rotation, one deploy per line:
//
//	active:AAA
//	active:AAA,staged:BBB        (wait C)
//	active:BBB,retiring:AAA      (wait T + leeway)
//	active:BBB
func ParseConfig(s string) ([]ConfiguredKey, error) {
	var out []ConfiguredKey
	active := 0
	for i, item := range strings.Split(s, ",") {
		name, seed, ok := strings.Cut(strings.TrimSpace(item), ":")
		if !ok {
			return nil, fmt.Errorf("%w: entry %d is not state:seed", ErrInvalidConfig, i)
		}
		var st State
		switch name {
		case "active":
			st = StateActive
			active++
		case "staged":
			st = StateStaged
		case "retiring":
			st = StateRetiring
		default:
			return nil, fmt.Errorf("%w: entry %d has unknown state %q", ErrInvalidConfig, i, name)
		}
		raw, err := base64.RawURLEncoding.Strict().DecodeString(seed)
		if err != nil || len(raw) != ed25519.SeedSize {
			// The seed is secret: never echo it.
			return nil, fmt.Errorf("%w: entry %d is not a 32-byte base64url seed", ErrInvalidConfig, i)
		}
		out = append(out, ConfiguredKey{State: st, Key: ed25519.NewKeyFromSeed(raw)})
	}
	if active != 1 {
		return nil, fmt.Errorf("%w: %d active keys, want exactly 1", ErrInvalidConfig, active)
	}
	return out, nil
}

// FromConfig builds a keyring from parsed configuration. Retiring keys are
// published for opts.RetireAfter from startup, which errs on the side of
// keeping them: the operator removes them from the secret afterwards.
func FromConfig(cfg []ConfiguredKey, opts Options) (*Keyring, error) {
	var ring *Keyring
	for _, c := range cfg {
		if c.State == StateActive {
			r, err := New(c.Key, opts)
			if err != nil {
				return nil, err
			}
			ring = r
		}
	}
	if ring == nil {
		return nil, fmt.Errorf("%w: no active key", ErrInvalidConfig)
	}
	for _, c := range cfg {
		var err error
		switch c.State {
		case StateStaged:
			err = ring.Stage(c.Key)
		case StateRetiring:
			err = ring.AddRetiring(c.Key, ring.now().Add(opts.RetireAfter))
		}
		if err != nil {
			return nil, err
		}
	}
	return ring, nil
}

// Rotator runs the rotation procedure on a timer, for single-replica dev
// deployments with generated keys. Every Interval it prunes, then either
// stages a new key (nothing staged) or activates the staged one (staged
// since the last tick, which must be at least C ago).
type Rotator struct {
	Ring     *Keyring
	Interval time.Duration
	Generate func() (ed25519.PrivateKey, error) // auth.GenerateKey when nil
}

// Tick runs one step of the procedure.
func (r *Rotator) Tick() error {
	r.Ring.Prune()
	if r.Ring.HasStaged() {
		_, err := r.Ring.Activate()
		return err
	}
	gen := r.Generate
	if gen == nil {
		gen = auth.GenerateKey
	}
	key, err := gen()
	if err != nil {
		return err
	}
	return r.Ring.Stage(key)
}

// Run ticks every Interval until ctx ends.
func (r *Rotator) Run(ctx context.Context) error {
	t := time.NewTicker(r.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := r.Tick(); err != nil {
				return err
			}
		}
	}
}

// Issue implements app.TokenIssuer: it signs with the active key, so a
// token's kid always names a published key.
func (k *Keyring) Issue(subject domain.Subject, ttl time.Duration) (string, time.Time, error) {
	tok, claims, err := k.Signer().Issue(auth.Subject{UserID: string(subject.UserID), AgentRunID: string(subject.AgentRunID)}, ttl)
	if err != nil {
		return "", time.Time{}, err
	}
	return tok, claims.ExpiresAt, nil
}
