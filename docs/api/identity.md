# Identity

`IdentityService` ([`identity.proto`](../../proto/skein/v1/identity.proto)):
accounts, sessions, workspaces, members, note roles, share links and replica
registration ([ADR-0008](../adr/0008-identity-and-roles.md)). Conventions
(JSON, errors, headers) are in [README.md](README.md).

| Method | Path | RPC | Auth |
|---|---|---|---|
| `POST` | `/v1/users` | SignUp | none |
| `POST` | `/v1/sessions` | SignIn | none |
| `POST` | `/v1/sessions:oauth` | SignInWithOAuth | none |
| `POST` | `/v1/sessions:refresh` | Refresh | none (refresh token) |
| `POST` | `/v1/sessions:signOut` | SignOut | none (refresh token) |
| `GET` | `/v1/users/{userId}` | GetUser | bearer |
| `POST` | `/v1/workspaces` | CreateWorkspace | bearer |
| `GET` | `/v1/workspaces/{workspaceId}/members` | ListMembers | bearer |
| `PUT` | `/v1/workspaces/{workspaceId}/members/{userId}` | SetWorkspaceRole | bearer, admin |
| `DELETE` | `/v1/workspaces/{workspaceId}/members/{userId}` | RemoveMember | bearer, admin |
| `PUT` | `/v1/notes/{noteId}/roles/{userId}` | SetNoteRole | bearer, may share |
| `POST` | `/v1/notes/{noteId}/share-links` | CreateShareLink | bearer, may share |
| `DELETE` | `/v1/share-links/{linkId}` | RevokeShareLink | bearer, may share |
| `POST` | `/v1/replicas` | RegisterReplica | bearer |

Not exposed: `Authorize`, `ListReadableNotes`, `IssueDelegatedToken` and `RegisterNote` are
internal RPCs that only other services call.

## Sessions

### SignUp

`POST /v1/users`. Creates an account and signs it in. An email already in
use is `ALREADY_EXISTS`.

```json skein.v1.SignUpRequest
{
  "email": "ada@example.com",
  "password": "correct horse battery staple",
  "displayName": "Ada Lovelace"
}
```

```json skein.v1.SignUpResponse
{
  "user": {
    "id": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
    "email": "ada@example.com",
    "displayName": "Ada Lovelace",
    "createdAt": "2026-11-16T09:00:00Z"
  },
  "session": {
    "accessToken": "EXAMPLE_ACCESS_TOKEN",
    "accessExpiresAt": "2026-11-16T09:15:00Z",
    "refreshToken": "rt_9mQ2xV7kLp3sT8wZ",
    "refreshExpiresAt": "2026-12-16T09:00:00Z"
  }
}
```

### SignIn

`POST /v1/sessions`. Wrong email or password is `UNAUTHENTICATED`, with the
same message for both.

```json skein.v1.SignInRequest
{"email": "ada@example.com", "password": "correct horse battery staple"}
```

```json skein.v1.SignInResponse
{
  "user": {
    "id": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
    "email": "ada@example.com",
    "displayName": "Ada Lovelace",
    "createdAt": "2026-11-16T09:00:00Z"
  },
  "session": {
    "accessToken": "EXAMPLE_ACCESS_TOKEN",
    "accessExpiresAt": "2026-11-16T09:15:00Z",
    "refreshToken": "rt_9mQ2xV7kLp3sT8wZ",
    "refreshExpiresAt": "2026-12-16T09:00:00Z"
  }
}
```

### SignInWithOAuth

`POST /v1/sessions:oauth`. Completes an OAuth authorization-code flow with
PKCE: the browser sends the provider's code and its verifier. `created` is
true when this sign-in created the account.

```json skein.v1.SignInWithOAuthRequest
{
  "provider": "O_AUTH_PROVIDER_GITHUB",
  "code": "4f2a9c1e7b3d",
  "codeVerifier": "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk",
  "redirectUri": "https://skein.example.com/auth/callback"
}
```

```json skein.v1.SignInWithOAuthResponse
{
  "user": {
    "id": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
    "email": "ada@example.com",
    "displayName": "Ada Lovelace",
    "createdAt": "2026-11-16T09:00:00Z"
  },
  "session": {
    "accessToken": "EXAMPLE_ACCESS_TOKEN",
    "accessExpiresAt": "2026-11-16T09:15:00Z",
    "refreshToken": "rt_9mQ2xV7kLp3sT8wZ",
    "refreshExpiresAt": "2026-12-16T09:00:00Z"
  },
  "created": true
}
```

### Refresh

`POST /v1/sessions:refresh`. Rotates the refresh token: the old one stops
working. Presenting an already-rotated token is treated as theft and revokes
the session; the user must sign in again.

```json skein.v1.RefreshRequest
{"refreshToken": "rt_9mQ2xV7kLp3sT8wZ"}
```

