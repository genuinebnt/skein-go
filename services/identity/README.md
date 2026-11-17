# identity

Accounts, sessions, workspaces, memberships, note roles, share links,
replica ownership and the `Authorize` check ([ADR-0008](../../docs/adr/0008-identity-and-roles.md)).
It implements `IdentityService` from
[`proto/skein/v1/identity.proto`](../../proto/skein/v1/identity.proto) as
documented in [`docs/api/identity.md`](../../docs/api/identity.md).

```
cmd/identity/          the service: gRPC API, admin HTTP, outbox relay, documents.events consumer
cmd/identity-seed/     loads identity's share of a seed dataset
internal/domain/       entities and the access policy (pure; no I/O, no clocks)
internal/credential/   Argon2id password hashing, opaque tokens (both parsers fuzzed)
internal/keys/         signing keyring and rotation
internal/app/          use cases and the ports they need
internal/adapters/     grpc, postgres, redis, pubsub, events, oauth, admin
internal/fakes/        in-memory store, OAuth provider, clock, issuer
internal/storetest/    contract tests every Store must pass (fake: unit; Postgres: integration)
internal/seed/         the seed loader
migrations/            the identity schema
```

## Running

```
. ~/.skein-env
cd services/identity
go test -race ./...                                    # unit tests: no Docker, no network
go test -tags integration ./...                        # adds Postgres via testcontainers (needs Docker)
go test -run='^$' -fuzz=FuzzParsePasswordHash ./internal/credential
go test -run='^$' -fuzz=FuzzParseToken ./internal/credential
go test -run='^$' -fuzz=FuzzDecodeNoteEvent ./internal/adapters/events

# Local stack (deploy/compose/services/identity.yml):
DATABASE_URL=postgres://identity:identity@localhost:5432/skein?sslmode=disable \
  go run ./services/identity/cmd/identity-seed -dir testdata/seed/small
```

## Ports

| Port | What |
|---|---|
| `IDENTITY_GRPC_ADDR` (`:9090`) | `skein.v1.IdentityService`, gRPC health, reflection in dev and test |
| `IDENTITY_ADMIN_ADDR` (`:8080`) | admin HTTP, below; not routed by the gateway |

### Public key set (JWKS)

`GET /.well-known/jwks.json` on the admin port returns every published
signing key as an RFC 7517 key set (Ed25519, `kid` = RFC 7638 thumbprint,
the shape `platform/auth.ParseJWKS` reads). Other services verify access
tokens with it:

```go
keys := auth.NewRemoteKeySet(func(ctx context.Context) ([]byte, error) {
	// GET http://identity:8080/.well-known/jwks.json
}, auth.RemoteKeySetOptions{})
verifier := auth.NewVerifier(keys, auth.VerifierOptions{})
```

The gateway reads it from `IDENTITY_JWKS_URL` (default
`http://identity:8080/.well-known/jwks.json`); ports follow the backend
convention, gRPC on 9090 and admin HTTP on 8080. The gateway also forwards
`x-skein-user-id`; identity ignores it and takes the caller only from the
verified bearer token, so a forged header cannot change who is acting.

The response carries `Cache-Control: public, max-age=300`: verifiers may
cache it for 5 minutes (C in the rotation procedure). `RemoteKeySet` also
refetches at once when it meets an unknown `kid`. `GET /livez` and
`GET /readyz` (database reachable) are the probes. With
`IDENTITY_DEV_SESSIONS=true` (refused outside dev and test) the admin port
also serves `POST /dev/sessions` with `{"userId": "..."}`, answering a
`SignInResponse` for that user: seed users have no passwords, so this is how
the end-to-end suite signs in as them. The gateway never routes the admin
port; `identity healthcheck`
runs the readiness probe from inside the distroless image.

### Key rotation

Keys live in the secret `IDENTITY_SIGNING_KEYS`: comma-separated
`state:seed` entries, where `seed` is a 32-byte Ed25519 seed in unpadded
base64url and `state` is `active` (exactly one; signs), `staged` (published,
not signing yet) or `retiring` (published, no longer signing). One deploy
per step, as in `platform/auth`:

| Step | `IDENTITY_SIGNING_KEYS` | Wait before the next step |
|---|---|---|
| 0 | `active:A` | |
| 1 | `active:A,staged:B` | C = 5 minutes, so every verifier has B |
| 2 | `active:B,retiring:A` | T + leeway = 15 min + 30 s, so tokens signed by A have expired |
| 3 | `active:B` | |

