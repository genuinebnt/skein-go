package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadyzReportsTheFirstFailingCheck(t *testing.T) {
	var dbErr error
	h := Handler(Check{Name: "postgres", Ready: func(context.Context) error { return dbErr }})
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	if get("/readyz").Code != http.StatusOK || get("/healthz").Code != http.StatusOK {
		t.Fatal("healthy service must be ready")
	}
	dbErr = errors.New("down")
	if rec := get("/readyz"); rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "postgres not ready\n" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if get("/healthz").Code != http.StatusOK {
		t.Fatal("liveness must not depend on dependencies")
	}
}
