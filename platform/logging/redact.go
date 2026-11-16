package logging

import (
	"log/slog"
	"strings"
	"unicode"
)

// Redacted replaces the value of every sensitive attribute.
const Redacted = "[REDACTED]"

// sensitiveWords are matched against whole words of a key.
var sensitiveWords = map[string]bool{
	"password": true, "passwd": true, "secret": true, "token": true,
	"authorization": true, "cookie": true, "credential": true,
	"credentials": true, "apikey": true, "jwt": true,
}

// sensitivePhrases are matched as substrings of the normalised key, because
// they are two words that are only sensitive together.
var sensitivePhrases = []string{"api_key", "private_key", "set_cookie"}

// IsSensitiveKey reports whether an attribute with this key is redacted.
// Exported so other sinks (HTTP header dumps, error details) can share the
// rule.
func IsSensitiveKey(key string) bool {
	normalised := normaliseKey(key)
	for _, phrase := range sensitivePhrases {
		if strings.Contains(normalised, phrase) {
			return true
		}
	}
	for word := range strings.SplitSeq(normalised, "_") {
		if sensitiveWords[word] {
			return true
		}
	}
	return false
}

// normaliseKey lowercases key and turns separators and camelCase boundaries
// into '_', so "accessToken", "access-token" and "ACCESS_TOKEN" all become
// "access_token". Word boundaries are found only between ASCII letters and
// digits: attribute keys are ASCII identifiers, and some Unicode letters
// count as upper case yet have no lower-case form, which would make the
// normalisation of a normalised key differ (found by FuzzIsSensitiveKey).
func normaliseKey(key string) string {
	var b strings.Builder
	b.Grow(len(key) + 4)
	var prev rune
	for i, r := range key {
		switch {
		case r == '-' || r == '.' || r == ' ' || r == '_':
			b.WriteByte('_')
		case r >= 'A' && r <= 'Z':
			if i > 0 && ((prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9')) {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteRune(unicode.ToLower(r))
		}
		prev = r
	}
	return b.String()
}

// redactAttr returns a with sensitive values replaced, descending into
// groups. LogValuers are resolved first so a valuer cannot smuggle a
// sensitive group past the check.
func redactAttr(a slog.Attr) slog.Attr {
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	v := a.Value.Resolve()
	if v.Kind() != slog.KindGroup {
		return slog.Attr{Key: a.Key, Value: v}
	}
	group := v.Group()
	out := make([]slog.Attr, len(group))
	for i, member := range group {
		out[i] = redactAttr(member)
	}
	return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
}

func redactAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = redactAttr(a)
	}
	return out
}
