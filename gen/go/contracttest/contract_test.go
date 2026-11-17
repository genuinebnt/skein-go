package contracttest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
)

// repoRoot is the repository root relative to this package's directory,
// which is the working directory of `go test`.
const repoRoot = "../../.."

// internalMethods are RPCs only other services call. They must have no REST
// route; adding one would expose them through the gateway.
var internalMethods = map[protoreflect.FullName]bool{
	"skein.v1.IdentityService.Authorize":           true,
	"skein.v1.IdentityService.ListReadableNotes":   true,
	"skein.v1.IdentityService.IssueDelegatedToken": true,
	"skein.v1.IdentityService.RegisterNote":        true,
}

// servicePages maps each service to its page in docs/api. A new service
// fails TestEveryServiceHasADocsPage until it is added here and documented.
var servicePages = map[protoreflect.FullName]string{
	"skein.v1.IdentityService":     "identity.md",
	"skein.v1.DocumentService":     "documents.md",
	"skein.v1.SearchService":       "search.md",
	"skein.v1.GraphService":        "graph.md",
	"skein.v1.MLService":           "ml.md",
	"skein.v1.AgentService":        "agent.md",
	"skein.v1.NotificationService": "notifications.md",
}

// route is one google.api.http rule, with the path written as the OpenAPI
// document and the docs write it: variables use request-field JSON names.
type route struct {
	method protoreflect.MethodDescriptor
	verb   string
	path   string
	body   string
}

func (r route) key() string { return r.verb + " " + r.path }

func (r route) operationID() string {
	return fmt.Sprintf("%s_%s", r.method.Parent().Name(), r.method.Name())
}

// services lists every service in package skein.v1, sorted by name so
// failures are reported in a stable order.
func services(t *testing.T) []protoreflect.ServiceDescriptor {
	t.Helper()
	// Referencing the package guarantees its descriptors are registered.
	_ = skeinv1.File_skein_v1_api_proto
	var found []protoreflect.ServiceDescriptor
	protoregistry.GlobalFiles.RangeFilesByPackage("skein.v1", func(f protoreflect.FileDescriptor) bool {
		for i := 0; i < f.Services().Len(); i++ {
			found = append(found, f.Services().Get(i))
		}
		return true
	})
	if len(found) == 0 {
		t.Fatal("no skein.v1 services registered")
	}
	sort.Slice(found, func(i, j int) bool { return found[i].FullName() < found[j].FullName() })
	return found
}

func methods(t *testing.T) []protoreflect.MethodDescriptor {
	t.Helper()
	var all []protoreflect.MethodDescriptor
	for _, s := range services(t) {
		for i := 0; i < s.Methods().Len(); i++ {
			all = append(all, s.Methods().Get(i))
		}
	}
	return all
}

func httpRule(m protoreflect.MethodDescriptor) *annotations.HttpRule {
	opts := m.Options()
	if opts == nil || !proto.HasExtension(opts, annotations.E_Http) {
		return nil
	}
	return proto.GetExtension(opts, annotations.E_Http).(*annotations.HttpRule)
}

func isStreaming(m protoreflect.MethodDescriptor) bool {
	return m.IsStreamingClient() || m.IsStreamingServer()
}

var pathVariable = regexp.MustCompile(`\{([a-z_][a-z0-9_]*)(=[^}]*)?\}`)

// jsonPath rewrites "{note_id}" to "{noteId}", failing if a variable names no
// field of the request.
func jsonPath(t *testing.T, m protoreflect.MethodDescriptor, template string) string {
	t.Helper()
	return pathVariable.ReplaceAllStringFunc(template, func(v string) string {
		name := pathVariable.FindStringSubmatch(v)[1]
		field := m.Input().Fields().ByName(protoreflect.Name(name))
		if field == nil {
			t.Errorf("%s: path variable %q is not a field of %s", m.FullName(), name, m.Input().FullName())
			return v
		}
		return "{" + field.JSONName() + "}"
	})
}

