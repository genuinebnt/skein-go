package httpapi

import (
	"fmt"
	"regexp"
	"sort"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	// Registers the skein.v1 descriptors this file reads.
	_ "github.com/genuinebnt/skein-go/gen/go/skein/v1"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// Route is one REST route from a google.api.http annotation.
type Route struct {
	Verb string
	// Template is the annotation's path ("/v1/notes/{note_id}:restore").
	Template string
	// Method is the RPC's full name.
	Method string
	Policy domain.RoutePolicy
}

// RouteTable maps grpc-gateway's matched pattern to the route. It is
// built from the proto descriptors, the same source grpc-gateway's
// generated code comes from, so the two cannot drift; a pattern missing
// from the table fails closed (see policyMiddleware).
type RouteTable struct {
	byPattern map[string]Route
	routes    []Route
}

var bareVariable = regexp.MustCompile(`\{([A-Za-z0-9_.]+)\}`)

// patternKey is how grpc-gateway's runtime.Pattern.String() spells a
// template: a bare "{x}" variable is printed as "{x=*}".
func patternKey(verb, template string) string {
	return verb + " " + bareVariable.ReplaceAllString(template, "{$1=*}")
}

// LoadRoutes reads every annotated RPC in the skein.v1 package.
func LoadRoutes() (*RouteTable, error) {
	t := &RouteTable{byPattern: map[string]Route{}}
	var err error
	protoregistry.GlobalFiles.RangeFilesByPackage("skein.v1", func(fd protoreflect.FileDescriptor) bool {
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			methods := services.Get(i).Methods()
			for j := 0; j < methods.Len(); j++ {
				if err = t.add(methods.Get(j)); err != nil {
					return false
				}
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(t.routes, func(i, j int) bool { return t.routes[i].Method < t.routes[j].Method })
	return t, nil
}

func (t *RouteTable) add(m protoreflect.MethodDescriptor) error {
	rule, ok := proto.GetExtension(m.Options(), annotations.E_Http).(*annotations.HttpRule)
	if !ok || rule == nil {
		return nil
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
		return fmt.Errorf("httpapi: %s has an unsupported HTTP pattern %T", m.FullName(), p)
	}
	method := "/" + string(m.Parent().FullName()) + "/" + string(m.Name())
	r := Route{Verb: verb, Template: template, Method: method, Policy: domain.PolicyFor(method, verb)}
	key := patternKey(verb, template)
	if prev, dup := t.byPattern[key]; dup {
		return fmt.Errorf("httpapi: %s and %s share the route %s", prev.Method, method, key)
	}
	t.byPattern[key] = r
	t.routes = append(t.routes, r)
	return nil
}

// Lookup finds the route grpc-gateway matched.
func (t *RouteTable) Lookup(verb, pattern string) (Route, bool) {
	r, ok := t.byPattern[verb+" "+pattern]
	return r, ok
}

// Routes returns every route, sorted by RPC name.
func (t *RouteTable) Routes() []Route { return append([]Route(nil), t.routes...) }
