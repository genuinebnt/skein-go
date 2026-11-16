# Notification conformance fixtures

Behaviour of `services/notifications/internal/domain` that a port must
reproduce exactly. Each file is a JSON list of cases with a `why`.

- `mentions.json`: `ExtractHandles`, the @mention grammar for block text:
  `text` in, the distinct lowercase `handles` out, ascending.
- `email_plans.json`: `PlanEmails`, how pending email jobs become plain
  emails and digests: `pending` and `now` in, `batches` (job IDs per email,
  in order) and `skipped` out.

The Go tests load them in `internal/domain/conformance_test.go`.