func routes(t *testing.T) []route {
	t.Helper()
	var all []route
	for _, m := range methods(t) {
		rule := httpRule(m)
		if rule == nil {
			continue
		}
		var verb, template string
		switch p := rule.GetPattern().(type) {
		case *annotations.HttpRule_Get:
			verb, template = "GET", p.Get
		case *annotations.HttpRule_Post:
			verb, template = "POST", p.Post
		case *annotations.HttpRule_Put:
			verb, template = "PUT", p.Put
		case *annotations.HttpRule_Patch:
			verb, template = "PATCH", p.Patch
		case *annotations.HttpRule_Delete:
			verb, template = "DELETE", p.Delete
		default:
			t.Errorf("%s: unsupported HTTP pattern %T", m.FullName(), p)
			continue
		}
		all = append(all, route{method: m, verb: verb, path: jsonPath(t, m, template), body: rule.GetBody()})
	}
	return all
}

func readRepoFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return data
}

func TestEveryRPCIsInternalStreamingOrHasOneRESTRoute(t *testing.T) {
	for _, m := range methods(t) {
		rule := httpRule(m)
		switch {
		case internalMethods[m.FullName()]:
			if rule != nil {
				t.Errorf("%s is internal but has an HTTP route; the gateway must not expose it", m.FullName())
			}
		case isStreaming(m):
			if rule != nil {
				t.Errorf("%s streams, which grpc-gateway cannot serve; remove its HTTP route and document its WebSocket", m.FullName())
			}
		case rule == nil:
			t.Errorf("%s has no google.api.http route; add one or list it in internalMethods", m.FullName())
		case len(rule.GetAdditionalBindings()) > 0:
			t.Errorf("%s has additional bindings; each RPC gets exactly one route", m.FullName())
		}
	}
	for name := range internalMethods {
		if _, err := protoregistry.GlobalFiles.FindDescriptorByName(name); err != nil {
			t.Errorf("internalMethods lists %s, which does not exist", name)
		}
	}
}

func TestRESTRoutesAreUnique(t *testing.T) {
	seen := map[string]protoreflect.FullName{}
	for _, r := range routes(t) {
		if other, dup := seen[r.key()]; dup {
			t.Errorf("%s is served by both %s and %s", r.key(), other, r.method.FullName())
		}
		seen[r.key()] = r.method.FullName()
	}
}

type openAPIOperation struct {
	OperationID string   `json:"operationId"`
	Summary     string   `json:"summary"`
	Tags        []string `json:"tags"`
}

func openAPIOperations(t *testing.T) map[string]openAPIOperation {
	t.Helper()
	var doc struct {
		Swagger string                                 `json:"swagger"`
		Paths   map[string]map[string]openAPIOperation `json:"paths"`
	}
	if err := json.Unmarshal(readRepoFile(t, "gen/openapi/skein.swagger.json"), &doc); err != nil {
		t.Fatalf("gen/openapi/skein.swagger.json is not valid JSON: %v", err)
	}
	if doc.Swagger != "2.0" {
		t.Fatalf("swagger version = %q, want 2.0", doc.Swagger)
	}
	ops := map[string]openAPIOperation{}
	for path, verbs := range doc.Paths {
		for verb, op := range verbs {
			ops[strings.ToUpper(verb)+" "+path] = op
		}
	}
	return ops
}

func TestOpenAPIHasExactlyOneOperationPerRoute(t *testing.T) {
	ops := openAPIOperations(t)
	all := routes(t)
	for _, r := range all {
		op, ok := ops[r.key()]
		if !ok {
			t.Errorf("OpenAPI has no operation for %s (%s); run make proto", r.key(), r.method.FullName())
			continue
		}
		if op.OperationID != r.operationID() {
			t.Errorf("%s: operationId = %q, want %q", r.key(), op.OperationID, r.operationID())
		}
		if op.Summary == "" {
			t.Errorf("%s: no summary; start the comment on %s with a summary paragraph", r.key(), r.method.FullName())
		}
		if len(op.Tags) == 0 {
			t.Errorf("%s: no tags", r.key())
		}
	}
	if len(ops) != len(all) {
		t.Errorf("OpenAPI has %d operations, contracts have %d routes", len(ops), len(all))
	}
	t.Logf("%d routes, %d OpenAPI operations", len(all), len(ops))
}

