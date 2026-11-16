package httpx_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/genuinebnt/skein-go/platform/httpx"
	"github.com/genuinebnt/skein-go/platform/logging"
)

func Example() {
	log := logging.Discard()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/notes/{id}", func(w http.ResponseWriter, r *http.Request) {
		// A gateway handler returns whatever the backend returned.
		err := status.Error(codes.NotFound, "note not found")
		httpx.WriteError(w, r, log, err)
	})
	handler := httpx.Chain(httpx.Config{
		Operation: "gateway",
		Logger:    log,
		CORS:      httpx.CORSConfig{AllowedOrigins: []string{"https://app.skein.dev"}},
	}, mux)

	req := httptest.NewRequest(http.MethodGet, "/v1/notes/0193", nil)
	req.Header.Set("X-Request-ID", "req-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	fmt.Print(rec.Code, " ", rec.Body.String())
	// Output: 404 {"error":{"code":"NOT_FOUND","message":"note not found","request_id":"req-1"}}
}