Generate a seed with `head -c32 /dev/urandom | basenc --base64url | tr -d =`.
In dev and test, with the variable unset, a key is generated at startup
(tokens do not survive a restart), and `IDENTITY_KEY_ROTATION_INTERVAL`
runs the same procedure on a timer. That is correct only for one replica,
so it is refused when keys come from the secret or outside dev.

## Configuration

Platform variables: `SKEIN_ENV`, `SKEIN_VERSION`, `DATABASE_URL`,
`DATABASE_MAX_CONNS`, `REDIS_URL`, `PUBSUB_PROJECT`, `PUBSUB_EMULATOR_HOST`,
`OTEL_*` ([docs/platform.md](../../docs/platform.md)).

| Variable | Default | Meaning |
|---|---|---|
| `IDENTITY_SIGNING_KEYS` | generated in dev/test; required otherwise | secret; see Key rotation |
| `IDENTITY_KEY_ROTATION_INTERVAL` | `0` (off) | dev only |
| `IDENTITY_ACCESS_TOKEN_TTL` | `15m` | access token lifetime |
| `IDENTITY_REFRESH_TOKEN_TTL` | `720h` | each refresh token's lifetime |
| `IDENTITY_DELEGATED_TOKEN_MAX_TTL` | `15m` | cap on agent-run tokens |
| `IDENTITY_SHARE_LINK_DEFAULT_TTL` | `168h` | expiry of links created without one; `0` = never expire |
| `IDENTITY_DECISION_CACHE_TTL` | `30s` | Authorize cache entry lifetime |
| `IDENTITY_ARGON2_MEMORY_KIB` / `_ITERATIONS` / `_PARALLELISM` | `65536` / `3` / `4` | Argon2id parameters |
| `IDENTITY_ARGON2_MAX_CONCURRENT` | `4` | hashes at once (memory = this × 64 MiB) |
| `IDENTITY_OAUTH_REDIRECT_URIS` | none | the only redirect URIs an OAuth flow may use (exact match) |
| `IDENTITY_GOOGLE_CLIENT_ID`, `IDENTITY_GOOGLE_CLIENT_SECRET` | unset | enables Google (secret required with the ID) |
| `IDENTITY_GITHUB_CLIENT_ID`, `IDENTITY_GITHUB_CLIENT_SECRET` | unset | enables GitHub |
| `IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS` | `false` (`true` in the local stack) | lets token-less callers use Authorize, ListReadableNotes, GetUser and ListMembers (see Decisions) |
| `IDENTITY_FIXED_NOW` | unset | dev/test only: fix the clock (set to a seed's `reference_time`) |
| `IDENTITY_DEV_SESSIONS` | `false` | dev/test only (startup fails otherwise): serves `POST /dev/sessions` on the admin port, which signs in any existing user without a credential, for end-to-end tests of seed users ([docs/local-native.md](../../docs/local-native.md)) |

## Design

### Passwords

Argon2id in PHC format, `m=65536,t=3,p=4`, 16-byte salt, 32-byte key: RFC
9106's second recommended option (`internal/credential`). The parameters
are stored in each hash; when the configured ones differ, sign-in rehashes
the password it has just verified. Verification compares in constant time,
and an unknown email (or an OAuth-only account) is verified against a dummy
hash with the same parameters, so timing does not reveal whether an account
exists. Every sign-in failure is the same `UNAUTHENTICATED` "authentication
failed". Passwords need 10 code points, 4 distinct characters, not a common
password and not containing the email's local part (NIST SP 800-63B, no
composition rules). The hash is checked against libargon2's output in tests.

### Sessions

Access tokens are Ed25519 JWTs from `platform/auth` (15 minutes). A session
is one sign-in plus its chain of opaque refresh tokens (`rt_` + 256 random
bits; only the SHA-256 is stored). Refresh rotates: the presented token is
marked rotated and a new one issued, under a row lock so concurrent
refreshes of one token serialise. Presenting a rotated token again means it
was copied: the whole session is revoked (`refresh_token_reuse`) and both
holders must sign in again. Sign-out revokes the session whichever token of
the chain is presented, and succeeds on a revoked session. Access tokens are
not revoked; they expire within minutes.

### OAuth

