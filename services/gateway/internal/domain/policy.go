package domain

import "net/http"

// Budget names a rate-limit budget. The values are the
// skein.ratelimit.budget metric attribute in docs/observability.md.
type Budget string

// The four budgets.
const (
	BudgetAPI    Budget = "api"
	BudgetSearch Budget = "search"
	BudgetSync   Budget = "sync"
	BudgetAgent  Budget = "agent"
)

// Budgets lists every budget in a fixed order.
var Budgets = []Budget{BudgetAPI, BudgetSearch, BudgetSync, BudgetAgent}

// SubjectKind says whom a budget is counted for. The values are the
// skein.ratelimit.subject metric attribute.
type SubjectKind string

// The two subject kinds.
const (
	SubjectUser SubjectKind = "user"
	SubjectIP   SubjectKind = "ip"
)

// RoutePolicy is what the gateway does around one RPC's REST route.
type RoutePolicy struct {
	// Method is the RPC's full name, "/skein.v1.DocumentService/GetNote".
	Method string
	// Public routes need no access token.
	Public bool
	// Budget is spent once per request.
	Budget Budget
	// AcceptsIdempotencyKey is true when an Idempotency-Key header is
	// honoured; otherwise the header is ignored.
	AcceptsIdempotencyKey bool
}

// Full RPC names the policy treats specially.
const (
	MethodSignUp          = "/skein.v1.IdentityService/SignUp"
	MethodSignIn          = "/skein.v1.IdentityService/SignIn"
	MethodSignInWithOAuth = "/skein.v1.IdentityService/SignInWithOAuth"
	MethodRefresh         = "/skein.v1.IdentityService/Refresh"
	MethodSignOut         = "/skein.v1.IdentityService/SignOut"
	MethodApplyOps        = "/skein.v1.DocumentService/ApplyOps"
	MethodMarkRead        = "/skein.v1.NotificationService/MarkRead"
	MethodSearch          = "/skein.v1.SearchService/Search"
	MethodSuggestTitles   = "/skein.v1.SearchService/SuggestTitles"
	MethodSemanticSearch  = "/skein.v1.MLService/SemanticSearch"
)

var publicMethods = map[string]bool{
	MethodSignUp: true, MethodSignIn: true, MethodSignInWithOAuth: true,
	MethodRefresh: true, MethodSignOut: true,
}

var methodBudgets = map[string]Budget{
	MethodSearch:         BudgetSearch,
	MethodSuggestTitles:  BudgetSearch,
	MethodSemanticSearch: BudgetSearch,
	MethodApplyOps:       BudgetSync,
}

// naturallyIdempotent POSTs ignore Idempotency-Key (docs/api/README.md):
// CRDT operations apply once, and marking read twice changes nothing.
var naturallyIdempotent = map[string]bool{MethodApplyOps: true, MethodMarkRead: true}

// IsPublicMethod reports whether an RPC works without an access token.
func IsPublicMethod(fullMethod string) bool { return publicMethods[fullMethod] }

// PolicyFor returns the policy for the REST route of fullMethod served
// with httpVerb.
func PolicyFor(fullMethod, httpVerb string) RoutePolicy {
	budget, ok := methodBudgets[fullMethod]
	if !ok {
		budget = BudgetAPI
	}
	public := publicMethods[fullMethod]
	return RoutePolicy{
		Method:                fullMethod,
		Public:                public,
		Budget:                budget,
		AcceptsIdempotencyKey: httpVerb == http.MethodPost && !public && !naturallyIdempotent[fullMethod],
	}
}
