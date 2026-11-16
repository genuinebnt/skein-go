// Package auth issues and verifies Skein access tokens: short-lived,
// Ed25519-signed JWTs that identify a user, or an agent run acting for a
// user (ADR-0008, ADR-0009).
//
// # Why JWT with EdDSA, not PASETO v4.public
//
// Both sign with Ed25519, so the cryptography is the same. PASETO's
// advantage is that its header cannot select an algorithm, which rules out
// algorithm confusion by construction. JWT gets the same property here by
// policy, and that policy is tested:
//
//   - the only accepted "alg" is "EdDSA"; "none", HS256 (a classic attack
//     that uses the public key as an HMAC secret) and every other value are
//     rejected before any key is looked up;
//   - each key ID maps to exactly one Ed25519 public key, never a key of
//     another type;
//   - "typ" must be "at+jwt" (RFC 9068), so an ID token or some other JWT
//     signed by the same key cannot be replayed as an access token.
//
// In exchange JWT brings what PASETO lacks: JWKS (RFC 7517) is the standard
// shape for publishing public keys and rotating them, and mature libraries
// exist in all three languages that touch these tokens: golang-jwt here,
// PyJWT in the Python agent service, `jsonwebtoken` (with EdDSA) in the
// Rust port. PASETO libraries are thinner in Python and Rust, and key
// distribution would be bespoke.
//
// # Claims
//
//	iss            the issuer, "skein-identity"
//	sub            the user ID (UUIDv7)
//	aud            audiences; every Skein service accepts "skein"
//	exp, nbf, iat  validity window; verifiers allow a small leeway for skew
//	jti            a unique token ID (UUIDv7), for audit and revocation lists
//	agent_run_id   set only on delegated tokens: the agent run acting for sub
//
// A delegated token carries the user's identity, so every permission check
// is the user's own; agent_run_id only marks who is acting, for audit,
// history and confirmation gates.
//
// # Keys and rotation
//
// A key's ID is its RFC 7638 JWK thumbprint, so IDs are derived, never
// typed by hand, and cannot collide between different keys. Rotation, with
// T the longest token lifetime and C the verifiers' JWKS cache period:
//
//  1. Generate the new key and add its public half to the published key
//     set. Keep signing with the old key.
//  2. After C, every verifier has the new key. Start signing with it.
//  3. After T plus the leeway, no unexpired token uses the old key. Remove it
//     from the key set.
//
// RemoteKeySet also refetches immediately (rate-limited) when it sees an
// unknown key ID, so a verifier that missed step 1 catches up on the first
// token signed with the new key.
//
// # Porting notes
//
// Rust: `jsonwebtoken` with `Algorithm::EdDSA` and `Validation` pinned to
// that one algorithm; `ed25519-dalek` for key generation. The sentinel
// errors become one `thiserror` enum.
package auth
