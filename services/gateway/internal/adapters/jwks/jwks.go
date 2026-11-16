// Package jwks fetches identity's public key set for token verification.
//
// identity.proto has no RPC for the key set, so identity publishes it as a
// JWKS document over HTTP (RFC 7517), by default at
// http://identity:8080/.well-known/jwks.json. Fetch returns a
// platform/auth.FetchFunc; auth.RemoteKeySet caches the result, refreshes
// it every RefreshInterval and refetches early (at most every MinRefetch)
// when a token names a key ID it does not know, which is how a rotation
// reaches the gateway before its cache expires.
//
// # Porting notes
//
// Rust: a reqwest GET with a timeout feeding the same cache logic.
package jwks

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// maxDocumentBytes bounds the response; a key set is a few hundred bytes
// per key.
const maxDocumentBytes = 1 << 20

// Fetch returns a fetch function for url. A nil client gets one with a
// five-second timeout and tracing.
func Fetch(url string, client *http.Client) func(ctx context.Context) ([]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, Transport: otelhttp.NewTransport(http.DefaultTransport)}
	}
	return func(ctx context.Context) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("jwks: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("jwks: %s answered %s", url, resp.Status)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes+1))
		if err != nil {
			return nil, fmt.Errorf("jwks: read: %w", err)
		}
		if len(data) > maxDocumentBytes {
			return nil, fmt.Errorf("jwks: document larger than %d bytes", maxDocumentBytes)
		}
		return data, nil
	}
}
