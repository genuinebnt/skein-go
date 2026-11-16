# Notifications

`NotificationService` ([`notifications.proto`](../../proto/skein/v1/notifications.proto)):
the caller's inbox and notification preferences. Every route acts on the
caller's own data, so none takes a user ID. Live delivery is the
`/v1/notifications/live` WebSocket ([websocket.md](websocket.md#notificationservicesubscribe)).
Conventions are in [README.md](README.md).

| Method | Path | RPC |
|---|---|---|
| `GET` | `/v1/notifications` | ListInbox |
| `POST` | `/v1/notifications:markRead` | MarkRead |
| `GET` | `/v1/notification-preferences` | GetPreferences |
| `PUT` | `/v1/notification-preferences` | UpdatePreferences |

### ListInbox

`GET /v1/notifications`. Newest first. Paginated; `unreadOnly=true` skips
read notifications. `unread` is the inbox's total unread count, for the
badge, whatever the page.

```
GET /v1/notifications?unreadOnly=true&page.pageSize=20
```

```json skein.v1.ListInboxResponse
{
  "notifications": [
    {
      "id": "01936f60-5e6f-7a80-9b92-0d1e2f304152",
      "kind": "NOTIFICATION_KIND_MENTION",
      "title": "Grace mentioned you in Raft log compaction",
      "body": "@Ada can you check the snapshot threshold?",
      "location": {
        "noteId": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
        "blockId": "9120384756102938475:3"
      },
      "actorId": "01936f3f-9d5b-7c3f-8a21-4e6b7c8d9ea1",
      "createdAt": "2026-11-16T09:30:00Z"
    },
    {
      "id": "01936f61-6f70-7b81-8ca3-1e2f30415263",
      "kind": "NOTIFICATION_KIND_AGENT_RUN_FINISHED",
      "title": "The agent finished: This week's Raft changes",
      "createdAt": "2026-11-16T10:05:00Z"
    }
  ],
  "page": {},
  "unread": 2
}
```

### MarkRead

`POST /v1/notifications:markRead`. IDs that are already read, or not the
caller's, are ignored, so the call is idempotent.

```json skein.v1.MarkReadRequest
{"ids": ["01936f60-5e6f-7a80-9b92-0d1e2f304152", "01936f61-6f70-7b81-8ca3-1e2f30415263"]}
```

```json skein.v1.MarkReadResponse
{}
```

### GetPreferences

`GET /v1/notification-preferences`. One entry per notification kind:
`inApp` shows it in the inbox and pushes it live, `email` also sends an
email.

```
GET /v1/notification-preferences
```

```json skein.v1.GetPreferencesResponse
{
  "preferences": [
    {"kind": "NOTIFICATION_KIND_MENTION", "inApp": true, "email": true},
    {"kind": "NOTIFICATION_KIND_COMMENT", "inApp": true},
    {"kind": "NOTIFICATION_KIND_SHARE", "inApp": true, "email": true},
    {"kind": "NOTIFICATION_KIND_AGENT_RUN_FINISHED", "inApp": true},
    {"kind": "NOTIFICATION_KIND_ACCESS_CHANGED", "inApp": true, "email": true}
  ]
}
```

### UpdatePreferences

`PUT /v1/notification-preferences`. Sets the listed kinds; kinds not listed
keep their current setting. Listing a kind twice is `INVALID_ARGUMENT`.

```json skein.v1.UpdatePreferencesRequest
{
  "preferences": [
    {"kind": "NOTIFICATION_KIND_COMMENT", "inApp": true, "email": true},
    {"kind": "NOTIFICATION_KIND_AGENT_RUN_FINISHED"}
  ]
}
```

```json skein.v1.UpdatePreferencesResponse
{}
```
