// Package credential hashes passwords and mints, parses and hashes the
// opaque bearer secrets identity hands out (refresh tokens, share-link
// tokens, email verification and password reset tokens). Both parsers read
// attacker-supplied or database-supplied text and are fuzzed.
//
// # Passwords: Argon2id
//
// Passwords are hashed with Argon2id (RFC 9106) and stored in the PHC
// string format that libsodium, argon2-cffi and Rust's `argon2` crate all
// read:
//
//	$argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<hash>
//
// with salt and hash in unpadded standard base64. DefaultParams follows
// RFC 9106 section 4's second recommended option for memory-constrained
// servers, m = 64 MiB, t = 3, p = 4, with a 16-byte salt and a 32-byte key:
// about 60 to 150 ms per hash on one modern core. Every parameter is in the
// stored string, so they can be raised later without breaking existing
// hashes: Verify reports NeedsRehash when a stored hash's parameters differ
// from the hasher's, and the sign-in use case rehashes the password it has
// just verified. Lowering parameters also triggers a rehash; the configured
// parameters are the policy, in both directions.
//
// Verification recomputes the key with the stored parameters and compares
// with crypto/subtle, so the time taken does not depend on how many leading
// bytes match. Sign-in also verifies against a dummy hash when the email is
// unknown (VerifyDummy), so the response time does not reveal whether an
// account exists.
//
// Each hash holds 64 MiB for its duration. A semaphore (MaxConcurrent)
// bounds how many run at once, so a burst of sign-ins queues instead of
// exhausting memory; per-account and per-IP rate limits sit in front.
//
// The parser bounds every parameter (MaxMemoryKiB and friends). The stored
// string comes from our own database, but a corrupted or malicious row must
// not be able to make Verify allocate gigabytes or loop for minutes.
//
// # Opaque tokens
//
// A token is a kind prefix plus 32 random bytes in unpadded base64url:
// "rt_" refresh, "sl_" share link, "ev_" email verification, "pr_" password
// reset. The prefix lets secret scanners and log redaction recognise them
// and stops a token of one kind being accepted as another. Only
// HashToken(token), the lowercase hex SHA-256 of the whole token string, is
// stored; the seed's token_sha256 uses the same scheme. A plain hash (not a
// slow one) is right here because the token has 256 bits of entropy: there
// is nothing to brute-force.
//
// # Porting notes
//
// Rust: the `argon2` crate's `PasswordHash` parses the same PHC strings
// (its bounds differ, so keep these explicit checks); `subtle` for the
// comparison; `base64` with `URL_SAFE_NO_PAD` and `STANDARD_NO_PAD`, which
// reject non-canonical trailing bits like Go's Strict decoding.
package credential
