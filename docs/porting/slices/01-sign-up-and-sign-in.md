# Slice 1: sign up and sign in

A new user signs up, gets an access token and a refresh token, signs in
again, refreshes (rotating the refresh token, and revoking the whole session
when an old one is reused), reads their own profile, and signs out. This is
the smallest slice that runs a real Rust service in the stack, and it builds
the base every later slice reuses: the generated crate, the gRPC server
stack, Postgres with the shared migration runner, and token signing.

## The path of a request

```
browser
  POST /v1/users {email, password, displayName}
gateway (Go, or Rust option B for these routes)
  domain.PolicyFor: public route, api budget, no idempotency key
  rate limit per IP (GCRA), deadline, body limit
  grpc-gateway -> IdentityService.SignUp
identity
  adapters/grpc/server.go SignUp  ->  app/sessions.go Service.SignUp
    domain.NormalizeEmail, NormalizeName      (domain/rules.go)
    credential.CheckStrength, Hasher.Hash     (credential/)
    Store.InTx: InsertUser, InsertSession, InsertRefreshToken
    keys.Keyring.Issue -> platform/auth Signer.Issue (Ed25519 JWT)
  <- SignUpResponse {user, session {accessToken, accessExpiresAt, refreshToken, refreshExpiresAt}}
```

## Contracts and storage

- Protos: `proto/skein/v1/identity.proto` (`SignUp`, `SignIn`, `Refresh`,
  `SignOut`, `GetUser` and their messages, `User`, `Session`),
  `common.proto`.
- Migration: `services/identity/migrations/0001_identity.sql`, applied
  whole (it is one file, and the Go service must be able to start on the
  same database). This slice uses `users`, `sessions`, `refresh_tokens`.
- JWKS: `GET /.well-known/jwks.json` on identity's admin port, read by every
  Go service that verifies tokens.

## Files to port, in order

Prerequisites (shared with every later slice):

1. `gen/` as `skein-proto`: messages and the `IdentityService` server
   ([gen/PORTING.md](../../../gen/PORTING.md), steps 1 and 3).
2. Platform ([platform/PORTING.md](../../../platform/PORTING.md)):
   `config/config.go`, `secret.go`, `service.go`; `shutdown/shutdown.go`;
   `logging/logging.go`, `context.go`, `redact.go`; `telemetry/telemetry.go`
   (run with `OTEL_SDK_DISABLED=true` at first); `auth/keys.go`,
   `token.go`, `context.go` (`remote.go` is not needed by identity itself);
   `ratelimit/ratelimit.go`, `tokenbucket.go`, `gcra.go`;
   `postgres/postgres.go`, `migrate.go`; `grpcx/interceptors.go`,
   `server.go`; `httpx/middleware.go`, `errors.go` (for the admin port).

Identity ([services/identity/PORTING.md](../../../services/identity/PORTING.md)):

| # | Go file | What this slice needs from it |
|---|---|---|
| 1 | `internal/domain/ids.go` | `UserID`, `SessionID`, `ParseUUID`, `NewID` |
| 2 | `internal/domain/errors.go` | all of it |
| 3 | `internal/domain/session.go` | `User`, `Session`, `RefreshToken`, `RevokeReason`, `RefreshOutcome`, `DecideRefresh` |
| 4 | `internal/domain/rules.go` | `NormalizeEmail`, `NormalizeName` |
| 5 | `internal/credential/password.go` | all of it |
| 6 | `internal/credential/strength.go` | all of it |
| 7 | `internal/credential/token.go` | the `rt_` kind (port all kinds; it is small) |
| 8 | `internal/keys/keys.go` | `Keyring`, `ParseConfig`, `JWKS`, `Issue` (the `Rotator` can wait) |
| 9 | `internal/app/ports.go` | `Store`, `Tx`, `UserRepository`, `SessionRepository`, the clock and rate-limiter ports |
| 10 | `internal/app/service.go` | `Config`, `Deps`, `Service::new` |
| 11 | `internal/app/sessions.go` | `SignUp`, `startSession`, `issueRefresh`, `SignIn`, `checkSignInLimits`, `Refresh`, `SignOut` |
| 12 | `internal/app/workspaces.go` | `GetUser` (yourself, or someone you share a workspace with) |
| 13 | `internal/fakes/fakes.go`, `store.go` | the clock, issuer and the user and session parts of the store |
| 14 | `internal/storetest/storetest.go` | the user and session cases |
| 15 | `internal/adapters/postgres/store.go` | `NewStore`, `InTx`, `InsertUser`, `UserByID`, `UserByEmail`, `SetPasswordHash`, `InsertSession`, `SessionByIDForUpdate`, `RevokeSession`, `RevokeUserSessions`, `InsertRefreshToken`, `RefreshTokenForUpdate`, `MarkRefreshTokenRotated` |
| 16 | `internal/adapters/grpc/errors.go` | all of it |
| 17 | `internal/adapters/grpc/server.go` | `PublicMethods`, `SignUp`, `SignIn`, `Refresh`, `SignOut`, `GetUser` |
| 18 | `internal/adapters/admin/admin.go` | JWKS, `/livez`, `/readyz` |
| 19 | `cmd/identity/settings.go`, `main.go` | the variables this slice reads, and the wiring |

