package contracttest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/protobuf/encoding/protojson"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
)

// The generated REST handlers are driven in memory (no listener) against
// servers that implement nothing, so each request fails with UNIMPLEMENTED
// naming the RPC it reached. That proves routing for every route, including
// custom verbs such as "/v1/notes/{noteId}:restore" that share a prefix with
// "/v1/notes/{noteId}".

var jsonPathVariable = regexp.MustCompile(`\{[A-Za-z0-9]+\}`)

type unimplementedServers struct {
	skeinv1.UnimplementedIdentityServiceServer
	skeinv1.UnimplementedDocumentServiceServer
	skeinv1.UnimplementedSearchServiceServer
	skeinv1.UnimplementedGraphServiceServer
	skeinv1.UnimplementedMLServiceServer
	skeinv1.UnimplementedAgentServiceServer
	skeinv1.UnimplementedNotificationServiceServer
}

// recordingDocuments captures the requests that path and query binding
// produce, for the RPCs whose binding is easiest to get wrong.
type recordingDocuments struct {
	skeinv1.UnimplementedDocumentServiceServer
	restored *skeinv1.RestoreNoteRequest
	got      *skeinv1.GetNoteRequest
}

func (r *recordingDocuments) RestoreNote(_ context.Context, req *skeinv1.RestoreNoteRequest) (*skeinv1.RestoreNoteResponse, error) {
	r.restored = req
	return &skeinv1.RestoreNoteResponse{}, nil
}

func (r *recordingDocuments) GetNote(_ context.Context, req *skeinv1.GetNoteRequest) (*skeinv1.GetNoteResponse, error) {
	r.got = req
	return &skeinv1.GetNoteResponse{Note: &skeinv1.Note{Id: req.GetNoteId()}}, nil
}

type recordingGraph struct {
	skeinv1.UnimplementedGraphServiceServer
	shortest *skeinv1.ShortestPathRequest
	blast    *skeinv1.BlastRadiusRequest
}

func (r *recordingGraph) ShortestPath(_ context.Context, req *skeinv1.ShortestPathRequest) (*skeinv1.ShortestPathResponse, error) {
	r.shortest = req
	return &skeinv1.ShortestPathResponse{}, nil
}

func (r *recordingGraph) BlastRadius(_ context.Context, req *skeinv1.BlastRadiusRequest) (*skeinv1.BlastRadiusResponse, error) {
	r.blast = req
	return &skeinv1.BlastRadiusResponse{}, nil
}

func newMux(t *testing.T, documents skeinv1.DocumentServiceServer, graph skeinv1.GraphServiceServer) *runtime.ServeMux {
	t.Helper()
	ctx := context.Background()
	mux := runtime.NewServeMux()
	var s unimplementedServers
	register := []error{
		skeinv1.RegisterIdentityServiceHandlerServer(ctx, mux, &s.UnimplementedIdentityServiceServer),
		skeinv1.RegisterDocumentServiceHandlerServer(ctx, mux, documents),
		skeinv1.RegisterSearchServiceHandlerServer(ctx, mux, &s.UnimplementedSearchServiceServer),
		skeinv1.RegisterGraphServiceHandlerServer(ctx, mux, graph),
		skeinv1.RegisterMLServiceHandlerServer(ctx, mux, &s.UnimplementedMLServiceServer),
		skeinv1.RegisterAgentServiceHandlerServer(ctx, mux, &s.UnimplementedAgentServiceServer),
		skeinv1.RegisterNotificationServiceHandlerServer(ctx, mux, &s.UnimplementedNotificationServiceServer),
	}
	for _, err := range register {
		if err != nil {
			t.Fatal(err)
		}
	}
	return mux
}

func serve(mux http.Handler, verb, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(verb, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestEveryRESTRouteReachesItsRPC(t *testing.T) {
	var s unimplementedServers
	mux := newMux(t, &s.UnimplementedDocumentServiceServer, &s.UnimplementedGraphServiceServer)
	for _, r := range routes(t) {
		target := jsonPathVariable.ReplaceAllString(r.path, "id1")
		body := ""
		if r.body != "" {
			body = "{}"
		}
		rec := serve(mux, r.verb, target, body)
		want := "method " + string(r.method.Name()) + " not implemented"
		if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s %s: got %d %s, want 501 from %s", r.verb, target, rec.Code, rec.Body.String(), r.method.FullName())
		}
	}
}

func TestCustomVerbBindsThePathVariableWithoutTheVerb(t *testing.T) {
	documents := &recordingDocuments{}
	mux := newMux(t, documents, &recordingGraph{})
	rec := serve(mux, http.MethodPost, "/v1/notes/01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f:restore", "{}")
	if rec.Code != http.StatusOK || documents.restored == nil {
		t.Fatalf("restore: got %d %s", rec.Code, rec.Body.String())
	}
	if got := documents.restored.GetNoteId(); got != "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f" {
		t.Errorf("noteId = %q", got)
	}
}

func TestGetBindsPathAndLowerCamelCaseQueryAndAnswersInJSONNames(t *testing.T) {
	documents := &recordingDocuments{}
	mux := newMux(t, documents, &recordingGraph{})
	rec := serve(mux, http.MethodGet, "/v1/notes/n1?includeMarkdown=true", "")
	if rec.Code != http.StatusOK || documents.got == nil {
		t.Fatalf("get: got %d %s", rec.Code, rec.Body.String())
	}
	if documents.got.GetNoteId() != "n1" || !documents.got.GetIncludeMarkdown() {
		t.Errorf("request = %v", documents.got)
	}
	var reply skeinv1.GetNoteResponse
	if err := protojson.Unmarshal(rec.Body.Bytes(), &reply); err != nil || reply.GetNote().GetId() != "n1" {
		t.Errorf("reply %s: %v", rec.Body.String(), err)
	}
}

func TestGraphRoutesBindTwoPathVariablesAndEnumQueryByName(t *testing.T) {
	graph := &recordingGraph{}
	mux := newMux(t, &recordingDocuments{}, graph)
	if rec := serve(mux, http.MethodGet, "/v1/notes/a/shortest-path/b?metric=PATH_METRIC_SEMANTIC", ""); rec.Code != http.StatusOK {
		t.Fatalf("shortest path: %d %s", rec.Code, rec.Body.String())
	}
	if graph.shortest.GetFrom() != "a" || graph.shortest.GetTo() != "b" || graph.shortest.GetMetric() != skeinv1.PathMetric_PATH_METRIC_SEMANTIC {
		t.Errorf("shortest path request = %v", graph.shortest)
	}
	if rec := serve(mux, http.MethodGet, "/v1/notes/a/blast-radius?maxHops=2&direction=BLAST_DIRECTION_CITES", ""); rec.Code != http.StatusOK {
		t.Fatalf("blast radius: %d %s", rec.Code, rec.Body.String())
	}
	if graph.blast.GetMaxHops() != 2 || graph.blast.GetDirection() != skeinv1.BlastDirection_BLAST_DIRECTION_CITES {
		t.Errorf("blast radius request = %v", graph.blast)
	}
}

func TestInternalRPCsAreNotRoutable(t *testing.T) {
	var s unimplementedServers
	mux := newMux(t, &s.UnimplementedDocumentServiceServer, &s.UnimplementedGraphServiceServer)
	for _, target := range []string{"/v1/authorize", "/skein.v1.IdentityService/Authorize", "/v1/readable-notes", "/v1/delegated-tokens"} {
		if rec := serve(mux, http.MethodPost, target, "{}"); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s: got %d, want 404", target, rec.Code)
		}
	}
}
