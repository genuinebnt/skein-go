// Package grpc serves SearchService and GraphService: it converts protos
// to use-case calls and back, and use-case errors to gRPC statuses with
// error details (docs/api/README.md, "Errors"). No decision is made here
// that a use case could make.
package grpc

import (
	"context"
	"errors"
	"strconv"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/libs/graph"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
)

// Response metadata of AnalyzeGraph. AnalyzeGraphResponse has no version
// fields yet (a contract follow-up), so the versions travel as headers.
const (
	HeaderGraphVersion   = "skein-graph-version"
	HeaderMetricsVersion = "skein-graph-metrics-version"
	HeaderMetricsPending = "skein-graph-metrics-pending"
)

// Server implements skeinv1.SearchServiceServer and
// skeinv1.GraphServiceServer.
type Server struct {
	skeinv1.UnimplementedSearchServiceServer
	skeinv1.UnimplementedGraphServiceServer
	searcher *app.Searcher
	grapher  *app.Grapher
}

// NewServer returns the handlers.
func NewServer(searcher *app.Searcher, grapher *app.Grapher) *Server {
	return &Server{searcher: searcher, grapher: grapher}
}

// Register registers both services on s.
func (s *Server) Register(r grpc.ServiceRegistrar) {
	skeinv1.RegisterSearchServiceServer(r, s)
	skeinv1.RegisterGraphServiceServer(r, s)
}

func subject(ctx context.Context) (auth.Subject, error) {
	sub, ok := auth.SubjectFromContext(ctx)
	if !ok || sub.UserID == "" {
		return auth.Subject{}, status.Error(codes.Unauthenticated, "no authenticated subject")
	}
	return sub, nil
}

// Search implements SearchService.Search.
func (s *Server) Search(ctx context.Context, req *skeinv1.SearchRequest) (*skeinv1.SearchResponse, error) {
	sub, err := subject(ctx)
	if err != nil {
		return nil, err
	}
	mode := app.ModeText
	switch req.GetMode() {
	case skeinv1.SearchMode_SEARCH_MODE_UNSPECIFIED, skeinv1.SearchMode_SEARCH_MODE_TEXT:
	case skeinv1.SearchMode_SEARCH_MODE_REGEX:
		mode = app.ModeRegex
	default:
		return nil, toStatus(ctx, &app.FieldError{Field: "mode", Description: "unknown search mode"})
	}
	page, err := s.searcher.Search(ctx, app.SearchRequest{
		Subject: sub, WorkspaceID: req.GetWorkspaceId(), Query: req.GetQuery(), Mode: mode,
		CaseSensitive: req.GetCaseSensitive(), WholeWord: req.GetWholeWord(), CodeOnly: req.GetCodeOnly(),
		Languages: req.GetLanguages(), PageSize: int(req.GetPage().GetPageSize()), PageToken: req.GetPage().GetPageToken(),
	})
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	resp := &skeinv1.SearchResponse{Page: &skeinv1.PageResponse{NextPageToken: page.NextPageToken},
		Candidates: uint32(page.Candidates), Matched: uint32(page.Matched)}
	for _, h := range page.Hits {
		resp.Hits = append(resp.Hits, hitProto(h))
	}
	return resp, nil
}

func hitProto(h domain.Hit) *skeinv1.SearchHit {
	out := &skeinv1.SearchHit{
		Location:  &skeinv1.BlockRef{NoteId: h.Note, BlockId: h.Block, Start: uint32(h.Location.Start), End: uint32(h.Location.End)},
		NoteTitle: h.NoteTitle, Snippet: h.Snippet, Score: h.Score, Language: h.Language,
	}
	for _, r := range h.Highlights {
		out.Highlights = append(out.Highlights, &skeinv1.Range{Start: uint32(r.Start), End: uint32(r.End)})
	}
	return out
}