func TestEveryServiceHasADocsPage(t *testing.T) {
	for _, s := range services(t) {
		page, ok := servicePages[s.FullName()]
		if !ok {
			t.Errorf("%s has no entry in servicePages", s.FullName())
			continue
		}
		readRepoFile(t, filepath.Join("docs/api", page))
	}
}

func TestDocsListEveryRouteWithExamples(t *testing.T) {
	examples, _ := docExamples(t)
	for _, r := range routes(t) {
		page := servicePages[r.method.Parent().FullName()]
		text := string(readRepoFile(t, filepath.Join("docs/api", page)))
		if !strings.Contains(text, "`"+r.key()+"`") {
			t.Errorf("docs/api/%s does not list `%s` (%s)", page, r.key(), r.method.FullName())
		}
		if !examples[page][r.method.Output().FullName()] {
			t.Errorf("docs/api/%s has no ```json %s example (response of %s)", page, r.method.Output().FullName(), r.method.Name())
		}
		if r.body != "" && !examples[page][r.method.Input().FullName()] {
			t.Errorf("docs/api/%s has no ```json %s example (request body of %s)", page, r.method.Input().FullName(), r.method.Name())
		}
	}
}

func TestWebSocketDocCoversEveryStreamingRPC(t *testing.T) {
	text := string(readRepoFile(t, "docs/api/websocket.md"))
	all, _ := docExamples(t)
	examples := all["websocket.md"]
	for _, m := range methods(t) {
		if !isStreaming(m) {
			continue
		}
		name := fmt.Sprintf("%s.%s", m.Parent().Name(), m.Name())
		if !strings.Contains(text, "`"+name+"`") {
			t.Errorf("docs/api/websocket.md does not document `%s`", name)
		}
		if !examples[m.Output().FullName()] {
			t.Errorf("docs/api/websocket.md has no ```json %s frame example", m.Output().FullName())
		}
	}
}

var exampleFence = regexp.MustCompile("(?ms)^```json (skein\\.v1\\.[A-Za-z0-9_.]+)\n(.*?)^```")

// docExamples parses and checks every typed JSON example in docs/api. It
// returns, per page, the message types with a valid example, and a problem
// for each invalid one; TestDocExamplesMatchTheirMessages reports those, the
// other tests only use the valid set.
func docExamples(t *testing.T) (valid map[string]map[protoreflect.FullName]bool, problems []string) {
	t.Helper()
	pages, err := filepath.Glob(filepath.Join(repoRoot, "docs/api/*.md"))
	if err != nil || len(pages) == 0 {
		t.Fatalf("no docs/api pages found: %v", err)
	}
	valid = map[string]map[protoreflect.FullName]bool{}
	for _, path := range pages {
		page := filepath.Base(path)
		valid[page] = map[protoreflect.FullName]bool{}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range exampleFence.FindAllSubmatch(data, -1) {
			name := protoreflect.FullName(match[1])
			found := exampleProblems(name, match[2])
			for _, p := range found {
				problems = append(problems, fmt.Sprintf("docs/api/%s: example %s: %s", page, name, p))
			}
			if len(found) == 0 {
				valid[page][name] = true
			}
		}
	}
	return valid, problems
}

