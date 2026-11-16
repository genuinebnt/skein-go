package credential

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Errors returned by this package.
var (
	// ErrMalformedHash means a stored password hash is not a PHC Argon2id
	// string within the accepted bounds.
	ErrMalformedHash = errors.New("credential: malformed password hash")
	// ErrMalformedToken means an opaque token is not of the expected kind
	// and shape.
	ErrMalformedToken = errors.New("credential: malformed token")
	// ErrWeakPassword means a new password fails the strength policy.
	ErrWeakPassword = errors.New("credential: weak password")
)

// Params are Argon2id cost parameters.
type Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams is RFC 9106's second recommended option (see the package
// documentation).
var DefaultParams = Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 4, SaltLength: 16, KeyLength: 32}

// Bounds on parameters the parser accepts.
const (
	MinMemoryKiB = 8          // Argon2 requires m >= 8p; p >= 1
	MaxMemoryKiB = 256 * 1024 // 256 MiB
	MaxIter      = 16
	MinSaltLen   = 8
	MaxSaltLen   = 64
	MinKeyLen    = 16
	MaxKeyLen    = 64
	// MaxPasswordBytes bounds the input to the hash; longer inputs are
	// refused rather than silently truncated.
	MaxPasswordBytes = 1024
)

const phcPrefix = "$argon2id$v=19$"

// Validate checks that p is within the parser's bounds, so every hash this
// package writes can be read back.
func (p Params) Validate() error {
	switch {
	case p.Parallelism == 0, p.MemoryKiB < 8*uint32(p.Parallelism), p.MemoryKiB > MaxMemoryKiB:
		return fmt.Errorf("%w: memory %d KiB with parallelism %d", ErrMalformedHash, p.MemoryKiB, p.Parallelism)
	case p.Iterations == 0, p.Iterations > MaxIter:
		return fmt.Errorf("%w: iterations %d", ErrMalformedHash, p.Iterations)
	case p.SaltLength < MinSaltLen, p.SaltLength > MaxSaltLen:
		return fmt.Errorf("%w: salt length %d", ErrMalformedHash, p.SaltLength)
	case p.KeyLength < MinKeyLen, p.KeyLength > MaxKeyLen:
		return fmt.Errorf("%w: key length %d", ErrMalformedHash, p.KeyLength)
	}
	return nil
}

// PasswordHash is a parsed PHC string.
type PasswordHash struct {
	Params Params
	Salt   []byte
	Key    []byte
}

// String formats the hash as a PHC string. ParsePasswordHash(h.String())
// returns h.
func (h PasswordHash) String() string {
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%sm=%d,t=%d,p=%d$%s$%s", phcPrefix,
		h.Params.MemoryKiB, h.Params.Iterations, h.Params.Parallelism,
		enc.EncodeToString(h.Salt), enc.EncodeToString(h.Key))
}

// ParsePasswordHash parses a PHC Argon2id string, accepting exactly the
// spelling String produces: parameters in m, t, p order, decimal without
// leading zeros, and canonical unpadded base64.
func ParsePasswordHash(s string) (PasswordHash, error) {
	rest, ok := strings.CutPrefix(s, phcPrefix)
	if !ok {
		return PasswordHash{}, fmt.Errorf("%w: not argon2id v=19", ErrMalformedHash)
	}
	parts := strings.Split(rest, "$")
	if len(parts) != 3 {
		return PasswordHash{}, fmt.Errorf("%w: want params$salt$hash", ErrMalformedHash)
	}
	m, t, p, err := parseParams(parts[0])
	if err != nil {
		return PasswordHash{}, err
	}
	salt, err := decodeCanonical(parts[1])
	if err != nil {
		return PasswordHash{}, fmt.Errorf("%w: salt", ErrMalformedHash)
	}
	key, err := decodeCanonical(parts[2])
	if err != nil {
		return PasswordHash{}, fmt.Errorf("%w: hash", ErrMalformedHash)
	}
	h := PasswordHash{
		Params: Params{MemoryKiB: m, Iterations: t, Parallelism: p, SaltLength: uint32(len(salt)), KeyLength: uint32(len(key))},
		Salt:   salt, Key: key,
	}
	if err := h.Params.Validate(); err != nil {
		return PasswordHash{}, err
	}
	return h, nil
}

// decodeCanonical decodes unpadded standard base64 and insists that the
// input is exactly the encoding of the result. Go's decoder skips '\r' and
// '\n' even in Strict mode (found by FuzzParsePasswordHash), so without
// the re-encoding check two spellings of one hash would both be accepted.
func decodeCanonical(s string) ([]byte, error) {
	b, err := base64.RawStdEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, err
	}
	if base64.RawStdEncoding.EncodeToString(b) != s {
		return nil, ErrMalformedHash
	}
	return b, nil
}