Authorization code with PKCE: the browser sends the code and its verifier,
identity forwards both with the redirect URI to the provider's token
endpoint (`internal/adapters/oauth`), then reads the profile. Accounts are
matched by the provider's stable subject; otherwise by a provider-verified
email. If that email's account was never verified, its password is removed
and its sessions revoked before linking (defeats account pre-hijacking).

### Authorization

`domain.Decide` is the policy from `docs/seed-data.md` and
`tools/seed/internal/policy`, rule for rule (members edit by default,
overrides can lower but not raise, owners and admins ignore overrides,
guests need explicit grants, removed members lose access and their
overrides, only owners and admins see trashed notes). Every row of
`testdata/seed/small/truth/permissions/matrix.jsonl` (6,256) is a test:
against the pure policy, through `Authorize` after loading the seed (with
and without the cache), and against ListReadableNotes. Share links never
grant editor: creation refuses it and the policy clamps stored ones.

Identity needs note metadata it does not own (workspace, creator, trash
state). It keeps a projection fed by `documents.events` (`NoteCreated`,
`NoteTrashed`, `NoteRestored`, `NotePurged`) on the
`identity.documents-events` subscription, consumed idempotently. Because
that event arrives a moment after the note exists, documents also calls the
internal `RegisterNote` right after creating a note, with the creator's
token: identity checks the caller may create notes in the workspace and
inserts the projection row if it is missing (never moving a known note), so
the creator and the workspace can open the note at once.

### Caching and rate limits

Authorize decisions are cached in Redis per (subject, action, resource)
for 30 seconds, tagged with the user, note and workspace. Every access
change invalidates its tags after commit; note events invalidate the note's
tag. Decisions about unknown notes are never cached (the projection may be
behind). Cache and limiter failures fail open, as everywhere in
`platform/`. Sign-in is limited per account (5/min, burst 10) and per
client IP (30/min, burst 60) with GCRA in Redis, before any hashing.

### Events

Every role, membership, override and share-link change writes an
`AccessChanged` to the outbox in the same transaction; the relay publishes
it to `identity.events` with the workspace ID as ordering key. `revoked`
is true when access went down (removal, demotion, a lowered or cleared
override, a revoked link), which tells consumers to close streams as well
as drop caches.

### Schema

`migrations/0001_identity.sql`: `users`, `oauth_identities`, `sessions`,
`refresh_tokens`, `workspaces`, `memberships` (one row per pair,
`removed_at`), `notes` (projection), `note_role_overrides`, `share_links`,
`replicas` (uint64 stored as bigint bits), `one_time_tokens`, `outbox`,
`processed_events`.

## Decisions to confirm

- **Share-link default expiry** (confirmed). Links created without
  `expires_at` expire after 7 days; `identity.proto` and
  `docs/api/identity.md` say so. `IDENTITY_SHARE_LINK_DEFAULT_TTL=0` makes
  them last until revoked.
- **Internal RPCs without a token.** `Authorize` and `ListReadableNotes`
  require a token by default, and a caller may only ask about itself. Event
  consumers (notifications deciding whether a mentioned user can read a
  note, resolving `@handles` to members, reading names and addresses) have
  no user token, so `IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS` opens
  `Authorize`, `ListReadableNotes`, `GetUser` and `ListMembers` to
  token-less callers until services have their own identity. The local
  stack turns it on (without it notifications dead-letters every mention);
  the gateway authenticates every route, so token-less calls only come from
  inside the cluster.
- **IssueDelegatedToken** cannot tell that the agent service is calling
  (there is no service identity yet). It requires the user's own,
  non-delegated token and only mints tokens for that user, so the token
  grants nothing the caller does not already have.
- **The note projection in the seed loader.** The loader fills the note
  projection from `notes.jsonl` so identity can be loaded and checked
  alone; it is keyed by note ID, so later documents events agree with it.

## Contract follow-ups

- Email verification and password reset are implemented as use cases
  (`RequestEmailVerification`, `VerifyEmail`, `RequestPasswordReset`,
  `ResetPassword`) with hashed single-use tokens, but `identity.proto` has
  no RPCs for them, and delivery needs a notifications request kind.
- Share-link holders cannot be authorized through the API: `Subject` has
  no share-link field and there is no RPC to redeem a token.
  `ResolveShareLink` and the link branch of the policy are ready for one.
- The `identity.documents-events` subscription was added to
  `deploy/compose/pubsub-init.sh`.