func exampleProblems(name protoreflect.FullName, body []byte) []string {
	messageType, err := protoregistry.GlobalTypes.FindMessageByName(name)
	if err != nil {
		return []string{"no such message"}
	}
	if err := protojson.Unmarshal(body, messageType.New().Interface()); err != nil {
		return []string{fmt.Sprintf("does not parse: %v", err)}
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return []string{fmt.Sprintf("invalid JSON: %v", err)}
	}
	return jsonNameProblems(messageType.Descriptor(), value, "")
}

func TestDocExamplesMatchTheirMessages(t *testing.T) {
	valid, problems := docExamples(t)
	for _, p := range problems {
		t.Error(p)
	}
	total := 0
	for page, types := range valid {
		t.Logf("docs/api/%s: valid examples of %d message types", page, len(types))
		total += len(types)
	}
	if total == 0 {
		t.Error("no ```json skein.v1.Message examples found in docs/api")
	}
}

// jsonNameProblems reports keys that are not the lowerCamelCase JSON name of
// a field (protojson also accepts the proto name, so parsing alone does not
// catch "note_id"), and 64-bit integers written as JSON numbers, which lose
// precision in JavaScript.
func jsonNameProblems(md protoreflect.MessageDescriptor, value any, at string) []string {
	if strings.HasPrefix(string(md.FullName()), "google.protobuf.") {
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("%s: want a JSON object for %s", at, md.FullName())}
	}
	keys := make([]string, 0, len(object))
	for k := range object {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var problems []string
	for _, key := range keys {
		field := md.Fields().ByJSONName(key)
		if field == nil || field.JSONName() != key {
			problems = append(problems, fmt.Sprintf("%s.%s: not a lowerCamelCase field of %s", at, key, md.FullName()))
			continue
		}
		problems = append(problems, fieldProblems(field, object[key], at+"."+key)...)
	}
	return problems
}

func fieldProblems(field protoreflect.FieldDescriptor, value any, at string) []string {
	switch {
	case field.IsMap():
		entries, _ := value.(map[string]any)
		var problems []string
		for k, v := range entries {
			problems = append(problems, singularProblems(field.MapValue(), v, at+"["+k+"]")...)
		}
		return problems
	case field.IsList():
		items, _ := value.([]any)
		var problems []string
		for i, v := range items {
			problems = append(problems, singularProblems(field, v, fmt.Sprintf("%s[%d]", at, i))...)
		}
		return problems
	default:
		return singularProblems(field, value, at)
	}
}

func singularProblems(field protoreflect.FieldDescriptor, value any, at string) []string {
	switch field.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return jsonNameProblems(field.Message(), value, at)
	case protoreflect.Int64Kind, protoreflect.Uint64Kind, protoreflect.Sint64Kind,
		protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind:
		if _, isString := value.(string); !isString {
			return []string{fmt.Sprintf("%s: 64-bit integers are JSON strings", at)}
		}
	}
	return nil
}

func TestExampleCheckerRejectsProtoNamesNumericUint64AndUnknownFields(t *testing.T) {
	md := (&skeinv1.ApplyOpsRequest{}).ProtoReflect().Descriptor()
	cases := map[string]string{
		"proto name":     `{"note_id": "n", "replicaId": "1"}`,
		"numeric uint64": `{"noteId": "n", "replicaId": 1}`,
		"unknown field":  `{"noteId": "n", "replica": "1"}`,
		"nested":         `{"version": {"entries": [{"replica_id": "1"}]}}`,
	}
	nested := (&skeinv1.GetTextAtRequest{}).ProtoReflect().Descriptor()
	for name, body := range cases {
		var value any
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			t.Fatal(err)
		}
		target := md
		if name == "nested" {
			target = nested
		}
		if len(jsonNameProblems(target, value, "")) == 0 {
			t.Errorf("%s: %s was accepted", name, body)
		}
	}
	var good any
	_ = json.Unmarshal([]byte(`{"noteId": "n", "replicaId": "1", "ops": "AQI="}`), &good)
	if p := jsonNameProblems(md, good, ""); len(p) != 0 {
		t.Errorf("valid example rejected: %v", p)
	}
}