```json skein.v1.RefreshResponse
{
  "session": {
    "accessToken": "EXAMPLE_ACCESS_TOKEN",
    "accessExpiresAt": "2026-11-16T09:30:00Z",
    "refreshToken": "rt_Hq8sZ3vB1mX6nK4p",
    "refreshExpiresAt": "2026-12-16T09:15:00Z"
  }
}
```

### SignOut

`POST /v1/sessions:signOut`. Revokes the session. Signing out an already
revoked session succeeds.

```json skein.v1.SignOutRequest
{"refreshToken": "rt_Hq8sZ3vB1mX6nK4p"}
```

```json skein.v1.SignOutResponse
{}
```

## Users

### GetUser

`GET /v1/users/{userId}`. Any user who shares a workspace with the caller;
others are `NOT_FOUND`.

```
GET /v1/users/01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90
```

```json skein.v1.GetUserResponse
{
  "user": {
    "id": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
    "email": "ada@example.com",
    "displayName": "Ada Lovelace",
    "createdAt": "2026-11-16T09:00:00Z"
  }
}
```

## Workspaces and members

### CreateWorkspace

`POST /v1/workspaces`. The caller becomes its owner. Send an
`Idempotency-Key`.

```json skein.v1.CreateWorkspaceRequest
{"name": "Research"}
```

```json skein.v1.CreateWorkspaceResponse
{"workspaceId": "01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f"}
```

### ListMembers

`GET /v1/workspaces/{workspaceId}/members`. Paginated.

```
GET /v1/workspaces/01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f/members?page.pageSize=2
```

```json skein.v1.ListMembersResponse
{
  "members": [
    {
      "user": {
        "id": "01936f3e-8c4a-7b2e-9f10-3d5a6b7c8d90",
        "email": "ada@example.com",
        "displayName": "Ada Lovelace",
        "createdAt": "2026-11-16T09:00:00Z"
      },
      "role": "WORKSPACE_ROLE_OWNER"
    },
    {
      "user": {
        "id": "01936f3f-9d5b-7c3f-8a21-4e6b7c8d9ea1",
        "email": "grace@example.com",
        "displayName": "Grace Hopper",
        "createdAt": "2026-11-16T09:05:00Z"
      },
      "role": "WORKSPACE_ROLE_MEMBER"
    }
  ],
  "page": {"nextPageToken": "CgwIgICAgICAgICAARIA"}
}
```

### SetWorkspaceRole

`PUT /v1/workspaces/{workspaceId}/members/{userId}`. Adds the user as a
member with the role, or changes their role. Only admins and owners may call
it, and only an owner can grant or remove `WORKSPACE_ROLE_OWNER`; a
workspace always keeps at least one owner (`FAILED_PRECONDITION`).

```json skein.v1.SetWorkspaceRoleRequest
{"role": "WORKSPACE_ROLE_ADMIN"}
```

```json skein.v1.SetWorkspaceRoleResponse
{}
```

### RemoveMember

`DELETE /v1/workspaces/{workspaceId}/members/{userId}`. Their open sync
connections to the workspace's notes are closed with `4403`.

```
DELETE /v1/workspaces/01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f/members/01936f3f-9d5b-7c3f-8a21-4e6b7c8d9ea1
```

```json skein.v1.RemoveMemberResponse
{}
```

## Note access

### SetNoteRole

`PUT /v1/notes/{noteId}/roles/{userId}`. Overrides the user's workspace role
on one note. Omitting `role` (or sending `NOTE_ROLE_UNSPECIFIED`) removes the
override, so `{}` is a valid body.

```json skein.v1.SetNoteRoleRequest
{"role": "NOTE_ROLE_COMMENTER"}
```

```json skein.v1.SetNoteRoleResponse
{}
```

### CreateShareLink

`POST /v1/notes/{noteId}/share-links`. The `token` is returned only here;
the server keeps a hash. Without `expiresAt` the link lasts until revoked.

```json skein.v1.CreateShareLinkRequest
{"role": "NOTE_ROLE_VIEWER", "expiresAt": "2026-12-01T00:00:00Z"}
```

```json skein.v1.CreateShareLinkResponse
{
  "linkId": "01936f70-4d5e-7f60-8a71-8c9d0e1f2031",
  "token": "sl_Vb4nR8tYq2Kd6Hj1"
}
```

### RevokeShareLink

`DELETE /v1/share-links/{linkId}`.

```
DELETE /v1/share-links/01936f70-4d5e-7f60-8a71-8c9d0e1f2031
```

```json skein.v1.RevokeShareLinkResponse
{}
```

## Replicas

### RegisterReplica

`POST /v1/replicas`. Each device mints a random 64-bit replica ID, keeps it
in IndexedDB ([ADR-0004](../adr/0004-identifiers.md)) and registers it here
before its first sync. Registering an ID the caller already owns succeeds;
one owned by someone else is `ALREADY_EXISTS`, and the device must mint a new
one. The ID is a JSON string.

```json skein.v1.RegisterReplicaRequest
{"replicaId": "4831727201934127107"}
```

```json skein.v1.RegisterReplicaResponse
{}
```
