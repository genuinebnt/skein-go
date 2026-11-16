// Package logging builds the structured JSON logger every Skein service
// uses, on top of log/slog.
//
// # What a record carries
//
// Every record has the service, version and env it was configured with.
// Records logged with a context (InfoContext, ErrorContext, ...) also carry,
// when present:
//
//	trace_id, span_id   from the OpenTelemetry span in the context
//	request_id          set by WithRequestID (grpcx and httpx do this)
//	user_id             set by WithSubject after a token is verified
//	agent_run_id        set by WithSubject for delegated agent tokens
//
// so a log line can be joined to its trace in Jaeger or Cloud Trace and to
// every other line of the same request. Always log with a context; the
// plain Info/Error methods lose all of these.
//
// # Redaction
//
// Attribute keys are split into words on '_', '-', '.' and case changes,
// and an attribute is replaced by "[REDACTED]" when any word is sensitive
// (password, secret, token, authorization, cookie, credential, apikey) or
// the key contains "api_key" or "private_key". So "password",
// "access_token" and "Authorization" are redacted, while "input_tokens" is
// not: whole words are matched, not substrings, to avoid hiding useful
// numbers. Redaction happens in this package's handler, before any sink,
// so it covers the JSON output and every extra sink (the OpenTelemetry log
// bridge) alike. config.Secret values redact themselves regardless of key.
//
// Redaction is a safety net, not a licence: never pass note contents,
// passwords or tokens to a logger in the first place.
//
// # Request IDs
//
// NewRequestID makes a UUIDv7 (time-ordered, so IDs sort by arrival).
// Incoming IDs from clients are accepted only when ValidRequestID holds:
// 1 to 128 characters from [A-Za-z0-9._:-]. Anything else is replaced, which
// keeps attacker-controlled bytes (newlines, escapes, megabyte headers) out
// of logs.
//
// # Porting notes
//
// Rust: `tracing` with `tracing-subscriber`'s JSON layer; the context fields
// become span fields on the request span, and redaction is a custom
// `Layer`. Context keys become task-local or span extensions.
package logging