// SuggestTitles implements SearchService.SuggestTitles.
func (s *Server) SuggestTitles(ctx context.Context, req *skeinv1.SuggestTitlesRequest) (*skeinv1.SuggestTitlesResponse, error) {
	sub, err := subject(ctx)
	if err != nil {
		return nil, err
	}
	got, err := s.searcher.SuggestTitles(ctx, app.TitlesRequest{Subject: sub, WorkspaceID: req.GetWorkspaceId(), Prefix: req.GetPrefix(), Limit: int(req.GetLimit())})
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	resp := &skeinv1.SuggestTitlesResponse{}
	for _, t := range got {
		resp.Suggestions = append(resp.Suggestions, &skeinv1.TitleSuggestion{NoteId: t.Note, Title: t.Title, EditDistance: uint32(t.Distance)})
	}
	return resp, nil
}

// GetLinkGraph implements GraphService.GetLinkGraph.
func (s *Server) GetLinkGraph(ctx context.Context, req *skeinv1.GetLinkGraphRequest) (*skeinv1.GetLinkGraphResponse, error) {
	sub, err := subject(ctx)
	if err != nil {
		return nil, err
	}
	lg, err := s.grapher.GetLinkGraph(ctx, sub, req.GetWorkspaceId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	resp := &skeinv1.GetLinkGraphResponse{}
	for _, n := range lg.Nodes {
		resp.Nodes = append(resp.Nodes, &skeinv1.GraphNode{NoteId: n.ID, Title: n.Title, IsRoot: n.Root})
	}
	for _, e := range lg.Edges {
		resp.Edges = append(resp.Edges, &skeinv1.GraphEdge{From: e.From, To: e.To})
	}
	return resp, nil
}

// AnalyzeGraph implements GraphService.AnalyzeGraph.
func (s *Server) AnalyzeGraph(ctx context.Context, req *skeinv1.AnalyzeGraphRequest) (*skeinv1.AnalyzeGraphResponse, error) {
	sub, err := subject(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.grapher.AnalyzeGraph(ctx, sub, req.GetWorkspaceId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	_ = grpc.SetHeader(ctx, metadata.Pairs(
		HeaderGraphVersion, strconv.FormatInt(a.GraphVersion, 10),
		HeaderMetricsVersion, strconv.FormatInt(a.SlowVersion, 10),
		HeaderMetricsPending, strconv.FormatBool(a.SlowPending)))
	return &skeinv1.AnalyzeGraphResponse{
		ComponentOf: toUint32Map(a.ComponentOf), OrphanComponents: toUint32s(a.Orphans),
		SccOf: toUint32Map(a.SCCOf), SccSizes: toUint32s(a.SCCSizes), Cycle: a.Cycle,
		TopologicalOrder: a.Topological, Unplaced: a.Unplaced, Betweenness: a.Betweenness, Pagerank: a.PageRank,
		CommunityOf: toUint32Map(a.CommunityOf), Modularity: a.Modularity, Diameter: uint32(a.Diameter),
	}, nil
}

func toUint32Map(m map[string]int) map[string]uint32 {
	out := make(map[string]uint32, len(m))
	for k, v := range m {
		out[k] = uint32(v)
	}
	return out
}

func toUint32s(xs []int) []uint32 {
	out := make([]uint32, len(xs))
	for i, x := range xs {
		out[i] = uint32(x)
	}
	return out
}

// BlastRadius implements GraphService.BlastRadius.
func (s *Server) BlastRadius(ctx context.Context, req *skeinv1.BlastRadiusRequest) (*skeinv1.BlastRadiusResponse, error) {
	sub, err := subject(ctx)
	if err != nil {
		return nil, err
	}
	dir := graph.CitedBy
	switch req.GetDirection() {
	case skeinv1.BlastDirection_BLAST_DIRECTION_UNSPECIFIED, skeinv1.BlastDirection_BLAST_DIRECTION_CITED_BY:
	case skeinv1.BlastDirection_BLAST_DIRECTION_CITES:
		dir = graph.Cites
	default:
		return nil, toStatus(ctx, &app.FieldError{Field: "direction", Description: "unknown direction"})
	}
	rings, err := s.grapher.BlastRadius(ctx, sub, req.GetNoteId(), int(req.GetMaxHops()), dir)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &skeinv1.BlastRadiusResponse{Hops: toUint32Map(rings.Hops), RingSizes: toUint32s(rings.Sizes)}, nil
}

// ShortestPath implements GraphService.ShortestPath.
func (s *Server) ShortestPath(ctx context.Context, req *skeinv1.ShortestPathRequest) (*skeinv1.ShortestPathResponse, error) {
	sub, err := subject(ctx)
	if err != nil {
		return nil, err
	}
	var semantic bool
	switch req.GetMetric() {
	case skeinv1.PathMetric_PATH_METRIC_UNSPECIFIED, skeinv1.PathMetric_PATH_METRIC_HOPS:
	case skeinv1.PathMetric_PATH_METRIC_SEMANTIC:
		semantic = true
	default:
		return nil, toStatus(ctx, &app.FieldError{Field: "metric", Description: "unknown metric"})
	}
	p, err := s.grapher.ShortestPath(ctx, sub, req.GetFrom(), req.GetTo(), semantic)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &skeinv1.ShortestPathResponse{Found: p.Found, Path: p.Nodes, Cost: p.Cost}, nil
}

// ReadingPath implements GraphService.ReadingPath.
func (s *Server) ReadingPath(ctx context.Context, req *skeinv1.ReadingPathRequest) (*skeinv1.ReadingPathResponse, error) {
	sub, err := subject(ctx)
	if err != nil {
		return nil, err
	}
	steps, err := s.grapher.ReadingPath(ctx, sub, req.GetNoteId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	resp := &skeinv1.ReadingPathResponse{}
	for _, st := range steps {
		resp.Steps = append(resp.Steps, &skeinv1.ReadingStep{NoteId: st.Note, Reason: st.Reason, Depth: uint32(st.Depth)})
	}
	return resp, nil
}

// ReadingPlan implements GraphService.ReadingPlan.
func (s *Server) ReadingPlan(ctx context.Context, req *skeinv1.ReadingPlanRequest) (*skeinv1.ReadingPlanResponse, error) {
	sub, err := subject(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := s.grapher.ReadingPlan(ctx, sub, req.GetWorkspaceId(), req.GetTopic(), int(req.GetMinutes()))
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	resp := &skeinv1.ReadingPlanResponse{TotalMinutes: uint32(plan.TotalMinutes), TotalValue: plan.TotalValue}
	for _, n := range plan.Notes {
		resp.Notes = append(resp.Notes, &skeinv1.PlannedNote{NoteId: n.Note, Minutes: uint32(n.Minutes), Value: n.Value, Reason: n.Reason})
	}
	return resp, nil
}

// toStatus maps a use-case error to a status with the documented details.
// Every status carries RequestInfo; messages never contain note contents.
func toStatus(ctx context.Context, err error) error {
	if st, ok := status.FromError(err); ok {
		return st.Err() // already a status, from an interceptor or a client
	}
	requestInfo := &errdetails.RequestInfo{RequestId: logging.RequestID(ctx)}
	var field *app.FieldError
	var notFound *app.NotFoundError
	var st *status.Status
	switch {
	case errors.As(err, &field):
		st = status.New(codes.InvalidArgument, field.Error())
		st, _ = st.WithDetails(&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{
			{Field: field.Field, Description: field.Description}}}, requestInfo)
	case errors.As(err, &notFound):
		st = status.New(codes.NotFound, notFound.Error())
		st, _ = st.WithDetails(&errdetails.ResourceInfo{ResourceType: notFound.ResourceType, ResourceName: notFound.Name}, requestInfo)
	case errors.Is(err, app.ErrNotFound):
		st = status.New(codes.NotFound, "not found")
		st, _ = st.WithDetails(requestInfo)
	case errors.Is(err, app.ErrUnavailable):
		st = status.New(codes.Unavailable, "a dependency is unavailable; retry with backoff")
		st, _ = st.WithDetails(requestInfo)
	case errors.Is(err, context.DeadlineExceeded):
		st = status.New(codes.DeadlineExceeded, "deadline exceeded")
	case errors.Is(err, context.Canceled):
		st = status.New(codes.Canceled, "cancelled")
	default:
		st = status.New(codes.Internal, "internal error")
		st, _ = st.WithDetails(requestInfo)
	}
	return st.Err()
}