func parseParams(s string) (m, t uint32, p uint8, err error) {
	fields := strings.Split(s, ",")
	if len(fields) != 3 {
		return 0, 0, 0, fmt.Errorf("%w: want m,t,p", ErrMalformedHash)
	}
	values := [3]uint64{}
	for i, name := range []string{"m=", "t=", "p="} {
		digits, ok := strings.CutPrefix(fields[i], name)
		if !ok || digits == "" || (len(digits) > 1 && digits[0] == '0') {
			return 0, 0, 0, fmt.Errorf("%w: parameter %d", ErrMalformedHash, i)
		}
		v, perr := strconv.ParseUint(digits, 10, 32)
		if perr != nil {
			return 0, 0, 0, fmt.Errorf("%w: parameter %d", ErrMalformedHash, i)
		}
		values[i] = v
	}
	if values[2] > 255 {
		return 0, 0, 0, fmt.Errorf("%w: parallelism", ErrMalformedHash)
	}
	return uint32(values[0]), uint32(values[1]), uint8(values[2]), nil
}

// Hasher hashes and verifies passwords with one parameter policy.
type Hasher struct {
	params Params
	random io.Reader
	slots  chan struct{}
	dummy  PasswordHash
}

// HasherOptions configure NewHasher. Zero values take the defaults.
type HasherOptions struct {
	Params Params // DefaultParams when zero
	// MaxConcurrent bounds simultaneous hashes (memory: MaxConcurrent x
	// MemoryKiB). Default 4.
	MaxConcurrent int
	Random        io.Reader // crypto/rand.Reader when nil
}

// NewHasher returns a hasher, or an error when the parameters are outside
// the parser's bounds.
func NewHasher(opts HasherOptions) (*Hasher, error) {
	if opts.Params == (Params{}) {
		opts.Params = DefaultParams
	}
	if err := opts.Params.Validate(); err != nil {
		return nil, err
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 4
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	h := &Hasher{params: opts.Params, random: opts.Random, slots: make(chan struct{}, opts.MaxConcurrent)}
	salt := make([]byte, opts.Params.SaltLength)
	if _, err := io.ReadFull(h.random, salt); err != nil {
		return nil, fmt.Errorf("credential: salt: %w", err)
	}
	h.dummy = PasswordHash{Params: h.params, Salt: salt, Key: make([]byte, h.params.KeyLength)}
	return h, nil
}

// Params returns the hasher's parameter policy.
func (h *Hasher) Params() Params { return h.params }

// acquire waits for a hashing slot or for ctx to end.
func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.slots }

func derive(password string, p Params, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLength)
}

// Hash returns the PHC string of password under the hasher's parameters.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if len(password) > MaxPasswordBytes {
		return "", fmt.Errorf("%w: longer than %d bytes", ErrWeakPassword, MaxPasswordBytes)
	}
	salt := make([]byte, h.params.SaltLength)
	if _, err := io.ReadFull(h.random, salt); err != nil {
		return "", fmt.Errorf("credential: salt: %w", err)
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	return PasswordHash{Params: h.params, Salt: salt, Key: derive(password, h.params, salt)}.String(), nil
}

// Verification is the result of Verify.
type Verification struct {
	Match bool
	// NeedsRehash is set on a match whose stored parameters differ from
	// the hasher's: the caller should store Hash(password) now, while it
	// has the plaintext.
	NeedsRehash bool
}

// Verify checks password against a stored PHC string in constant time
// with respect to the key bytes.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (Verification, error) {
	stored, err := ParsePasswordHash(encoded)
	if err != nil {
		return Verification{}, err
	}
	if len(password) > MaxPasswordBytes {
		return Verification{}, nil
	}
	if err := h.acquire(ctx); err != nil {
		return Verification{}, err
	}
	defer h.release()
	got := derive(password, stored.Params, stored.Salt)
	if subtle.ConstantTimeCompare(got, stored.Key) != 1 {
		return Verification{}, nil
	}
	return Verification{Match: true, NeedsRehash: stored.Params != h.params}, nil
}

// VerifyDummy spends the same work as Verify against a hash that matches
// nothing, for sign-ins with an unknown email or an account without a
// password, so timing does not reveal which.
func (h *Hasher) VerifyDummy(ctx context.Context, password string) error {
	if err := h.acquire(ctx); err != nil {
		return err
	}
	defer h.release()
	got := derive(password, h.dummy.Params, h.dummy.Salt)
	subtle.ConstantTimeCompare(got, h.dummy.Key)
	return nil
}