Gateway: keep the Go gateway for this slice. If you want a Rust edge now,
port `internal/domain/policy.go` (`IsPublicMethod`, `PolicyFor`),
`internal/adapters/edge/errors.go`, and five hand-written axum handlers
(option B in [services/gateway/PORTING.md](../../../services/gateway/PORTING.md))
behind `skein-http`'s chain.

## Tests that prove it

Unit (Rust ports of the Go tests):

- domain: `TestDecideRefreshChecksRevocationThenReuseThenExpiry`,
  `TestNormalizeEmail`, `TestNormalizeNameTrimsAndBoundsInCodePoints`,
  `TestParseUUIDCanonicalisesAndRejectsOtherSpellings`;
- credential: the whole `credential_test.go`, with both fuzz targets;
- keys: `TestPublishedKeySetParsesAndKeyIDsAreThumbprints`,
  `TestParseConfig`;
- app: `TestSignUpThenSignInIssuesASessionForTheUser`,
  `TestSignUpRejectsTakenEmailsAndWeakPasswords`,
  `TestSignInFailuresLookTheSame`, `TestSignInRehashesWhenTheParametersChange`,
  `TestSignInIsRateLimitedPerAccountAndPerIP`,
  `TestRefreshRotatesAndReuseRevokesTheWholeSession`,
  `TestConcurrentRefreshesOfOneTokenLetOnlyOneWin`,
  `TestExpiredAndMalformedRefreshTokensAreRefused`,
  `TestSignOutRevokesTheSessionAndIsIdempotent`;
- gRPC: `TestTokensFromSignUpVerifyAgainstThePublishedKeySet`,
  `TestErrorsCarryTheDocumentedDetails`,
  `TestRateLimitBecomesResourceExhaustedWithRetryInfo`;
- settings: `TestDefaultsInDevNeedNoSecrets`,
  `TestProductionRefusesUnsafeSettings`,
  `TestSigningKeysComeFromTheSecretAndAreNeverEchoed`.

Integration (native Postgres on 25432): `TestMigrationsApplyTwiceWithoutChange`,
the user and session cases of `TestPostgresStoreMeetsTheStoreContract`.

Cross-language: a token issued by Rust identity verifies in a Go service
(any Go service's authenticated RPC accepts it), and `jwks.json` from Rust
parses in Go's `auth.ParseJWKS`.

End to end (`e2e/auth_test.go`): `TestSignUpSignInAndSignOut`,
`TestRefreshRotatesAndReuseRevokesTheSession`.

## Stubbed in this slice

- `SignInWithOAuth`, workspaces, members, note roles, share links,
  `Authorize`, `ListReadableNotes`, `IssueDelegatedToken`, `RegisterNote`,
  `RegisterReplica`: `UNIMPLEMENTED`. Every other e2e journey still needs Go
  identity, so swap Rust identity in only for this slice's journeys, or
  finish [slice 5](05-share-and-rbac.md) first.
- The outbox (session use cases publish nothing), the `documents.events`
  consumer, the decision cache: not wired.
- Email verification, password reset and dev sessions: not wired (the seed
  journeys need dev sessions; they come with the rest of identity).

## Build

```
cd rust
cargo build -p identity
cargo test -p identity-core
cargo test -p identity --features integration   # needs the native Postgres
```
